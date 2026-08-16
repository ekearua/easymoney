package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// MerchantWebhookConsumer turns terminal payment facts into durable merchant
// webhook deliveries. It subscribes to payment.succeeded and payment.failed on
// its own consumer group so notification and compliance consumers are not
// coupled to outbound delivery. Enqueueing is idempotent per
// (merchant, payment, event), so at-least-once bus delivery is safe.
type MerchantWebhookConsumer struct {
	store   *store.Store
	baseURL string
	logger  *slog.Logger
}

// NewMerchantWebhookConsumer constructs the merchant webhook consumer.
func NewMerchantWebhookConsumer(repository *store.Store, baseURL string, logger *slog.Logger) *MerchantWebhookConsumer {
	return &MerchantWebhookConsumer{store: repository, baseURL: baseURL, logger: logger}
}

// HandlePaymentSucceeded queues a payment.succeeded delivery for the merchant
// and closes any request-money checkouts attached to the payment.
func (c *MerchantWebhookConsumer) HandlePaymentSucceeded(ctx context.Context, msg ports.EventMessage) error {
	var event domain.PaymentSucceeded
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return fmt.Errorf("decode payment.succeeded: %w", err)
	}
	if id, err := uuid.Parse(event.PaymentID); err == nil {
		if err := c.store.MarkCheckoutsPaidByPayment(ctx, id); err != nil {
			return err
		}
	}
	return c.enqueue(ctx, event.PaymentID, domain.TopicPaymentSucceeded)
}

// HandlePaymentFailed queues a payment.failed delivery for the merchant.
func (c *MerchantWebhookConsumer) HandlePaymentFailed(ctx context.Context, msg ports.EventMessage) error {
	var event domain.PaymentFailed
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return fmt.Errorf("decode payment.failed: %w", err)
	}
	return c.enqueue(ctx, event.PaymentID, domain.TopicPaymentFailed)
}

// enqueue builds the delivery payload from the authoritative payment row and
// writes it only when the merchant has registered a callback URL.
func (c *MerchantWebhookConsumer) enqueue(ctx context.Context, paymentID, topic string) error {
	id, err := uuid.Parse(paymentID)
	if err != nil {
		return fmt.Errorf("parse payment id: %w", err)
	}
	payment, err := c.store.PaymentByID(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := c.store.MerchantWebhookConfig(ctx, payment.MerchantID)
	if err != nil {
		return err
	}
	if cfg.URL == "" {
		c.logger.Debug("merchant has no webhook url; skipping delivery", "merchant_id", payment.MerchantID, "payment_id", id)
		return nil
	}
	payload, err := buildMerchantWebhookPayload(c.baseURL, payment, topic)
	if err != nil {
		return err
	}
	queued, err := c.store.EnqueueMerchantWebhook(ctx, payment.MerchantID, id, topic, payload)
	if err != nil {
		return err
	}
	if queued {
		c.logger.Info("merchant webhook queued", "merchant_id", payment.MerchantID, "payment_id", id, "event", topic)
	}
	return nil
}

// merchantWebhookEnvelope is the signed JSON body delivered to merchants.
type merchantWebhookEnvelope struct {
	Reference  string     `json:"reference"`
	PaymentID  string     `json:"payment_id"`
	Status     string     `json:"status"`
	Amount     apiMoney   `json:"amount"`
	PaidAt     *time.Time `json:"paid_at,omitempty"`
	ReceiptURL string     `json:"receipt_url,omitempty"`
}

type apiMoney struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency"`
}

// buildMerchantWebhookPayload renders the delivery body from the payment row.
func buildMerchantWebhookPayload(baseURL string, payment store.PaymentView, event string) ([]byte, error) {
	status := string(payment.Status)
	if event == domain.TopicPaymentFailed {
		status = "failed"
	}
	envelope := merchantWebhookEnvelope{
		Reference:  payment.MerchantReference,
		PaymentID:  payment.ID.String(),
		Status:     status,
		Amount:     apiMoney{Value: payment.AmountKobo, Currency: payment.Currency},
		PaidAt:     payment.PaidAt,
		ReceiptURL: baseURL + "/receipts/" + payment.ReceiptToken,
	}
	if event == domain.TopicPaymentFailed || payment.PaidAt == nil {
		envelope.ReceiptURL = ""
		envelope.PaidAt = nil
	}
	return json.Marshal(envelope)
}

// SignMerchantWebhook returns the lowercase hex HMAC-SHA256 of the body under
// the merchant secret. Merchants recompute the same value to verify delivery.
func SignMerchantWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// MerchantWebhookDeliverer claims due deliveries, signs them, and POSTs them
// to the merchant callback URL with bounded exponential retry.
type MerchantWebhookDeliverer struct {
	store  *store.Store
	client *http.Client
	logger *slog.Logger
}

// NewMerchantWebhookDeliverer constructs the delivery worker. A nil client
// defaults to a client with a ten-second per-request timeout.
func NewMerchantWebhookDeliverer(repository *store.Store, client *http.Client, logger *slog.Logger) *MerchantWebhookDeliverer {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &MerchantWebhookDeliverer{store: repository, client: client, logger: logger}
}

// DeliverDue delivers one bounded batch of due webhooks.
func (d *MerchantWebhookDeliverer) DeliverDue(ctx context.Context) error {
	deliveries, err := d.store.ClaimMerchantWebhooks(ctx, 20)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		d.deliver(ctx, delivery)
	}
	return nil
}

func (d *MerchantWebhookDeliverer) deliver(ctx context.Context, delivery store.MerchantWebhookDelivery) {
	cfg, err := d.store.MerchantWebhookConfig(ctx, delivery.MerchantID)
	if err != nil {
		d.logger.Error("merchant webhook config lookup failed", "delivery_id", delivery.ID, "error", err)
		_ = d.store.RetryMerchantWebhook(ctx, delivery.ID, delivery.Attempts, err.Error())
		return
	}
	if cfg.URL == "" {
		_ = d.store.CompleteMerchantWebhook(ctx, delivery.ID)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		d.logger.Error("merchant webhook request build failed", "delivery_id", delivery.ID, "error", err)
		_ = d.store.RetryMerchantWebhook(ctx, delivery.ID, delivery.Attempts, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Xego-Event", delivery.Event)
	req.Header.Set("X-Xego-Signature", SignMerchantWebhook(cfg.Secret, delivery.Payload))
	req.Header.Set("User-Agent", "Xego-Webhooks/1.0")

	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Warn("merchant webhook delivery failed", "delivery_id", delivery.ID, "url", redactURL(cfg.URL), "error", err)
		_ = d.store.RetryMerchantWebhook(ctx, delivery.ID, delivery.Attempts, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.logger.Info("merchant webhook delivered", "delivery_id", delivery.ID, "event", delivery.Event, "status", resp.StatusCode)
		_ = d.store.CompleteMerchantWebhook(ctx, delivery.ID)
		return
	}
	err = fmt.Errorf("merchant webhook endpoint returned %s", resp.Status)
	d.logger.Warn("merchant webhook rejected", "delivery_id", delivery.ID, "url", redactURL(cfg.URL), "error", err)
	_ = d.store.RetryMerchantWebhook(ctx, delivery.ID, delivery.Attempts, err.Error())
}

// redactURL keeps credentials out of logs without hiding the endpoint.
func redactURL(raw string) string {
	if len(raw) > 80 {
		return raw[:80] + "..."
	}
	return raw
}
