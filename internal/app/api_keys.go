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
	"net/url"
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

type apiCreateCheckoutRequest struct {
	Reference   string         `json:"reference"`
	Note        string         `json:"note"`
	Amount      apiAmount      `json:"amount"`
	RedirectURL string         `json:"redirect_url"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	Metadata    map[string]any `json:"metadata"`
}

// apiCreateCheckout mints a general request-money link for an individual. No
// merchant or customer is bound at creation: the payer is resolved when the
// hosted link is opened. Replaying the same payee reference returns the
// existing checkout.
func (a *App) apiCreateCheckout(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	var req apiCreateCheckoutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Amount.Currency != "" && req.Amount.Currency != "NGN" {
		writeAPIError(w, http.StatusUnprocessableEntity, "currency_invalid", "only NGN is supported")
		return
	}
	if req.Amount.Value < apiMinAmountKobo || req.Amount.Value > apiMaxAmountKobo {
		writeAPIError(w, http.StatusUnprocessableEntity, "amount_out_of_range", "amount must be between NGN 1.00 and NGN 10,000,000.00")
		return
	}
	reference := strings.ToUpper(strings.TrimSpace(req.Reference))
	if len(reference) > apiMaxReferenceLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "reference_invalid", "reference must be at most 64 characters")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 500 {
		writeAPIError(w, http.StatusUnprocessableEntity, "note_invalid", "note must be at most 500 characters")
		return
	}
	redirectURL := strings.TrimSpace(req.RedirectURL)
	if redirectURL != "" && !strings.HasPrefix(redirectURL, "https://") && !strings.HasPrefix(redirectURL, "http://") {
		writeAPIError(w, http.StatusUnprocessableEntity, "redirect_url_invalid", "redirect_url must be an absolute http(s) URL")
		return
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now()) {
		writeAPIError(w, http.StatusUnprocessableEntity, "expires_at_invalid", "expires_at must be in the future")
		return
	}
	ctx := r.Context()
	checkout, err := a.payments.CreateCheckout(ctx, auth.merchant, store.CheckoutSpec{
		PayeeMerchantID: auth.merchant.ID,
		Reference:       reference,
		Note:            note,
		AmountKobo:      req.Amount.Value,
		Currency:        "NGN",
		RedirectURL:     redirectURL,
		ExpiresAt:       req.ExpiresAt,
	})
	if err != nil {
		a.logger.Error("api checkout creation failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "checkout creation failed")
		return
	}
	_, _ = a.store.AppendAuditLog(ctx, store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: auth.merchant.ID, Valid: true},
		Action:       "api.checkouts.create",
		ResourceType: sql.NullString{String: "checkout", Valid: true},
		ResourceID:   sql.NullString{String: checkout.ID.String(), Valid: true},
		Details:      map[string]any{"reference": checkout.Reference, "amount_kobo": checkout.AmountKobo},
	})
	a.writeCheckoutJSON(w, checkout)
}

// apiCheckoutStatus returns the current state of a request-money link.
func (a *App) apiCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	checkout, err := a.store.CheckoutByPayeeAndReference(r.Context(), auth.merchant.ID, reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no checkout with that reference")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "checkout lookup failed")
		return
	}
	a.writeCheckoutJSON(w, checkout)
}

func (a *App) writeCheckoutJSON(w http.ResponseWriter, checkout store.CheckoutView) {
	response := map[string]any{
		"checkout_id": checkout.ID.String(),
		"reference":   checkout.Reference,
		"status":      checkout.Status,
		"amount": map[string]any{
			"value":    checkout.AmountKobo,
			"currency": checkout.Currency,
		},
		"checkout_url": a.cfg.BaseURL + "/link/" + checkout.Token,
		"created_at":   checkout.CreatedAt.UTC().Format(time.RFC3339),
	}
	if checkout.Note != "" {
		response["note"] = checkout.Note
	}
	if checkout.PaymentID.Valid {
		response["payment_status"] = checkout.PaymentStatus
		response["payment_id"] = checkout.PaymentID.UUID.String()
	}
	if checkout.ExpiresAt != nil {
		response["expires_at"] = checkout.ExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

type apiInvoiceItem struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
	UnitAmount  int64  `json:"unit_amount"`
}

type apiCreateInvoiceRequest struct {
	Reference   string           `json:"reference"`
	Customer    apiCustomer      `json:"customer"`
	Items       []apiInvoiceItem `json:"items"`
	DeliveryFee int64            `json:"delivery_fee"`
	Currency    string           `json:"currency"`
	DueAt       *time.Time       `json:"due_at"`
}

// apiCreateInvoice creates a merchant invoice from line items through the
// Partner API. It is idempotent on the merchant-supplied reference: replaying
// the same reference returns the existing invoice.
func (a *App) apiCreateInvoice(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	var req apiCreateInvoiceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Currency != "" && req.Currency != "NGN" {
		writeAPIError(w, http.StatusUnprocessableEntity, "currency_invalid", "only NGN is supported")
		return
	}
	if len(req.Items) == 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "items_invalid", "at least one line item is required")
		return
	}
	items := make([]store.InvoiceItem, 0, len(req.Items))
	subtotal := int64(0)
	for _, item := range req.Items {
		description := strings.TrimSpace(item.Description)
		if description == "" || item.Quantity <= 0 || item.UnitAmount <= 0 {
			writeAPIError(w, http.StatusUnprocessableEntity, "items_invalid", "each item needs a description, a quantity, and a unit amount")
			return
		}
		items = append(items, store.InvoiceItem{Description: description, Quantity: item.Quantity, UnitPriceKobo: item.UnitAmount})
		subtotal += int64(item.Quantity) * item.UnitAmount
	}
	if req.DeliveryFee < 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "delivery_fee_invalid", "delivery_fee must be zero or positive")
		return
	}
	total := subtotal + req.DeliveryFee
	if total < apiMinAmountKobo || total > apiMaxAmountKobo {
		writeAPIError(w, http.StatusUnprocessableEntity, "amount_out_of_range", "invoice total must be between NGN 1.00 and NGN 10,000,000.00")
		return
	}
	reference := strings.ToUpper(strings.TrimSpace(req.Reference))
	if len(reference) > apiMaxReferenceLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "reference_invalid", "reference must be at most 64 characters")
		return
	}
	phone := domain.CanonicalE164Phone(req.Customer.Phone)
	if len(strings.TrimPrefix(phone, "+")) < 10 {
		writeAPIError(w, http.StatusUnprocessableEntity, "customer_invalid", "customer.phone must be a valid E.164 number")
		return
	}
	ctx := r.Context()

	if reference != "" {
		if invoice, err := a.store.InvoiceByReference(ctx, reference); err == nil {
			if invoice.MerchantID != auth.merchant.ID {
				writeAPIError(w, http.StatusConflict, "reference_taken", "that reference already belongs to another merchant")
				return
			}
			a.writeInvoiceJSON(w, invoice)
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("api invoice lookup failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "internal", "invoice lookup failed")
			return
		}
	}

	ownerID, err := a.store.MerchantOwnerID(ctx, auth.merchant.ID)
	if err != nil {
		a.logger.Error("api invoice merchant owner lookup failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "invoice creation failed")
		return
	}
	invoice, err := a.store.CreateInvoice(ctx, store.InvoiceSpec{
		MerchantID:             auth.merchant.ID,
		CreatedByUserID:        ownerID,
		CustomerWhatsAppNumber: phone,
		CustomerEmail:          strings.ToLower(strings.TrimSpace(req.Customer.Email)),
		DeliveryFeeKobo:        req.DeliveryFee,
		DueAt:                  req.DueAt,
		Reference:              reference,
		Items:                  items,
	})
	if err != nil {
		// Unique violation from a concurrent replay of the same reference:
		// return the invoice that won the race.
		if reference != "" {
			if winner, lookupErr := a.store.InvoiceByReference(ctx, reference); lookupErr == nil && winner.MerchantID == auth.merchant.ID {
				a.writeInvoiceJSON(w, winner)
				return
			}
		}
		a.logger.Error("api invoice creation failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "invoice creation failed")
		return
	}
	_, _ = a.store.AppendAuditLog(ctx, store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: auth.merchant.ID, Valid: true},
		Action:       "api.invoices.create",
		ResourceType: sql.NullString{String: "invoice", Valid: true},
		ResourceID:   sql.NullString{String: invoice.ID.String(), Valid: true},
		Details:      map[string]any{"reference": invoice.Reference, "total_kobo": invoice.TotalKobo},
	})
	a.writeInvoiceJSON(w, invoice)
}

// apiInvoiceStatus returns the current state of a merchant invoice, including
// how much has been paid toward the total.
func (a *App) apiInvoiceStatus(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	invoice, err := a.store.InvoiceByReference(r.Context(), reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no invoice with that reference")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "invoice lookup failed")
		return
	}
	if invoice.MerchantID != auth.merchant.ID {
		writeAPIError(w, http.StatusNotFound, "not_found", "no invoice with that reference")
		return
	}
	a.writeInvoiceJSON(w, invoice)
}

func (a *App) writeInvoiceJSON(w http.ResponseWriter, invoice store.InvoiceView) {
	items := make([]map[string]any, 0, len(invoice.Items))
	for _, item := range invoice.Items {
		items = append(items, map[string]any{
			"description": item.Description,
			"quantity":    item.Quantity,
			"unit_amount": item.UnitPriceKobo,
		})
	}
	response := map[string]any{
		"invoice_id": invoice.ID.String(),
		"reference":  invoice.Reference,
		"status":     invoice.Status,
		"amount": map[string]any{
			"value":    invoice.TotalKobo,
			"currency": "NGN",
		},
		"amount_paid": map[string]any{
			"value":    invoice.AmountPaidKobo,
			"currency": "NGN",
		},
		"items":      items,
		"created_at": invoice.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.cfg.WhatsAppPhoneNumber != "" {
		response["pay_link"] = "https://wa.me/" + a.cfg.WhatsAppPhoneNumber + "?text=" + url.QueryEscape("PAY "+invoice.Reference)
	}
	writeJSON(w, http.StatusOK, response)
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
