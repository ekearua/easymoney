package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

const (
	apiKeyMaxBodyBytes = 1 << 20 // 1 MiB
	apiKeyTimestampTTL = 5 * time.Minute
	apiMinAmountKobo   = 100
	apiMaxAmountKobo   = 1_000_000_000
	apiMaxReferenceLen = 64
)

// apiKeyAuth is the authenticated principal extracted from a valid Partner API
// request.
type apiKeyAuth struct {
	keyID    uuid.UUID
	keyName  string
	merchant store.Merchant
}

type apiKeyContextKey struct{}

func apiKeyAuthFromContext(ctx context.Context) (apiKeyAuth, bool) {
	value, ok := ctx.Value(apiKeyContextKey{}).(apiKeyAuth)
	return value, ok
}

// newMerchantAPIKey mints a single-part Partner API key. The key doubles as the
// HMAC signing secret; only its SHA-256 hash is ever persisted.
func (a *App) newMerchantAPIKey() (plaintext string, hash []byte, prefix string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, "", err
	}
	if a.cfg.Environment == "production" {
		prefix = "xeg_sk_live_"
	} else {
		prefix = "xeg_sk_test_"
	}
	plaintext = prefix + base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, sum[:], prefix, nil
}

// signAPIRequest recomputes the canonical HMAC over the request line parts.
func signAPIRequest(key, method, path, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(method))
	mac.Write([]byte("\n"))
	mac.Write([]byte(path))
	mac.Write([]byte("\n"))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// requireAPIKey authenticates a Partner API request via X-Xego-Key,
// X-Xego-Timestamp, and X-Xego-Signature. The signature is HMAC-SHA256(key,
// METHOD\nPATH\nTIMESTAMP\nBODY) hex-encoded, where key is the credential
// itself. The raw request body is consumed for verification and then restored
// for the handler.
func (a *App) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Xego-Key")
		rawTimestamp := r.Header.Get("X-Xego-Timestamp")
		signature := r.Header.Get("X-Xego-Signature")
		if key == "" || rawTimestamp == "" || signature == "" {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing X-Xego-Key, X-Xego-Timestamp, or X-Xego-Signature header")
			return
		}
		timestamp, err := strconv.ParseInt(rawTimestamp, 10, 64)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "X-Xego-Timestamp must be unix seconds")
			return
		}
		if delta := time.Now().Unix() - timestamp; delta > int64(apiKeyTimestampTTL.Seconds()) || delta < -int64(apiKeyTimestampTTL.Seconds()) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "X-Xego-Timestamp is stale")
			return
		}
		sum := sha256.Sum256([]byte(key))
		view, err := a.store.MerchantAPIKeyByKeyHash(r.Context(), sum[:])
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unknown API key")
			return
		}
		if !view.Enabled || view.RevokedAt != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "API key is revoked or disabled")
			return
		}
		if !view.MerchantActive {
			writeAPIError(w, http.StatusForbidden, "merchant_inactive", "merchant account is not active")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, apiKeyMaxBodyBytes+1))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "could not read request body")
			return
		}
		if len(body) > apiKeyMaxBodyBytes {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		expected := signAPIRequest(key, r.Method, r.URL.EscapedPath(), rawTimestamp, body)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature)))) != 1 {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "invalid signature")
			return
		}
		if allowed, _ := a.rateLimiter.Allow(r.Context(), "apikey:"+view.ID.String(), a.cfg.RateLimitAPIKeysPerMinute, time.Minute); !allowed {
			writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "API key rate limit exceeded")
			return
		}
		_ = a.store.TouchMerchantAPIKey(r.Context(), view.ID)
		merchant, err := a.store.MerchantByID(r.Context(), view.MerchantID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal", "merchant lookup failed")
			return
		}
		ctx := context.WithValue(r.Context(), apiKeyContextKey{}, apiKeyAuth{keyID: view.ID, keyName: view.Name, merchant: merchant})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type apiCreatePaymentRequest struct {
	Reference      string         `json:"reference"`
	IdempotencyKey string         `json:"idempotency_key"`
	Amount         apiAmount      `json:"amount"`
	Customer       apiCustomer    `json:"customer"`
	RedirectURL    string         `json:"redirect_url"`
	Metadata       map[string]any `json:"metadata"`
}

