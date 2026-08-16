package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/redact"
)

// MerchantWebhookConfig is the outbound notification target a merchant
// registers on the dashboard. The secret is the symmetric HMAC key merchants
// use to verify delivered payloads; it is sealed at rest.
type MerchantWebhookConfig struct {
	URL    string
	Secret string
}

// MerchantWebhookDelivery is one claimed delivery row.
type MerchantWebhookDelivery struct {
	ID         int64
	MerchantID uuid.UUID
	SourceID   uuid.UUID
	Event      string
	Payload    []byte
	Attempts   int
}

// SetMerchantWebhookURL updates the merchant's callback URL. The signing secret
// is left untouched so saving the URL never invalidates deliveries in flight.
func (s *Store) SetMerchantWebhookURL(ctx context.Context, merchantID uuid.UUID, url string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchants SET webhook_url=$2,updated_at=now() WHERE id=$1`, merchantID, url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("merchant not found")
	}
	return nil
}

// SetMerchantWebhookSecret rotates the merchant's signing secret. The plaintext
// is sealed at rest and shown to the merchant exactly once at rotation time.
func (s *Store) SetMerchantWebhookSecret(ctx context.Context, merchantID uuid.UUID, secret string) error {
	sealed, err := s.sealValue([]byte(secret))
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchants SET webhook_secret=$2,updated_at=now() WHERE id=$1`, merchantID, sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("merchant not found")
	}
	return nil
}

// MerchantWebhookConfig returns the merchant's callback URL and the unsealed
// signing secret.
func (s *Store) MerchantWebhookConfig(ctx context.Context, merchantID uuid.UUID) (MerchantWebhookConfig, error) {
	var cfg MerchantWebhookConfig
	var secret string
	err := s.pool.QueryRow(ctx, `
		SELECT webhook_url,webhook_secret FROM merchants WHERE id=$1`, merchantID).Scan(&cfg.URL, &secret)
	if err != nil {
		return cfg, err
	}
	open, err := s.openValue(secret)
	if err != nil {
		return cfg, err
	}
	cfg.Secret = string(open)
	return cfg, nil
}

// EnqueueMerchantWebhook queues a signed delivery for the merchant. It is
// idempotent per (merchant, source, event): redelivered bus events are no-ops.
// Payment events carry the payment id as source; settlement and payout events
// carry their own id. The payload is sealed at rest like chat payloads.
func (s *Store) EnqueueMerchantWebhook(ctx context.Context, merchantID, sourceID uuid.UUID, event string, payload []byte) (bool, error) {
	sealed, err := s.sealValue(payload)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_webhook_deliveries(merchant_id,source_id,event,payload)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (merchant_id,source_id,event) DO NOTHING`, merchantID, sourceID, event, sealed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimMerchantWebhooks leases due deliveries for the deliverer.
func (s *Store) ClaimMerchantWebhooks(ctx context.Context, limit int) ([]MerchantWebhookDelivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id,merchant_id,source_id,event,payload,attempts
		FROM merchant_webhook_deliveries
		WHERE status IN ('pending','sending') AND available_at <= now()
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var deliveries []MerchantWebhookDelivery
	for rows.Next() {
		var delivery MerchantWebhookDelivery
		var payload string
		if err := rows.Scan(&delivery.ID, &delivery.MerchantID, &delivery.SourceID, &delivery.Event, &payload, &delivery.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		raw, err := s.openValue(payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		delivery.Payload = raw
		deliveries = append(deliveries, delivery)
	}
	rows.Close()
	for _, delivery := range deliveries {
		if _, err := tx.Exec(ctx, `
			UPDATE merchant_webhook_deliveries SET status='sending',available_at=now()+interval '5 minutes' WHERE id=$1`, delivery.ID); err != nil {
			return nil, err
		}
	}
	return deliveries, tx.Commit(ctx)
}

// CompleteMerchantWebhook marks a delivery delivered.
func (s *Store) CompleteMerchantWebhook(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_webhook_deliveries SET status='sent',attempts=attempts+1,last_error='',available_at=now() WHERE id=$1`, id)
	return err
}

// RetryMerchantWebhook schedules a bounded exponential retry or permanently
// dead-letters the delivery as failed after the cap, mirroring RetryOutbox.
func (s *Store) RetryMerchantWebhook(ctx context.Context, id int64, attempts int, message string) error {
	nextAttempts := attempts + 1
	status := "pending"
	if nextAttempts >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(nextAttempts, 6)) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_webhook_deliveries
		SET status=$2,attempts=$3,last_error=$4,available_at=$5
		WHERE id=$1`, id, status, nextAttempts, redact.Error(message, redact.DefaultMaxLen), time.Now().Add(delay))
	return err
}
