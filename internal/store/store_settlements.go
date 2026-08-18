// S1: settlement + payout store. A settlement batch freezes a merchant's
// accrued 3100 merchant_payable liability at a point in time (dr 3100 /
// cr 3200 settlement_payable), and the payout moves funds out of the operating
// bank (dr 3200 / cr 1100) once the provider confirms. Both postings are
// merchant-scoped on the append-only ledger so merchant balances keep working.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// SettlementAccountStatus values for merchant_settlement_accounts.status.
const (
	SettlementAccountActive   = "active"
	SettlementAccountDisabled = "disabled"
)

// Settlement batch statuses.
const (
	SettlementBatchOpen      = "open"
	SettlementBatchScheduled = "scheduled"
	SettlementBatchProcessed = "processed"
	SettlementBatchFailed    = "failed"
)

// Payout statuses.
const (
	PayoutQueued     = "queued"
	PayoutProcessing = "processing"
	PayoutCompleted  = "completed"
	PayoutFailed     = "failed"
	PayoutReversed   = "reversed"
)

// SettlementProviderDefault is the rail used when none is configured.
const SettlementProviderDefault = "simulated"

// PayoutMaxAttempts bounds retries before a payout is left for operator action.
const PayoutMaxAttempts = 3

// MerchantSettlementAccount is a verified payout destination.
type MerchantSettlementAccount struct {
	ID            uuid.UUID
	MerchantID    uuid.UUID
	BankCode      string
	AccountNumber string
	AccountName   string
	IsDefault     bool
	Status        string
	ApprovedBy    *uuid.UUID
	CreatedAt     time.Time
}

// SettlementBatch is one cut of a merchant's payable.
type SettlementBatch struct {
	ID            uuid.UUID
	BatchNo       string
	MerchantID    uuid.UUID
	Status        string
	TotalKobo     int64
	FeeKobo       int64
	FeeBps        int
	LineCount     int
	CutoffAt      time.Time
	LedgerJournal string
	CreatedAt     time.Time
	ProcessedAt   *time.Time
}

// SettlementLine is one collected payment inside a batch.
type SettlementLine struct {
	ID         uuid.UUID
	BatchID    uuid.UUID
	PaymentID  uuid.UUID
	AmountKobo int64
}

// Payout is one outbound funds movement for a settlement batch.
type Payout struct {
	ID            uuid.UUID
	BatchID       uuid.UUID
	MerchantID    uuid.UUID
	DestinationID uuid.UUID
	AmountKobo    int64
	Status        string
	ExternalRef   string
	Provider      string
	Attempts      int
	LastError     string
	LedgerJournal string
	CreatedAt     time.Time
	CompletedAt   *time.Time
}

// PayoutDispatch is the claimed payout plus the context a provider call needs.
type PayoutDispatch struct {
	Payout      Payout
	Batch       SettlementBatch
	Destination MerchantSettlementAccount
}

// ---------------------------------------------------------------------------
// Merchant settlement accounts
// ---------------------------------------------------------------------------

// CreateSettlementAccount registers a payout destination. The first account
// for a merchant becomes the default.
func (s *Store) CreateSettlementAccount(ctx context.Context, merchantID uuid.UUID, bankCode, accountNumber, accountName string) (MerchantSettlementAccount, error) {
	var account MerchantSettlementAccount
	err := s.pool.QueryRow(ctx, `
		INSERT INTO merchant_settlement_accounts(merchant_id,bank_code,account_number,account_name,is_default)
		SELECT $1,$2,$3,$4, NOT EXISTS(SELECT 1 FROM merchant_settlement_accounts WHERE merchant_id=$1 AND status='active')
		RETURNING id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at`,
		merchantID, bankCode, accountNumber, accountName).Scan(
		&account.ID, &account.MerchantID, &account.BankCode, &account.AccountNumber,
		&account.AccountName, &account.IsDefault, &account.Status, &account.ApprovedBy, &account.CreatedAt)
	if err != nil {
		return account, fmt.Errorf("create settlement account: %w", err)
	}
	return account, nil
}

