package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/providers/telegram"
	"whatsapp-payment-demo/internal/providers/vtpass"
	"whatsapp-payment-demo/internal/providers/whatsapp"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

func (a *App) verifyWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("hub.mode") != "subscribe" ||
		r.URL.Query().Get("hub.verify_token") != a.cfg.WhatsAppVerifyToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(r.URL.Query().Get("hub.challenge")))
}

func (a *App) receiveWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	eventKey := digest(body)
	validationErr := a.whatsapp.ValidateSignature(body, r.Header.Get("X-Hub-Signature-256"))
	deliveryID, _, storeErr := a.store.RecordWebhook(r.Context(), "whatsapp", eventKey, validationErr == nil, json.RawMessage(`{}`))
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	messages, err := whatsapp.ParseInbound(body)
	if err != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	for _, message := range messages {
		if _, err := a.store.EnqueueInboundMessage(r.Context(), store.InboundMessage{
			ID: message.ID, Channel: service.ChannelWhatsApp, Sender: message.From, Recipient: message.From, Text: message.Text, Interactive: message.Interactive,
			MediaType: message.MediaType, MediaID: message.MediaID, MediaMime: message.MediaMime, Caption: message.Caption,
		}); err != nil {
			_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
			http.Error(w, "storage error", http.StatusServiceUnavailable)
			return
		}
	}
	_ = a.store.CompleteWebhook(r.Context(), deliveryID, "accepted", "")
	w.WriteHeader(http.StatusOK)
}

