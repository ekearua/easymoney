package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// newMerchantSecret mints the symmetric HMAC secret used to sign outbound
// webhooks. It is stored sealed and shown to the merchant exactly once, at the
// same ceremony as a Partner API key.
func (a *App) newMerchantSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// merchantUpdateWebhook saves the merchant's callback URL and optionally rotates
// the webhook signing secret. When a secret is minted (first setup or explicit
// rotation) it is shown exactly once on a dedicated page so it never lands in
// an access-logged redirect query string.
func (a *App) merchantUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	rawURL := strings.TrimSpace(r.FormValue("webhook_url"))
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			http.Error(w, "webhook URL must be a valid http(s) URL", http.StatusBadRequest)
			return
		}
	} else {
		rawURL = ""
	}
	rotate := r.FormValue("rotate_secret") == "on"

	cfg, err := a.store.MerchantWebhookConfig(r.Context(), merchantID)
	if err != nil {
		a.logger.Error("webhook config lookup failed", "error", err)
		http.Error(w, "webhook config unavailable", http.StatusInternalServerError)
		return
	}
	needsNewSecret := rotate || cfg.Secret == ""
	secret := ""
	if needsNewSecret {
		secret, err = a.newMerchantSecret()
		if err != nil {
			a.logger.Error("generate webhook secret failed", "error", err)
			http.Error(w, "secret generation failed", http.StatusInternalServerError)
			return
		}
	}
	if err := a.store.SetMerchantWebhookURL(r.Context(), merchantID, rawURL); err != nil {
		a.logger.Error("save webhook config failed", "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if needsNewSecret {
		if err := a.store.SetMerchantWebhookSecret(r.Context(), merchantID, secret); err != nil {
			a.logger.Error("rotate webhook secret failed", "error", err)
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
	}
	_, _ = a.store.AppendAuditLog(r.Context(), store.AuditLog{
		ActorType:    "merchant",
		ActorID:      uuid.NullUUID{UUID: merchantID, Valid: true},
		Action:       "merchant.webhook.update",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		Details:      map[string]any{"url_set": rawURL != "", "secret_rotated": needsNewSecret},
	})
	if needsNewSecret {
		a.renderMerchant(w, "merchant_secret_created.html", r, "Webhook secret", map[string]any{
			"Secret":     secret,
			"WebhookURL": rawURL,
		})
		return
	}
	http.Redirect(w, r, "/merchant/settings?saved=1", http.StatusSeeOther)
}