// ListSettlementAccounts returns the merchant's payout destinations.
func (s *Store) ListSettlementAccounts(ctx context.Context, merchantID uuid.UUID) ([]MerchantSettlementAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at
		FROM merchant_settlement_accounts WHERE merchant_id=$1 ORDER BY created_at`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []MerchantSettlementAccount
	for rows.Next() {
		var a MerchantSettlementAccount
		if err := rows.Scan(&a.ID, &a.MerchantID, &a.BankCode, &a.AccountNumber,
			&a.AccountName, &a.IsDefault, &a.Status, &a.ApprovedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// ListAllSettlementAccounts returns every merchant's payout destinations for
// the operator console, newest first.
func (s *Store) ListAllSettlementAccounts(ctx context.Context) ([]MerchantSettlementAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at
		FROM merchant_settlement_accounts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []MerchantSettlementAccount
	for rows.Next() {
		var a MerchantSettlementAccount
		if err := rows.Scan(&a.ID, &a.MerchantID, &a.BankCode, &a.AccountNumber,
			&a.AccountName, &a.IsDefault, &a.Status, &a.ApprovedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// SettlementAccountByID returns one account regardless of merchant.
func (s *Store) SettlementAccountByID(ctx context.Context, accountID uuid.UUID) (MerchantSettlementAccount, error) {
	var a MerchantSettlementAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at
		FROM merchant_settlement_accounts WHERE id=$1`, accountID).Scan(
		&a.ID, &a.MerchantID, &a.BankCode, &a.AccountNumber,
		&a.AccountName, &a.IsDefault, &a.Status, &a.ApprovedBy, &a.CreatedAt)
	return a, err
}

// ApproveSettlementAccount marks a destination approved by an admin operator.
func (s *Store) ApproveSettlementAccount(ctx context.Context, accountID, adminID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_settlement_accounts SET approved_by=$2,status='active' WHERE id=$1`, accountID, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("settlement account not found")
	}
	return nil
}

// DisableSettlementAccount disables a destination so payouts cannot target it.
func (s *Store) DisableSettlementAccount(ctx context.Context, accountID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_settlement_accounts SET status='disabled' WHERE id=$1`, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("settlement account not found")
	}
	return nil
}

// DefaultSettlementAccount returns the merchant's default active destination,
// or the first active one if no default is flagged.
func (s *Store) DefaultSettlementAccount(ctx context.Context, merchantID uuid.UUID) (MerchantSettlementAccount, error) {
	var a MerchantSettlementAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at
		FROM merchant_settlement_accounts
		WHERE merchant_id=$1 AND status='active'
		ORDER BY is_default DESC, created_at LIMIT 1`, merchantID).Scan(
		&a.ID, &a.MerchantID, &a.BankCode, &a.AccountNumber,
		&a.AccountName, &a.IsDefault, &a.Status, &a.ApprovedBy, &a.CreatedAt)
	return a, err
}

// ---------------------------------------------------------------------------
// Settlement batches
// ---------------------------------------------------------------------------

// CutSettlement freezes the merchant's succeeded, un-batched payments into a
// batch and moves the liability from 3100 to 3200 on the ledger. A fee is
// deducted as a separate ledger entry (dr 3200 / cr 5200) so the payout amount
// is always total minus fee. Idempotent on batch_no.
func (s *Store) CutSettlement(ctx context.Context, merchantID uuid.UUID, batchNo string, cutoffAt time.Time, feeBps int) (SettlementBatch, error) {
	if existing, err := s.SettlementBatchByBatchNo(ctx, batchNo); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return SettlementBatch{}, err
	}
	if cutoffAt.IsZero() {
		cutoffAt = time.Now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SettlementBatch{}, err
	}
	defer tx.Rollback(ctx)

	type line struct {
		paymentID uuid.UUID
		amount    int64
	}
	var lines []line
	var total int64
	rows, err := tx.Query(ctx, `
		SELECT id, amount_kobo
		FROM payments
		WHERE status='succeeded' AND merchant_id=$1
		  AND NOT EXISTS (
		      SELECT 1 FROM settlement_lines sl
		      JOIN settlement_batches sb ON sb.id=sl.batch_id
		      WHERE sl.payment_id=payments.id AND sb.status IN ('open','scheduled','processed'))
		ORDER BY id
		FOR UPDATE SKIP LOCKED`, merchantID)
	if err != nil {
		return SettlementBatch{}, err
	}
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.paymentID, &l.amount); err != nil {
			rows.Close()
			return SettlementBatch{}, err
		}
		lines = append(lines, l)
		total += l.amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SettlementBatch{}, err
	}
	if total <= 0 {
		return SettlementBatch{}, fmt.Errorf("nothing to settle for merchant %s", merchantID)
	}
	feeKobo := total * int64(feeBps) / 10_000

	var batch SettlementBatch
	if err := tx.QueryRow(ctx, `
		INSERT INTO settlement_batches(batch_no,merchant_id,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at`,
		batchNo, merchantID, total, feeKobo, feeBps, len(lines), cutoffAt, "STL:"+batchNo).Scan(
		&batch.ID, &batch.BatchNo, &batch.MerchantID, &batch.Status, &batch.TotalKobo,
		&batch.FeeKobo, &batch.FeeBps, &batch.LineCount, &batch.CutoffAt, &batch.LedgerJournal, &batch.CreatedAt, &batch.ProcessedAt); err != nil {
		return SettlementBatch{}, fmt.Errorf("insert settlement batch: %w", err)
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO settlement_lines(batch_id,payment_id,amount_kobo) VALUES($1,$2,$3)`,
			batch.ID, l.paymentID, l.amount); err != nil {
			return SettlementBatch{}, err
		}
	}
	if err := s.postLedgerPair(ctx, tx, "STL:"+batchNo, "settlement", batch.ID.String(),
		LedgerAccountMerchantPayable, LedgerAccountSettlementPayable, "NGN",
		"Settlement cut "+batchNo, "system", total, &merchantID); err != nil {
		return SettlementBatch{}, err
	}
	if feeKobo > 0 {
		if err := s.postLedgerPair(ctx, tx, "STL:"+batchNo+":FEE", "settlement", batch.ID.String(),
			LedgerAccountSettlementPayable, LedgerAccountSettlementFees, "NGN",
			"Settlement fee "+batchNo, "system", feeKobo, &merchantID); err != nil {
			return SettlementBatch{}, err
		}
	}
	if err := s.emitSettlementBatchCreatedTx(ctx, tx, batch); err != nil {
		return SettlementBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SettlementBatch{}, err
	}
	return batch, nil
}

