package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Payment hook names applied after a payment succeeds. They are tracked in
// payment_hooks so a failure can be retried by a worker instead of silently
// leaving a succeeded payment with its purpose un-applied.
const (
	PaymentHookInvoice     = "invoice"
	PaymentHookThrift      = "thrift"
	PaymentHookService     = "service"
	PaymentHookEvent       = "event"
	PaymentHookSplits      = "splits"
	PaymentHookReceiptScan = "receipt_scan"
)

// PaymentHookOrder is the order in which post-success hooks run.
var PaymentHookOrder = []string{
	PaymentHookInvoice,
	PaymentHookThrift,
	PaymentHookService,
	PaymentHookEvent,
	PaymentHookSplits,
	PaymentHookReceiptScan,
}

// PaymentHook is a pending, done, or failed post-success hook for a payment.
type PaymentHook struct {
	PaymentID   uuid.UUID
	Hook        string
	Status      string
	Attempts    int
	LastError   string
	NextRetryAt time.Time
}

// EnsurePaymentHooks records the hooks that must run for a payment, ignoring
// ones already tracked (idempotent across replays of the success path).
func (s *Store) EnsurePaymentHooks(ctx context.Context, paymentID uuid.UUID, hooks []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, h := range hooks {
		// The initial retry is pushed a minute out so the worker cannot pick
		// up a hook while the inline success chain is still running it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_hooks(payment_id, hook, next_retry_at)
			VALUES($1, $2, now() + interval '1 minute')
			ON CONFLICT (payment_id, hook) DO NOTHING`, paymentID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PendingPaymentHooks returns hooks due for retry, oldest first.
func (s *Store) PendingPaymentHooks(ctx context.Context, limit int) ([]PaymentHook, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT payment_id, hook, status, attempts, COALESCE(last_error, ''), next_retry_at
		FROM payment_hooks
		WHERE status = 'pending' AND next_retry_at <= now()
		ORDER BY next_retry_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hooks []PaymentHook
	for rows.Next() {
		var h PaymentHook
		if err := rows.Scan(&h.PaymentID, &h.Hook, &h.Status, &h.Attempts, &h.LastError, &h.NextRetryAt); err != nil {
			return nil, err
		}
		hooks = append(hooks, h)
	}
	return hooks, rows.Err()
}

// MarkPaymentHookDone records a hook as successfully applied.
func (s *Store) MarkPaymentHookDone(ctx context.Context, paymentID uuid.UUID, hook string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE payment_hooks
		SET status = 'done', last_error = NULL, updated_at = now()
		WHERE payment_id = $1 AND hook = $2`, paymentID, hook)
	return err
}

// MarkPaymentHookFailed records a failed attempt and schedules the next retry.
// When final is true the hook moves to status 'failed' for manual review.
func (s *Store) MarkPaymentHookFailed(ctx context.Context, paymentID uuid.UUID, hook, lastErr string, nextRetryAt time.Time, final bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE payment_hooks
		SET attempts = attempts + 1,
		    last_error = $3,
		    next_retry_at = $4,
		    status = CASE WHEN $5 THEN 'failed' ELSE 'pending' END,
		    updated_at = now()
		WHERE payment_id = $1 AND hook = $2`, paymentID, hook, lastErr, nextRetryAt, final)
	return err
}