type apiAmount struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency"`
}

type apiCustomer struct {
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// apiCreatePayment initiates a card payment through the Partner API. It is
// idempotent on the merchant-supplied reference: replaying the same reference
// returns the existing payment instead of creating a duplicate.
func (a *App) apiCreatePayment(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	var req apiCreatePaymentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	reference := strings.TrimSpace(req.Reference)
	if reference == "" || len(reference) > apiMaxReferenceLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "reference_invalid", "reference is required and must be at most 64 characters")
		return
	}
	if req.Amount.Currency != "NGN" {
		writeAPIError(w, http.StatusUnprocessableEntity, "currency_invalid", "only NGN is supported")
		return
	}
	if req.Amount.Value < apiMinAmountKobo || req.Amount.Value > apiMaxAmountKobo {
		writeAPIError(w, http.StatusUnprocessableEntity, "amount_out_of_range", "amount must be between NGN 1.00 and NGN 10,000,000.00")
		return
	}
	phone := domain.CanonicalE164Phone(req.Customer.Phone)
	if len(strings.TrimPrefix(phone, "+")) < 10 {
		writeAPIError(w, http.StatusUnprocessableEntity, "customer_invalid", "customer.phone must be a valid E.164 number")
		return
	}
	ctx := r.Context()

	existing, err := a.store.PaymentByMerchantReference(ctx, auth.merchant.ID, reference)
	if err == nil {
		a.writePaymentJSON(w, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		a.logger.Error("api payment lookup failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "payment lookup failed")
		return
	}

	user, err := a.store.GetOrCreateUser(ctx, phone)
	if err != nil {
		a.logger.Error("api user resolution failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "customer resolution failed")
		return
	}
	if email := strings.TrimSpace(strings.ToLower(req.Customer.Email)); email != "" {
		_ = a.store.UpdateUserEmail(ctx, user.ID, email)
	}

	payment, err := a.payments.CreateDraftForProvider(ctx, user, auth.merchant, req.Amount.Value, service.ProviderPaystack, service.ChannelAPI, phone)
	if err != nil {
		a.logger.Error("api payment creation failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "payment creation failed")
		return
	}

	meta, err := json.Marshal(map[string]any{
		"idempotency_key": req.IdempotencyKey,
		"redirect_url":    req.RedirectURL,
		"metadata":        req.Metadata,
	})
	if err != nil {
		meta = []byte(`{}`)
	}
	if err := a.store.SetPaymentInitiationMeta(ctx, payment.ID, reference, meta); err != nil {
		// Unique-violation from a concurrent replay of the same reference:
		// return the payment that won the race.
		if winner, lookupErr := a.store.PaymentByMerchantReference(ctx, auth.merchant.ID, reference); lookupErr == nil {
			a.writePaymentJSON(w, winner)
			return
		}
		a.logger.Error("api payment metadata failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "payment creation failed")
		return
	}
	payment.MerchantReference = reference

	_, _ = a.store.AppendAuditLog(ctx, store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: auth.merchant.ID, Valid: true},
		Action:       "api.payments.create",
		ResourceType: sql.NullString{String: "payment", Valid: true},
		ResourceID:   sql.NullString{String: payment.ID.String(), Valid: true},
		Details:      map[string]any{"reference": reference, "amount_kobo": req.Amount.Value},
	})
	a.writePaymentJSON(w, payment)
}

// apiPaymentStatus returns the current state of a merchant-initiated payment.
func (a *App) apiPaymentStatus(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	payment, err := a.store.PaymentByMerchantReference(r.Context(), auth.merchant.ID, reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no payment with that reference")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "payment lookup failed")
		return
	}
	a.writePaymentJSON(w, payment)
}

// apiVerifyPayment re-checks the payment against the gateway and returns the
// authoritative result. This is the Partner API mirror of VerifyAndApply.
func (a *App) apiVerifyPayment(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	payment, err := a.store.PaymentByMerchantReference(r.Context(), auth.merchant.ID, reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no payment with that reference")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "payment lookup failed")
		return
	}
	if payment.Provider != service.ProviderPaystack {
		writeAPIError(w, http.StatusConflict, "unsupported_rail", "verification is only supported for card payments")
		return
	}
	updated, _, err := a.payments.VerifyAndApply(r.Context(), payment.ProviderReference, "api.verify")
	if err != nil {
		a.logger.Warn("api verification failed", "payment_id", payment.ID, "error", err)
		writeAPIError(w, http.StatusBadGateway, "verification_failed", "gateway verification failed; retry shortly")
		return
	}
	_, _ = a.store.AppendAuditLog(r.Context(), store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: auth.merchant.ID, Valid: true},
		Action:       "api.payments.verify",
		ResourceType: sql.NullString{String: "payment", Valid: true},
		ResourceID:   sql.NullString{String: payment.ID.String(), Valid: true},
	})
	a.writePaymentJSON(w, updated)
}