func (a *App) receiveTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.TelegramEnabled || a.telegram == nil {
		http.Error(w, "telegram disabled", http.StatusNotFound)
		return
	}
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	validationErr := a.telegram.ValidateSecret(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	updates, parseErr := telegram.ParseInbound(body)
	eventKey := digest(body)
	if len(updates) > 0 {
		eventKey = "update:" + strconv.FormatInt(updates[0].UpdateID, 10)
	}
	deliveryID, fresh, storeErr := a.store.RecordWebhook(r.Context(), service.ChannelTelegram, eventKey, validationErr == nil && parseErr == nil, json.RawMessage(`{}`))
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid secret")
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	if parseErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", parseErr.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	for _, update := range updates {
		if update.CallbackQueryID != "" {
			_ = a.telegram.AnswerCallback(r.Context(), update.CallbackQueryID)
		}
		if _, err := a.store.EnqueueInboundMessage(r.Context(), store.InboundMessage{
			ID:          "telegram:" + strconv.FormatInt(update.UpdateID, 10),
			Channel:     service.ChannelTelegram,
			Sender:      update.UserID,
			Recipient:   update.ChatID,
			Text:        update.Text,
			Interactive: update.Interactive,
			Username:    update.Username,
			MediaType:   update.MediaType,
			MediaID:     update.MediaID,
			MediaMime:   update.MediaMime,
			Caption:     update.Caption,
		}); err != nil {
			_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
			http.Error(w, "storage error", http.StatusServiceUnavailable)
			return
		}
	}
	if fresh {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "accepted", "")
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) receiveSMSWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.SMSEnabled {
		http.Error(w, "sms disabled", http.StatusNotFound)
		return
	}
	secret := r.Header.Get("X-SMS-Webhook-Secret")
	if secret == "" {
		secret = r.Header.Get("X-Xego-SMS-Secret")
	}
	if a.cfg.SMSWebhookSecret == "" || secret != a.cfg.SMSWebhookSecret {
		http.Error(w, "invalid sms secret", http.StatusUnauthorized)
		return
	}
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	messageID, sender, text := parseSMSWebhookPayload(r, body)
	if messageID == "" {
		messageID = "sms:" + digest(body)
	}
	if sender == "" || text == "" {
		http.Error(w, "missing sender or text", http.StatusBadRequest)
		return
	}
	reply, err := a.data.HandleSMS(r.Context(), messageID, sender, text)
	if err != nil {
		a.logger.WarnContext(r.Context(), "process SMS webhook", "message_id", messageID, "error", err)
		http.Error(w, "sms processing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

// receiveInterswitchWebhook handles the Interswitch outbound webhook. The
// body is a signed JSON event (TRANSACTION.CREATED/UPDATED/COMPLETED)
// authenticated via the X-Interswitch-Signature header using the dashboard
// webhook secret. The event itself is not authoritative: it is recorded for
// audit/dedup, and only a terminal COMPLETED event triggers a server-side
// requery before value is delivered.
func (a *App) receiveInterswitchWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	event, validationErr := a.interswitch.ValidateWebhook(body, r.Header.Get("X-Interswitch-Signature"))
	eventKey := digest(body)
	if event.Reference != "" {
		eventKey = event.Event + ":" + event.Reference
	}
	payload := json.RawMessage(`{}`)
	if validationErr == nil {
		payload, _ = json.Marshal(store.GatewayEvent{Event: event.Event, Reference: event.Reference})
	}
	deliveryID, fresh, err := a.store.RecordWebhook(r.Context(), "interswitch", eventKey, validationErr == nil, payload)
	if err != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = a.store.CompleteWebhook(context.Background(), deliveryID, "delivered", "")
	if !fresh || event.Reference == "" {
		return
	}
	// Only a terminal notification should trigger a confirmation requery.
	// CREATED/UPDATED are recorded (dedup + audit) but carry no final status.
	if !a.payments.IsGatewaySuccessEvent(service.ProviderInterswitch, event.Event) {
		return
	}
	// The webhook itself is not trusted; requery is authoritative.
	payment, _, err := a.payments.VerifyAndApply(r.Context(), event.Reference, "interswitch.webhook")
	if err != nil {
		a.logger.WarnContext(r.Context(), "interswitch webhook verify failed", "reference", event.Reference, "error", err)
		return
	}
	a.logger.InfoContext(r.Context(), "interswitch webhook verified", "reference", event.Reference, "status", payment.Status)
}

// vtpassWebhookSecretValid reports whether the VTPass callback is authorized.
// C28: the shared secret is carried in the X-VTPass-Webhook-Secret header and
// never in the URL. A query-parameter secret is deliberately ignored, so a
// poisoned webhook URL cannot authenticate and URLs are never logged with the
// secret in them. An empty configured secret disables the check (backlog
// default; VTPASS_WEBHOOK_SECRET should be set in production).
func vtpassWebhookSecretValid(configured, receivedHeader string) bool {
	return configured == "" || receivedHeader == configured
}

func (a *App) receiveVTPassWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	secretValid := vtpassWebhookSecretValid(a.cfg.VTPassWebhookSecret, r.Header.Get("X-VTPass-Webhook-Secret"))
	event, parseErr := vtpass.ParseWebhook(body)
	eventKey := digest(body)
	if event.Reference != "" {
		eventKey = "vtpass:" + event.Reference
	}
	payload := json.RawMessage(`{}`)
	if parseErr == nil {
		payload, _ = json.Marshal(map[string]string{"reference": event.Reference, "status": event.Status, "message": event.Message})
	}
	deliveryID, _, storeErr := a.store.RecordWebhook(r.Context(), "vtpass", eventKey, secretValid && parseErr == nil, payload)
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if !secretValid {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid secret")
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	if parseErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", parseErr.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": "success"})
}

func parseSMSWebhookPayload(r *http.Request, body []byte) (string, string, string) {
	var payload struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
		From    string `json:"from"`
		Sender  string `json:"sender"`
		Body    string `json:"body"`
		Text    string `json:"text"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(body, &payload)
	} else if values, err := url.ParseQuery(string(body)); err == nil {
		payload.ID = values.Get("id")
		payload.Message = values.Get("message_id")
		payload.From = values.Get("from")
		payload.Sender = values.Get("sender")
		payload.Body = values.Get("body")
		payload.Text = values.Get("text")
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		id = strings.TrimSpace(payload.Message)
	}
	sender := strings.TrimSpace(payload.From)
	if sender == "" {
		sender = strings.TrimSpace(payload.Sender)
	}
	text := strings.TrimSpace(payload.Body)
	if text == "" {
		text = strings.TrimSpace(payload.Text)
	}
	return id, sender, text
}