// SettlementBatchByBatchNo returns a batch by its external reference.
func (s *Store) SettlementBatchByBatchNo(ctx context.Context, batchNo string) (SettlementBatch, error) {
	var b SettlementBatch
	err := s.pool.QueryRow(ctx, `
		SELECT id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at
		FROM settlement_batches WHERE batch_no=$1`, batchNo).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.Status, &b.TotalKobo,
		&b.FeeKobo, &b.FeeBps, &b.LineCount, &b.CutoffAt, &b.LedgerJournal, &b.CreatedAt, &b.ProcessedAt)
	return b, err
}

// SettlementBatchByID returns a batch by its internal id.
func (s *Store) SettlementBatchByID(ctx context.Context, batchID uuid.UUID) (SettlementBatch, error) {
	var b SettlementBatch
	err := s.pool.QueryRow(ctx, `
		SELECT id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at
		FROM settlement_batches WHERE id=$1`, batchID).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.Status, &b.TotalKobo,
		&b.FeeKobo, &b.FeeBps, &b.LineCount, &b.CutoffAt, &b.LedgerJournal, &b.CreatedAt, &b.ProcessedAt)
	return b, err
}

// ListSettlementBatches returns a merchant's batches, newest first.
func (s *Store) ListSettlementBatches(ctx context.Context, merchantID uuid.UUID, limit int) ([]SettlementBatch, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at
		FROM settlement_batches WHERE merchant_id=$1 ORDER BY created_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []SettlementBatch
	for rows.Next() {
		var b SettlementBatch
		if err := rows.Scan(&b.ID, &b.BatchNo, &b.MerchantID, &b.Status, &b.TotalKobo,
			&b.FeeKobo, &b.FeeBps, &b.LineCount, &b.CutoffAt, &b.LedgerJournal, &b.CreatedAt, &b.ProcessedAt); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// ListAllSettlementBatches returns every merchant's batches for the operator
// console, newest first.
func (s *Store) ListAllSettlementBatches(ctx context.Context, limit int) ([]SettlementBatch, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at
		FROM settlement_batches ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []SettlementBatch
	for rows.Next() {
		var b SettlementBatch
		if err := rows.Scan(&b.ID, &b.BatchNo, &b.MerchantID, &b.Status, &b.TotalKobo,
			&b.FeeKobo, &b.FeeBps, &b.LineCount, &b.CutoffAt, &b.LedgerJournal, &b.CreatedAt, &b.ProcessedAt); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// SettlementLinesByBatch returns the payments inside a batch.
func (s *Store) SettlementLinesByBatch(ctx context.Context, batchID uuid.UUID) ([]SettlementLine, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_id,payment_id,amount_kobo FROM settlement_lines WHERE batch_id=$1 ORDER BY created_at`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []SettlementLine
	for rows.Next() {
		var l SettlementLine
		if err := rows.Scan(&l.ID, &l.BatchID, &l.PaymentID, &l.AmountKobo); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// ---------------------------------------------------------------------------
// Payouts
// ---------------------------------------------------------------------------

// CreatePayout registers the payout for a batch. Idempotent: the existing
// non-reversed payout for the batch is returned when present.
func (s *Store) CreatePayout(ctx context.Context, batchID, destinationID uuid.UUID) (Payout, error) {
	if existing, err := s.PayoutByBatchID(ctx, batchID); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Payout{}, err
	}
	var p Payout
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payouts(batch_id,merchant_id,destination_id,amount_kobo,provider)
		SELECT b.id,b.merchant_id,$2,(b.total_kobo - b.fee_kobo),$3
		FROM settlement_batches b WHERE b.id=$1
		RETURNING id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at`,
		batchID, destinationID, SettlementProviderDefault).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt)
	if err != nil {
		return p, fmt.Errorf("create payout: %w", err)
	}
	return p, nil
}

// DailyPayoutStats returns today's payout count and total amount for a merchant.
func (s *Store) DailyPayoutStats(ctx context.Context, merchantID uuid.UUID) (count int, totalKobo int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(amount_kobo),0)
		FROM payouts WHERE merchant_id=$1
		  AND created_at >= date_trunc('day', now())
		  AND status != 'reversed'`, merchantID).Scan(&count, &totalKobo)
	return
}

// ClaimPayoutForDispatch leases a queued payout for one provider attempt and
// marks the batch scheduled. Returns the claim or pgx.ErrNoRows when nothing
// is queued.
func (s *Store) ClaimPayoutForDispatch(ctx context.Context, batchID uuid.UUID) (PayoutDispatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PayoutDispatch{}, err
	}
	defer tx.Rollback(ctx)

	var d PayoutDispatch
	if err := tx.QueryRow(ctx, `
		SELECT p.id,p.batch_id,p.merchant_id,p.destination_id,p.amount_kobo,p.status,
		       COALESCE(p.external_ref,''),p.provider,p.attempts,p.last_error,p.ledger_journal,p.created_at,p.completed_at
		FROM payouts p
		WHERE p.batch_id=$1 AND p.status='queued'
		FOR UPDATE SKIP LOCKED`, batchID).Scan(
		&d.Payout.ID, &d.Payout.BatchID, &d.Payout.MerchantID, &d.Payout.DestinationID, &d.Payout.AmountKobo,
		&d.Payout.Status, &d.Payout.ExternalRef, &d.Payout.Provider, &d.Payout.Attempts,
		&d.Payout.LastError, &d.Payout.LedgerJournal, &d.Payout.CreatedAt, &d.Payout.CompletedAt); err != nil {
		return PayoutDispatch{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payouts SET status='processing',attempts=attempts+1 WHERE id=$1`, d.Payout.ID); err != nil {
		return PayoutDispatch{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE settlement_batches SET status='scheduled' WHERE id=$1`, batchID); err != nil {
		return PayoutDispatch{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT id,batch_no,merchant_id,status,total_kobo,fee_kobo,fee_bps,line_count,cutoff_at,ledger_journal,created_at,processed_at
		FROM settlement_batches WHERE id=$1`, batchID).Scan(
		&d.Batch.ID, &d.Batch.BatchNo, &d.Batch.MerchantID, &d.Batch.Status, &d.Batch.TotalKobo,
		&d.Batch.FeeKobo, &d.Batch.FeeBps, &d.Batch.LineCount, &d.Batch.CutoffAt, &d.Batch.LedgerJournal, &d.Batch.CreatedAt, &d.Batch.ProcessedAt); err != nil {
		return PayoutDispatch{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT id,merchant_id,bank_code,account_number,account_name,is_default,status,approved_by,created_at
		FROM merchant_settlement_accounts WHERE id=$1`, d.Payout.DestinationID).Scan(
		&d.Destination.ID, &d.Destination.MerchantID, &d.Destination.BankCode, &d.Destination.AccountNumber,
		&d.Destination.AccountName, &d.Destination.IsDefault, &d.Destination.Status,
		&d.Destination.ApprovedBy, &d.Destination.CreatedAt); err != nil {
		return PayoutDispatch{}, err
	}
	return d, tx.Commit(ctx)
}

// CompletePayout records a confirmed payout: money leaves the operating bank
// (dr 3200 / cr 1100), the batch is processed, and payout.succeeded plus
// settlement.batch.processed are outboxed.
func (s *Store) CompletePayout(ctx context.Context, payoutID uuid.UUID, externalRef string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var p Payout
	if err := tx.QueryRow(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE id=$1 FOR UPDATE`, payoutID).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt); err != nil {
		return err
	}
	if p.Status != PayoutProcessing {
		return fmt.Errorf("payout %s is not processing (status %s)", payoutID, p.Status)
	}
	var batchNo string
	if err := tx.QueryRow(ctx, `SELECT batch_no FROM settlement_batches WHERE id=$1`, p.BatchID).Scan(&batchNo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payouts SET status='completed',external_ref=$2,completed_at=now(),ledger_journal=$3 WHERE id=$1`,
		payoutID, externalRef, "PAY:"+batchNo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE settlement_batches SET status='processed',processed_at=now() WHERE id=$1`, p.BatchID); err != nil {
		return err
	}
	merchantID := p.MerchantID
	if err := s.postLedgerPair(ctx, tx, "PAY:"+batchNo, "payout", p.ID.String(),
		LedgerAccountSettlementPayable, LedgerAccountOperatingBank, "NGN",
		"Payout "+batchNo+" "+externalRef, "system", p.AmountKobo, &merchantID); err != nil {
		return err
	}
	now := time.Now()
	if err := s.insertBusinessEventTx(ctx, tx, domain.TopicPayoutSucceeded, "payout:"+p.ID.String(), mustJSON(domain.PayoutSucceeded{
		PayoutID:    p.ID.String(),
		BatchNo:     batchNo,
		MerchantID:  p.MerchantID.String(),
		AmountKobo:  p.AmountKobo,
		ExternalRef: externalRef,
		CompletedAt: now,
	})); err != nil {
		return err
	}
	if err := s.insertBusinessEventTx(ctx, tx, domain.TopicSettlementBatchProcessed, "settlement:"+p.BatchID.String(), mustJSON(domain.SettlementBatchProcessed{
		BatchID:    p.BatchID.String(),
		BatchNo:    batchNo,
		MerchantID: p.MerchantID.String(),
		TotalKobo:  p.AmountKobo,
		PayoutID:   p.ID.String(),
	})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailPayout records a declined provider attempt. The batch stays scheduled;
// the operator can retry (bounded) or reverse.
func (s *Store) FailPayout(ctx context.Context, payoutID uuid.UUID, message string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE payouts SET status='failed',last_error=$2 WHERE id=$1 AND status='processing'`, payoutID, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("payout is not processing")
	}
	return s.insertPayoutFailed(ctx, payoutID, message)
}

// RetryPayout re-queues a failed payout while attempts remain.
func (s *Store) RetryPayout(ctx context.Context, payoutID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE payouts SET status='queued',last_error=''
		WHERE id=$1 AND status='failed' AND attempts < $2`, payoutID, PayoutMaxAttempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("payout cannot be retried")
	}
	return nil
}

// ReversePayout cancels a failed payout and reopens the batch so a fresh
// payout can be dispatched. Money never left the bank (no ledger posting was
// made for a failed payout), so no ledger reversal is needed.
func (s *Store) ReversePayout(ctx context.Context, payoutID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var p Payout
	if err := tx.QueryRow(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE id=$1 FOR UPDATE`, payoutID).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt); err != nil {
		return err
	}
	if p.Status != PayoutFailed {
		return fmt.Errorf("only a failed payout can be reversed, payout %s is %s", payoutID, p.Status)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payouts SET status='reversed',last_error=$2 WHERE id=$1`, payoutID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE settlement_batches SET status='open' WHERE id=$1 AND status='scheduled'`, p.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RequeueStalePayouts resets payouts stuck in processing (crashed between the
// claim and the provider outcome) back to queued so they can be re-attempted.
func (s *Store) RequeueStalePayouts(ctx context.Context, olderThan time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE payouts SET status='queued'
		WHERE status='processing' AND updated_at <= now()-$1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// PayoutByID returns one payout with no joins.
func (s *Store) PayoutByID(ctx context.Context, payoutID uuid.UUID) (Payout, error) {
	var p Payout
	err := s.pool.QueryRow(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE id=$1`, payoutID).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt)
	return p, err
}

// PayoutByBatchID returns the batch's non-reversed payout.
func (s *Store) PayoutByBatchID(ctx context.Context, batchID uuid.UUID) (Payout, error) {
	var p Payout
	err := s.pool.QueryRow(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE batch_id=$1 AND status <> 'reversed'
		ORDER BY created_at LIMIT 1`, batchID).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt)
	return p, err
}

// PayoutByBatchNo returns the payout for a batch by batch reference.
func (s *Store) PayoutByBatchNo(ctx context.Context, batchNo string) (Payout, error) {
	var p Payout
	err := s.pool.QueryRow(ctx, `
		SELECT p.id,p.batch_id,p.merchant_id,p.destination_id,p.amount_kobo,p.status,
		       COALESCE(p.external_ref,''),p.provider,p.attempts,p.last_error,p.ledger_journal,p.created_at,p.completed_at
		FROM payouts p JOIN settlement_batches b ON b.id=p.batch_id
		WHERE b.batch_no=$1 AND p.status <> 'reversed'
		ORDER BY p.created_at LIMIT 1`, batchNo).Scan(
		&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
		&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt)
	return p, err
}

// ListPayouts returns a merchant's payouts, newest first.
func (s *Store) ListPayouts(ctx context.Context, merchantID uuid.UUID, limit int) ([]Payout, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE merchant_id=$1 ORDER BY created_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []Payout
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
			&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

// ListAllPayouts returns every merchant's payouts for the operator console,
// newest first.
func (s *Store) ListAllPayouts(ctx context.Context, limit int) ([]Payout, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []Payout
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
			&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

// ListQueuedPayouts returns payouts waiting for a provider attempt, oldest
// first, for the dispatch worker.
func (s *Store) ListQueuedPayouts(ctx context.Context, limit int) ([]Payout, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,batch_id,merchant_id,destination_id,amount_kobo,status,COALESCE(external_ref,''),provider,attempts,last_error,ledger_journal,created_at,completed_at
		FROM payouts WHERE status='queued' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []Payout
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.BatchID, &p.MerchantID, &p.DestinationID, &p.AmountKobo, &p.Status,
			&p.ExternalRef, &p.Provider, &p.Attempts, &p.LastError, &p.LedgerJournal, &p.CreatedAt, &p.CompletedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func (s *Store) emitSettlementBatchCreatedTx(ctx context.Context, tx pgx.Tx, batch SettlementBatch) error {
	return s.insertBusinessEventTx(ctx, tx, domain.TopicSettlementBatchCreated, "settlement:"+batch.ID.String(), mustJSON(domain.SettlementBatchCreated{
		BatchID:    batch.ID.String(),
		BatchNo:    batch.BatchNo,
		MerchantID: batch.MerchantID.String(),
		TotalKobo:  batch.TotalKobo,
		Currency:   "NGN",
		CutoffAt:   batch.CutoffAt,
	}))
}

func (s *Store) insertPayoutFailed(ctx context.Context, payoutID uuid.UUID, message string) error {
	p, err := s.PayoutByID(ctx, payoutID)
	if err != nil {
		return err
	}
	var batchNo string
	if err := s.pool.QueryRow(ctx, `SELECT batch_no FROM settlement_batches WHERE id=$1`, p.BatchID).Scan(&batchNo); err != nil {
		return err
	}
	return s.InsertBusinessEvent(ctx, domain.TopicPayoutFailed, "payout:"+payoutID.String(), mustJSON(domain.PayoutFailed{
		PayoutID:   p.ID.String(),
		BatchNo:    batchNo,
		MerchantID: p.MerchantID.String(),
		AmountKobo: p.AmountKobo,
		Message:    message,
	}))
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
