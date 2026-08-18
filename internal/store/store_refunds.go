// S2: refund + dispute store. A refund reverses the ledger entries for a
// succeeded payment and transitions it to the refunded terminal state. If the
// payment is already in an open settlement batch the line is removed and the
// batch totals are recomputed.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// Refund status constants.
const (
	RefundPending   = "pending"
	RefundSucceeded = "succeeded"
	RefundFailed    = "failed"
)

// Dispute status constants.
const (
	DisputeOpen    = "open"
	DisputeWon     = "won"
	DisputeLost    = "lost"
	DisputeExpired = "expired"
)

// Refund is one customer-initiated reversal of a succeeded payment.
type Refund struct {
	ID               uuid.UUID
	PaymentID        uuid.UUID
	MerchantID       uuid.UUID
	AmountKobo       int64
	Reason           string
	Status           string
	ApprovalStatus   string
	ProviderRefundID string
	LastError        string
	AdminID          uuid.UUID
	ApprovedBy       uuid.UUID
	CreatedAt        time.Time
	CompletedAt      *time.Time
	ApprovedAt       *time.Time
}

// Dispute is a provider-initiated chargeback or complaint against a payment.
type Dispute struct {
	ID         uuid.UUID
	PaymentID  uuid.UUID
	MerchantID uuid.UUID
	Reason     string
	Status     string
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// RefundRefundPayment is the entry point for a merchant-initiated full refund.
// It validates preconditions, removes the payment from any open settlement
// batch, transitions the payment to refunded, posts the ledger reversal, and
// emits the payment.refunded event—all inside one transaction.
func (s *Store) RefundPayment(ctx context.Context, paymentID uuid.UUID, reason, postedBy string, adminID *uuid.UUID) (Refund, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Refund{}, err
	}
	defer tx.Rollback(ctx)

	// 1. Lock and validate the payment.
	var merchantID uuid.UUID
	var amountKobo int64
	var currency, providerRef, reference string
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT merchant_id, amount_kobo, currency, provider_reference,
		       COALESCE(merchant_reference,''), status
		FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(
		&merchantID, &amountKobo, &currency, &providerRef, &reference, &status); err != nil {
		return Refund{}, fmt.Errorf("load payment: %w", err)
	}
	if status != "succeeded" {
		return Refund{}, fmt.Errorf("cannot refund payment in status %q (must be succeeded)", status)
	}
	if !domain.CanTransition(domain.PaymentStatus(status), domain.StatusRefunded) {
		return Refund{}, fmt.Errorf("invalid payment transition %s -> refunded", status)
	}
	// Reject if already refunded.
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM refunds WHERE payment_id=$1 AND status IN ('pending','succeeded')`, paymentID).Scan(&existing); err != nil {
		return Refund{}, err
	}
	if existing > 0 {
		return Refund{}, fmt.Errorf("payment %s already has an active refund", paymentID)
	}

	// 2. Check settlement batch status. Lock the batch to prevent concurrent CutSettlement.
	var batchStatus sql.NullString
	var batchID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT sb.status, sb.id
		FROM settlement_lines sl
		JOIN settlement_batches sb ON sb.id=sl.batch_id
		WHERE sl.payment_id=$1 AND sb.status IN ('open','scheduled','processed')
		ORDER BY sb.created_at DESC LIMIT 1`, paymentID).Scan(&batchStatus, &batchID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Refund{}, err
	}
	if batchStatus.Valid && batchStatus.String != "open" {
		return Refund{}, fmt.Errorf("payment %s is in a %s settlement batch; reverse the payout first", paymentID, batchStatus.String)
	}

	// 3. If in an open batch, lock it, remove the line and recompute totals.
	if batchID != nil {
		if _, err := tx.Exec(ctx, `SELECT id FROM settlement_batches WHERE id=$1 FOR UPDATE`, *batchID); err != nil {
			return Refund{}, fmt.Errorf("lock batch: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM settlement_lines WHERE payment_id=$1 AND batch_id=$2`, paymentID, *batchID); err != nil {
			return Refund{}, fmt.Errorf("remove settlement line: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE settlement_batches
			SET total_kobo = (SELECT COALESCE(sum(amount_kobo),0) FROM settlement_lines WHERE batch_id=$1),
			    line_count = (SELECT count(*) FROM settlement_lines WHERE batch_id=$1)
			WHERE id=$1`, *batchID); err != nil {
			return Refund{}, fmt.Errorf("recompute batch: %w", err)
		}
	}

	// 4. Create the refund record.
	var refund Refund
	if err := tx.QueryRow(ctx, `
		INSERT INTO refunds(payment_id,merchant_id,amount_kobo,reason,status,admin_id)
		VALUES($1,$2,$3,$4,$5,$6)
		RETURNING id,payment_id,merchant_id,amount_kobo,reason,status,provider_refund_id,last_error,
		          COALESCE(admin_id, '00000000-0000-0000-0000-000000000000'),created_at,completed_at`,
		paymentID, merchantID, amountKobo, reason, RefundPending, adminID).Scan(
		&refund.ID, &refund.PaymentID, &refund.MerchantID, &refund.AmountKobo, &refund.Reason,
		&refund.Status, &refund.ProviderRefundID, &refund.LastError,
		&refund.AdminID, &refund.CreatedAt, &refund.CompletedAt); err != nil {
		return Refund{}, fmt.Errorf("insert refund: %w", err)
	}

	// 5. Transition payment to refunded.
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET status='refunded', updated_at=now() WHERE id=$1`, paymentID); err != nil {
		return Refund{}, fmt.Errorf("transition payment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,'succeeded','refunded',$2,$3::jsonb)`, paymentID, postedBy, fmt.Sprintf(`{"reason":%q}`, reason)); err != nil {
		return Refund{}, err
	}

	// 6. Post ledger reversal for the original payment.
	reversalJournal := paymentID.String()
	if _, err := s.postLedgerReversalTx(ctx, tx, reversalJournal, "Refund: "+reason, postedBy); err != nil {
		return Refund{}, fmt.Errorf("ledger reversal: %w", err)
	}

	// 7. Emit the payment.refunded event.
	fact, err := json.Marshal(domain.PaymentRefunded{
		PaymentID:  paymentID.String(),
		MerchantID: merchantID.String(),
		RefundID:   refund.ID.String(),
		Reference:  reference,
		Currency:   currency,
		AmountKobo: amountKobo,
		Reason:     reason,
	})
	if err != nil {
		return Refund{}, err
	}
	if err := s.insertBusinessEventTx(ctx, tx, "payment.refunded", "payment:"+paymentID.String(), fact); err != nil {
		return Refund{}, err
	}

	return refund, tx.Commit(ctx)
}

// RequestRefund creates a refund request with approval_status='pending_approval'.
// It validates preconditions and removes the payment from any open settlement
// batch, but does NOT transition the payment or post ledger — those happen
// only after approval via ApproveRefund.
func (s *Store) RequestRefund(ctx context.Context, paymentID uuid.UUID, reason, postedBy string, adminID *uuid.UUID) (Refund, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Refund{}, err
	}
	defer tx.Rollback(ctx)

	// 1. Lock and validate the payment.
	var merchantID uuid.UUID
	var amountKobo int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT merchant_id, amount_kobo, status
		FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(
		&merchantID, &amountKobo, &status); err != nil {
		return Refund{}, fmt.Errorf("load payment: %w", err)
	}
	if status != "succeeded" {
		return Refund{}, fmt.Errorf("cannot refund payment in status %q (must be succeeded)", status)
	}
	if !domain.CanTransition(domain.PaymentStatus(status), domain.StatusRefunded) {
		return Refund{}, fmt.Errorf("invalid payment transition %s -> refunded", status)
	}
	// Reject if already refunded.
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM refunds WHERE payment_id=$1 AND status IN ('pending','succeeded')`, paymentID).Scan(&existing); err != nil {
		return Refund{}, err
	}
	if existing > 0 {
		return Refund{}, fmt.Errorf("payment %s already has an active refund", paymentID)
	}

	// 2. Check settlement batch status.
	var batchStatus sql.NullString
	var batchID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT sb.status, sb.id
		FROM settlement_lines sl
		JOIN settlement_batches sb ON sb.id=sl.batch_id
		WHERE sl.payment_id=$1 AND sb.status IN ('open','scheduled','processed')
		ORDER BY sb.created_at DESC LIMIT 1`, paymentID).Scan(&batchStatus, &batchID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Refund{}, err
	}
	if batchStatus.Valid && batchStatus.String != "open" {
		return Refund{}, fmt.Errorf("payment %s is in a %s settlement batch; reverse the payout first", paymentID, batchStatus.String)
	}

	// 3. If in an open batch, remove the line and recompute totals.
	if batchID != nil {
		if _, err := tx.Exec(ctx, `
			DELETE FROM settlement_lines WHERE payment_id=$1 AND batch_id=$2`, paymentID, *batchID); err != nil {
			return Refund{}, fmt.Errorf("remove settlement line: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE settlement_batches
			SET total_kobo = (SELECT COALESCE(sum(amount_kobo),0) FROM settlement_lines WHERE batch_id=$1),
			    line_count = (SELECT count(*) FROM settlement_lines WHERE batch_id=$1)
			WHERE id=$1`, *batchID); err != nil {
			return Refund{}, fmt.Errorf("recompute batch: %w", err)
		}
	}

	// 4. Create the refund record with approval_status='pending_approval'.
	var refund Refund
	if err := tx.QueryRow(ctx, `
		INSERT INTO refunds(payment_id,merchant_id,amount_kobo,reason,status,approval_status,admin_id)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id,payment_id,merchant_id,amount_kobo,reason,status,approval_status,provider_refund_id,last_error,
		          COALESCE(admin_id,'00000000-0000-0000-0000-000000000000'),created_at,completed_at`,
		paymentID, merchantID, amountKobo, reason, RefundPending, "pending_approval", adminID).Scan(
		&refund.ID, &refund.PaymentID, &refund.MerchantID, &refund.AmountKobo, &refund.Reason,
		&refund.Status, &refund.ApprovalStatus, &refund.ProviderRefundID, &refund.LastError,
		&refund.AdminID, &refund.CreatedAt, &refund.CompletedAt); err != nil {
		return Refund{}, fmt.Errorf("insert refund: %w", err)
	}

	return refund, tx.Commit(ctx)
}

// ApproveRefund marks a refund as approved and executes the actual refund:
// transitions payment to refunded, posts ledger reversal, emits event.
// Must be called by a different admin than the one who requested the refund.
func (s *Store) ApproveRefund(ctx context.Context, refundID, approvedBy uuid.UUID, postedBy string) (Refund, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Refund{}, err
	}
	defer tx.Rollback(ctx)

	// 1. Lock the refund and validate.
	var paymentID uuid.UUID
	var merchantID uuid.UUID
	var amountKobo int64
	var reason, approvalStatus string
	var requestedBy sql.NullString
	if err := tx.QueryRow(ctx, `
		SELECT payment_id, merchant_id, amount_kobo, reason, approval_status,
		       COALESCE(admin_id::text, '')
		FROM refunds WHERE id=$1 FOR UPDATE`, refundID).Scan(
		&paymentID, &merchantID, &amountKobo, &reason, &approvalStatus, &requestedBy); err != nil {
		return Refund{}, fmt.Errorf("load refund: %w", err)
	}
	if approvalStatus != "pending_approval" {
		return Refund{}, fmt.Errorf("refund is not pending approval (current: %s)", approvalStatus)
	}
	// Maker-checker: approver must differ from requester.
	if requestedBy.Valid && requestedBy.String == approvedBy.String() {
		return Refund{}, fmt.Errorf("approver must differ from the refund requester")
	}

	// 2. Lock the payment to prevent concurrent approve from double-reversing.
	var paymentStatus string
	var currency, reference string
	if err := tx.QueryRow(ctx, `
		SELECT status, currency, COALESCE(merchant_reference,'')
		FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(
		&paymentStatus, &currency, &reference); err != nil {
		return Refund{}, fmt.Errorf("lock payment: %w", err)
	}
	if paymentStatus != "succeeded" {
		return Refund{}, fmt.Errorf("payment is no longer succeeded (current: %s)", paymentStatus)
	}

	// 3. Mark approved.
	if _, err := tx.Exec(ctx, `
		UPDATE refunds SET approval_status='approved', approved_by=$2, approved_at=now()
		WHERE id=$1`, refundID, approvedBy); err != nil {
		return Refund{}, fmt.Errorf("approve refund: %w", err)
	}

	// 4. Transition payment to refunded.
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET status='refunded', updated_at=now() WHERE id=$1`, paymentID); err != nil {
		return Refund{}, fmt.Errorf("transition payment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,'succeeded','refunded',$2,$3::jsonb)`, paymentID, postedBy, fmt.Sprintf(`{"reason":%q}`, reason)); err != nil {
		return Refund{}, err
	}

	// 5. Post ledger reversal for the original payment.
	reversalJournal := paymentID.String()
	if _, err := s.postLedgerReversalTx(ctx, tx, reversalJournal, "Refund: "+reason, postedBy); err != nil {
		return Refund{}, fmt.Errorf("ledger reversal: %w", err)
	}

	// 6. Emit the payment.refunded event.
	fact, err := json.Marshal(domain.PaymentRefunded{
		PaymentID:  paymentID.String(),
		MerchantID: merchantID.String(),
		RefundID:   refundID.String(),
		Currency:   currency,
		Reference:  reference,
		AmountKobo: amountKobo,
		Reason:     reason,
	})
	if err != nil {
		return Refund{}, err
	}
	if err := s.insertBusinessEventTx(ctx, tx, "payment.refunded", "payment:"+paymentID.String(), fact); err != nil {
		return Refund{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Refund{}, err
	}
	return s.RefundByID(ctx, refundID)
}

// RejectRefund marks a refund request as rejected by a checker.
func (s *Store) RejectRefund(ctx context.Context, refundID, rejectedBy uuid.UUID, reason string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE refunds SET approval_status='rejected', approved_by=$2, approved_at=now(), last_error=$3
		WHERE id=$1 AND approval_status='pending_approval'`, refundID, rejectedBy, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("refund not found or not pending approval")
	}
	return nil
}

// postLedgerReversalTx is the transactional variant of PostLedgerReversal used
// by RefundPayment so the reversal commits atomically with the state change.
func (s *Store) postLedgerReversalTx(ctx context.Context, tx pgx.Tx, journalRef, reason, postedBy string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, entry_type, account, amount_kobo, currency, COALESCE(reversal_of,0), merchant_id
		FROM ledger_entries WHERE journal_ref=$1 ORDER BY id ASC`, journalRef)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id         int64
		entryType  string
		account    string
		amount     int64
		currency   string
		reversalOf int64
		merchantID *uuid.UUID
	}
	var originals []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.entryType, &e.account, &e.amount, &e.currency, &e.reversalOf, &e.merchantID); err != nil {
			rows.Close()
			return 0, err
		}
		originals = append(originals, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(originals) == 0 {
		return 0, fmt.Errorf("no ledger entries for journal reference %q", journalRef)
	}
	for _, o := range originals {
		if o.reversalOf != 0 {
			return 0, fmt.Errorf("journal reference %q is already reversed", journalRef)
		}
	}
	opposite := map[string]string{"debit": "credit", "credit": "debit"}
	reversed := 0
	for _, o := range originals {
		if _, err := s.appendLedgerEntryTx(ctx, tx, LedgerEntry{
			JournalRef: journalRef, SourceType: "reversal", SourceID: journalRef,
			EntryType: opposite[o.entryType], Account: o.account,
			AmountKobo: o.amount, Currency: o.currency,
			Description: "Reversal: " + reason, PostedBy: postedBy,
			ReversalOf: o.id, MerchantID: o.merchantID,
		}); err != nil {
			return 0, err
		}
		reversed++
	}
	return reversed, nil
}

// CompleteRefund marks a refund as succeeded after provider confirmation.
func (s *Store) CompleteRefund(ctx context.Context, refundID uuid.UUID, providerRefundID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE refunds SET status=$1, provider_refund_id=$2, completed_at=now()
		WHERE id=$3 AND status='pending'`, RefundSucceeded, providerRefundID, refundID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("refund not found or not in pending status")
	}
	return nil
}

// FailRefund marks a refund as failed with a provider error message.
func (s *Store) FailRefund(ctx context.Context, refundID uuid.UUID, message string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE refunds SET status=$1, last_error=$2, completed_at=now()
		WHERE id=$3 AND status='pending'`, RefundFailed, message, refundID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("refund not found or not in pending status")
	}
	return nil
}

// RefundByID returns one refund regardless of merchant.
func (s *Store) RefundByID(ctx context.Context, refundID uuid.UUID) (Refund, error) {
	var r Refund
	err := s.pool.QueryRow(ctx, `
		SELECT id,payment_id,merchant_id,amount_kobo,reason,status,approval_status,provider_refund_id,last_error,
		       COALESCE(admin_id,'00000000-0000-0000-0000-000000000000'),
		       COALESCE(approved_by,'00000000-0000-0000-0000-000000000000'),
		       created_at,completed_at,approved_at
		FROM refunds WHERE id=$1`, refundID).Scan(
		&r.ID, &r.PaymentID, &r.MerchantID, &r.AmountKobo, &r.Reason,
		&r.Status, &r.ApprovalStatus, &r.ProviderRefundID, &r.LastError,
		&r.AdminID, &r.ApprovedBy, &r.CreatedAt, &r.CompletedAt, &r.ApprovedAt)
	if err != nil {
		return Refund{}, err
	}
	return r, nil
}

// ListRefunds returns a merchant's refunds, newest first.
func (s *Store) ListRefunds(ctx context.Context, merchantID uuid.UUID, limit int) ([]Refund, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,payment_id,merchant_id,amount_kobo,reason,status,approval_status,provider_refund_id,last_error,
		       COALESCE(admin_id,'00000000-0000-0000-0000-000000000000'),
		       COALESCE(approved_by,'00000000-0000-0000-0000-000000000000'),
		       created_at,completed_at,approved_at
		FROM refunds WHERE merchant_id=$1
		ORDER BY created_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRefunds(rows)
}

// ListAllRefunds returns every refund for the admin console, newest first.
func (s *Store) ListAllRefunds(ctx context.Context, limit int) ([]Refund, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,payment_id,merchant_id,amount_kobo,reason,status,approval_status,provider_refund_id,last_error,
		       COALESCE(admin_id,'00000000-0000-0000-0000-000000000000'),
		       COALESCE(approved_by,'00000000-0000-0000-0000-000000000000'),
		       created_at,completed_at,approved_at
		FROM refunds ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRefunds(rows)
}

func scanRefunds(rows pgx.Rows) ([]Refund, error) {
	var refunds []Refund
	for rows.Next() {
		var r Refund
		if err := rows.Scan(
			&r.ID, &r.PaymentID, &r.MerchantID, &r.AmountKobo, &r.Reason,
			&r.Status, &r.ApprovalStatus, &r.ProviderRefundID, &r.LastError,
			&r.AdminID, &r.ApprovedBy, &r.CreatedAt, &r.CompletedAt, &r.ApprovedAt); err != nil {
			return nil, err
		}
		refunds = append(refunds, r)
	}
	return refunds, rows.Err()
}

// Disputes

// CreateDispute records a provider-initiated dispute against a payment.
func (s *Store) CreateDispute(ctx context.Context, paymentID uuid.UUID, reason string) (Dispute, error) {
	var merchantID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT merchant_id FROM payments WHERE id=$1`, paymentID).Scan(&merchantID); err != nil {
		return Dispute{}, fmt.Errorf("load payment: %w", err)
	}
	var d Dispute
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO disputes(payment_id,merchant_id,reason,status)
		VALUES($1,$2,$3,$4)
		RETURNING id,payment_id,merchant_id,reason,status,created_at,resolved_at`,
		paymentID, merchantID, reason, DisputeOpen).Scan(
		&d.ID, &d.PaymentID, &d.MerchantID, &d.Reason, &d.Status, &d.CreatedAt, &d.ResolvedAt); err != nil {
		return Dispute{}, fmt.Errorf("insert dispute: %w", err)
	}

	// Emit the payment.disputed event.
	fact, err := json.Marshal(domain.PaymentDisputed{
		PaymentID:  paymentID.String(),
		MerchantID: merchantID.String(),
		DisputeID:  d.ID.String(),
		Reference:  paymentID.String(),
		Reason:     reason,
	})
	if err != nil {
		return Dispute{}, err
	}
	if err := s.InsertBusinessEvent(ctx, "payment.disputed", "payment:"+paymentID.String(), []byte(fact)); err != nil {
		return Dispute{}, err
	}

	return d, nil
}

