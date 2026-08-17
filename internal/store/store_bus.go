package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/redact"
)

// BusinessEvent is one transactional domain event awaiting publication.
type BusinessEvent struct {
	ID       int64
	Topic    string
	Key      string
	Payload  json.RawMessage
	Attempts int
}

// ClaimMerchantNotification atomically claims the payment's merchant
// notification slot. It returns false when the slot was already claimed, which
// makes the notification consumer idempotent under at-least-once delivery.
func (s *Store) ClaimMerchantNotification(ctx context.Context, paymentID uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE payments SET merchant_notified_at=now()
		WHERE id=$1 AND merchant_notified_at IS NULL`, paymentID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// InsertBusinessEvent durably queues a domain event for the outbox->bus
// publisher. Callers that already hold a transaction should use
// insertBusinessEventTx so the event is committed with its state change.
func (s *Store) InsertBusinessEvent(ctx context.Context, topic, key string, payload []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.insertBusinessEventTx(ctx, tx, topic, key, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) insertBusinessEventTx(ctx context.Context, tx pgx.Tx, topic, key string, payload []byte) error {
	dedupKey := topic + ":" + key
	if _, err := tx.Exec(ctx, `
		INSERT INTO business_event_outbox(topic,event_key,dedup_key,payload)
		VALUES($1,$2,$3,$4::jsonb)
		ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`, topic, key, dedupKey, string(payload)); err != nil {
		return fmt.Errorf("insert business event: %w", err)
	}
	return nil
}

// insertPaymentEvent enqueues the terminal payment fact (succeeded/failed)
// inside the transition transaction so it commits atomically with the state
// change. Transitions are monotonic, so each payment emits each event at most
// once.
func (s *Store) insertPaymentEvent(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, from, to domain.PaymentStatus, source string, userID, merchantID uuid.UUID, provider, reference string, amountKobo int64, currency string) error {
	switch to {
	case domain.StatusSucceeded:
		payload, err := json.Marshal(domain.PaymentSucceeded{
			PaymentID:  paymentID.String(),
			UserID:     userID.String(),
			MerchantID: merchantID.String(),
			Reference:  reference,
			Provider:   provider,
			Currency:   currency,
			AmountKobo: amountKobo,
			PaidAt:     time.Now(),
		})
		if err != nil {
			return err
		}
		return s.insertBusinessEventTx(ctx, tx, domain.TopicPaymentSucceeded, "payment:"+paymentID.String(), payload)
	case domain.StatusFailed:
		payload, err := json.Marshal(domain.PaymentFailed{
			PaymentID:  paymentID.String(),
			UserID:     userID.String(),
			MerchantID: merchantID.String(),
			Reference:  reference,
			Provider:   provider,
			Currency:   currency,
			AmountKobo: amountKobo,
			Source:     source,
		})
		if err != nil {
			return err
		}
		return s.insertBusinessEventTx(ctx, tx, domain.TopicPaymentFailed, "payment:"+paymentID.String(), payload)
	}
	return nil
}

// ClaimBusinessEvents atomically leases pending events to one publisher.
func (s *Store) ClaimBusinessEvents(ctx context.Context, limit int) ([]BusinessEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id,topic,event_key,payload,attempts
		FROM business_event_outbox
		WHERE status IN ('pending','publishing') AND available_at <= now()
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var events []BusinessEvent
	for rows.Next() {
		var event BusinessEvent
		if err := rows.Scan(&event.ID, &event.Topic, &event.Key, &event.Payload, &event.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, event := range events {
		if _, err := tx.Exec(ctx, `
			UPDATE business_event_outbox SET status='publishing',available_at=now()+interval '5 minutes' WHERE id=$1`, event.ID); err != nil {
			return nil, err
		}
	}
	return events, tx.Commit(ctx)
}

// CompleteBusinessEvent marks an event published.
func (s *Store) CompleteBusinessEvent(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE business_event_outbox SET status='published',published_at=now(),attempts=attempts+1 WHERE id=$1`, id)
	return err
}

// RetryBusinessEvent schedules a bounded exponential retry or permanently
// fails the event, mirroring RetryOutbox.
func (s *Store) RetryBusinessEvent(ctx context.Context, id int64, attempts int, message string) error {
	nextAttempts := attempts + 1
	status := "pending"
	if nextAttempts >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(nextAttempts, 6)) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE business_event_outbox
		SET status=$2,attempts=$3,last_error=$4,available_at=$5
		WHERE id=$1`, id, status, nextAttempts, redact.Error(message, redact.DefaultMaxLen), time.Now().Add(delay))
	return err
}