func (a *App) writePaymentJSON(w http.ResponseWriter, payment store.PaymentView) {
	response := map[string]any{
		"payment_id":   payment.ID.String(),
		"reference":    payment.MerchantReference,
		"status":       payment.Status,
		"checkout_url": a.payments.HostedCheckoutURL(payment),
		"amount": map[string]any{
			"value":    payment.AmountKobo,
			"currency": payment.Currency,
		},
		"created_at": payment.CreatedAt.UTC().Format(time.RFC3339),
	}
	if payment.Status == domain.StatusSucceeded {
		response["receipt_url"] = a.cfg.BaseURL + "/receipts/" + payment.ReceiptToken
	}
	writeJSON(w, http.StatusOK, response)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// merchantCreateAPIKey mints a Partner API credential and shows it exactly once.
func (a *App) merchantCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "api key name is required", http.StatusBadRequest)
		return
	}
	plaintext, hash, prefix, err := a.newMerchantAPIKey()
	if err != nil {
		a.logger.Error("generate api key failed", "error", err)
		http.Error(w, "key generation failed", http.StatusInternalServerError)
		return
	}
	view, err := a.store.CreateMerchantAPIKey(r.Context(), merchantID, name, hash, prefix)
	if err != nil {
		a.logger.Error("create api key failed", "error", err)
		http.Error(w, "key creation failed", http.StatusInternalServerError)
		return
	}
	_, _ = a.store.AppendAuditLog(r.Context(), store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: merchantID, Valid: true},
		Action:       "merchant.api_keys.create",
		ResourceType: sql.NullString{String: "merchant_api_key", Valid: true},
		ResourceID:   sql.NullString{String: view.ID.String(), Valid: true},
	})
	a.renderMerchant(w, "merchant_api_key_created.html", r, "API key created", map[string]any{
		"KeyName": name, "KeyValue": plaintext,
	})
}

// merchantRevokeAPIKey disables a credential from the merchant dashboard.
func (a *App) merchantRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	keyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid api key id", http.StatusBadRequest)
		return
	}
	if err := a.store.RevokeMerchantAPIKey(r.Context(), keyID, merchantID); err != nil {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	_, _ = a.store.AppendAuditLog(r.Context(), store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: merchantID, Valid: true},
		Action:       "merchant.api_keys.revoke",
		ResourceType: sql.NullString{String: "merchant_api_key", Valid: true},
		ResourceID:   sql.NullString{String: keyID.String(), Valid: true},
	})
	http.Redirect(w, r, "/merchant/settings", http.StatusSeeOther)
}