// ResolveDispute marks a dispute as won or lost.
func (s *Store) ResolveDispute(ctx context.Context, disputeID uuid.UUID, outcome string) error {
	if outcome != DisputeWon && outcome != DisputeLost && outcome != DisputeExpired {
		return fmt.Errorf("invalid dispute outcome %q", outcome)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE disputes SET status=$1, resolved_at=now()
		WHERE id=$2 AND status='open'`, outcome, disputeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("dispute not found or not open")
	}
	return nil
}

// DisputeByID returns one dispute regardless of merchant.
func (s *Store) DisputeByID(ctx context.Context, disputeID uuid.UUID) (Dispute, error) {
	var d Dispute
	err := s.pool.QueryRow(ctx, `
		SELECT id,payment_id,merchant_id,reason,status,created_at,resolved_at
		FROM disputes WHERE id=$1`, disputeID).Scan(
		&d.ID, &d.PaymentID, &d.MerchantID, &d.Reason, &d.Status, &d.CreatedAt, &d.ResolvedAt)
	if err != nil {
		return Dispute{}, err
	}
	return d, nil
}

// ListDisputes returns a merchant's disputes, newest first.
func (s *Store) ListDisputes(ctx context.Context, merchantID uuid.UUID, limit int) ([]Dispute, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,payment_id,merchant_id,reason,status,created_at,resolved_at
		FROM disputes WHERE merchant_id=$1
		ORDER BY created_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDisputes(rows)
}

// ListAllDisputes returns every dispute for the admin console, newest first.
func (s *Store) ListAllDisputes(ctx context.Context, limit int) ([]Dispute, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,payment_id,merchant_id,reason,status,created_at,resolved_at
		FROM disputes ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDisputes(rows)
}

func scanDisputes(rows pgx.Rows) ([]Dispute, error) {
	var disputes []Dispute
	for rows.Next() {
		var d Dispute
		if err := rows.Scan(&d.ID, &d.PaymentID, &d.MerchantID, &d.Reason, &d.Status, &d.CreatedAt, &d.ResolvedAt); err != nil {
			return nil, err
		}
		disputes = append(disputes, d)
	}
	return disputes, rows.Err()
}
