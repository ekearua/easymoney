// Wallet cash-out store: user-initiated withdrawals from an individual wallet
// to an external bank account. Unlike WithdrawFromUserWallet (which only moves
// money out of the wallet), a cash-out records a real NIP destination and the
// provider lifecycle, so the rail can be dispatched and, on terminal failure,
// reversed back into the wallet.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Wallet payout statuses. They mirror the settlement payout vocabulary.
const (
	WalletPayoutQueued     = "queued"
	WalletPayoutProcessing = "processing"
	WalletPayoutCompleted  = "completed"
	WalletPayoutFailed     = "failed"
	WalletPayoutReversed   = "reversed"
)

// WalletPayout is one outbound cash-out from an individual wallet.
type WalletPayout struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	DestinationID  *uuid.UUID
	BankCode       string
	BankName       string
	AccountNumber  string
	AccountName    string
	AmountKobo     int64 // total debited from the wallet
	PayoutKobo     int64 // amount sent to the bank (amount - fee)
	FeeKobo        int64
	Status         string
	Provider       string
	ExternalRef    string
	Attempts       int
	LastError      string
	ReservationRef string
	LedgerJournal  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

const walletPayoutCols = `id,user_id,destination_id,bank_code,bank_name,account_number,account_name,
	amount_kobo,payout_kobo,fee_kobo,status,provider,external_ref,attempts,last_error,
	reservation_ref,ledger_journal,created_at,updated_at,completed_at`

func scanWalletPayout(row pgx.Row) (WalletPayout, error) {
	var p WalletPayout
	err := row.Scan(&p.ID, &p.UserID, &p.DestinationID, &p.BankCode, &p.BankName,
		&p.AccountNumber, &p.AccountName, &p.AmountKobo, &p.PayoutKobo, &p.FeeKobo,
		&p.Status, &p.Provider, &p.ExternalRef, &p.Attempts, &p.LastError,
		&p.ReservationRef, &p.LedgerJournal, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt)
	return p, err
}

// CreateWalletPayoutInput is the neutral request the service hands to the
// store. The store owns the ledger posting: the caller supplies the amounts,
// the store makes the debit and the fee split atomic with the row insert.
type CreateWalletPayoutInput struct {
	UserID         uuid.UUID
	DestinationID  *uuid.UUID
	BankCode       string
	BankName       string
	AccountNumber  string
	AccountName    string
	AmountKobo     int64
	PayoutKobo     int64
	FeeKobo        int64
	Provider       string
	ReservationRef string
	Description    string
}

// CreateWalletPayout debits the wallet and records the cash-out in one
// transaction. It is idempotent on ReservationRef: a replay returns the
// already-created row without posting again.
//
// Postings (all under the wallet-payout journal):
//
//	dr 2301_user_wallet:<user> / cr 1100_operating_bank   payout_kobo
//	dr 2301_user_wallet:<user> / cr 2300_xego_payable     fee_kobo
//
// The wallet row is locked FOR UPDATE before the balance is read, so two
// concurrent cash-outs cannot both pass the same balance check.
func (s *Store) CreateWalletPayout(ctx context.Context, in CreateWalletPayoutInput) (WalletPayout, error) {
	if in.AmountKobo <= 0 {
		return WalletPayout{}, errors.New("wallet cash-out requires a positive amount")
	}
	if in.PayoutKobo <= 0 {
		return WalletPayout{}, errors.New("wallet cash-out requires a positive payout amount")
	}
	if in.FeeKobo < 0 {
		return WalletPayout{}, errors.New("wallet cash-out fee cannot be negative")
	}
	if in.PayoutKobo+in.FeeKobo != in.AmountKobo {
		return WalletPayout{}, fmt.Errorf("wallet cash-out amounts do not balance: payout %d + fee %d != amount %d",
			in.PayoutKobo, in.FeeKobo, in.AmountKobo)
	}
	if in.ReservationRef == "" {
		return WalletPayout{}, errors.New("wallet cash-out requires a reservation reference")
	}
	if in.AccountNumber == "" || in.BankCode == "" {
		return WalletPayout{}, errors.New("wallet cash-out requires a destination account and bank code")
	}
	if in.Provider == "" {
		in.Provider = SettlementProviderDefault
	}
	if in.Description == "" {
		in.Description = "Wallet cash-out"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WalletPayout{}, err
	}
	defer tx.Rollback(ctx)

	if existing, err := scanWalletPayout(tx.QueryRow(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE reservation_ref=$1`,
		in.ReservationRef)); err == nil {
		return existing, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return WalletPayout{}, err
	}

	// Lock the wallet row so concurrent cash-outs serialize on the balance
	// check. ensureWalletTx creates the row (pending) when absent, matching
	// the other wallet paths.
	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, in.UserID, "", WalletStatusPending)
	if err != nil {
		return WalletPayout{}, err
	}
	var status, accountCode string
	if err := tx.QueryRow(ctx,
		`SELECT status, account_code FROM wallet_accounts WHERE id=$1 FOR UPDATE`,
		wallet.ID).Scan(&status, &accountCode); err != nil {
		return WalletPayout{}, err
	}
	if status != WalletStatusActive {
		return WalletPayout{}, fmt.Errorf("%w: wallet %s is %s (reach L1 to cash out)", ErrWalletNotActive, wallet.ID, status)
	}
	balance, err := walletBalanceQ(ctx, tx, accountCode)
	if err != nil {
		return WalletPayout{}, err
	}
	if balance < in.AmountKobo {
		return WalletPayout{}, fmt.Errorf("%w: have %d kobo, need %d kobo", ErrInsufficientWalletBalance, balance, in.AmountKobo)
	}

	payoutID := uuid.New()
	journal := "wallet-payout:" + payoutID.String()
	payout, err := scanWalletPayout(tx.QueryRow(ctx, `
		INSERT INTO wallet_payouts(id,user_id,destination_id,bank_code,bank_name,account_number,account_name,
			amount_kobo,payout_kobo,fee_kobo,status,provider,reservation_ref,ledger_journal)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued',$11,$12,$13)
		RETURNING `+walletPayoutCols,
		payoutID, in.UserID, in.DestinationID, in.BankCode, in.BankName, in.AccountNumber,
		in.AccountName, in.AmountKobo, in.PayoutKobo, in.FeeKobo, in.Provider,
		in.ReservationRef, journal))
	if err != nil {
		return WalletPayout{}, fmt.Errorf("create wallet payout: %w", err)
	}

	if err := s.postLedgerPair(ctx, tx, journal, "wallet_payout", payoutID.String(),
		accountCode, LedgerAccountOperatingBank, "NGN", in.Description+" payout",
		"system", in.PayoutKobo, nil); err != nil {
		return WalletPayout{}, err
	}
	if in.FeeKobo > 0 {
		if err := s.postLedgerPair(ctx, tx, journal, "wallet_payout", payoutID.String(),
			accountCode, LedgerAccountXegoPayable, "NGN", in.Description+" NIP fee",
			"system", in.FeeKobo, nil); err != nil {
			return WalletPayout{}, err
		}
	}
	return payout, tx.Commit(ctx)
}

// WalletPayoutByID returns one cash-out row.
func (s *Store) WalletPayoutByID(ctx context.Context, payoutID uuid.UUID) (WalletPayout, error) {
	return scanWalletPayout(s.pool.QueryRow(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE id=$1`, payoutID))
}

// WalletPayoutByReservationRef returns the cash-out created for a reservation
// ref, or pgx.ErrNoRows.
func (s *Store) WalletPayoutByReservationRef(ctx context.Context, ref string) (WalletPayout, error) {
	return scanWalletPayout(s.pool.QueryRow(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE reservation_ref=$1`, ref))
}

// ListQueuedWalletPayouts returns queued cash-outs, oldest first, for dispatch.
func (s *Store) ListQueuedWalletPayouts(ctx context.Context, limit int) ([]WalletPayout, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE status='queued' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WalletPayout
	for rows.Next() {
		p, err := scanWalletPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListFailedWalletPayouts returns failed cash-outs so the worker can retry
// them while attempts remain and reverse them once the budget is spent.
func (s *Store) ListFailedWalletPayouts(ctx context.Context, limit int) ([]WalletPayout, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE status='failed' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WalletPayout
	for rows.Next() {
		p, err := scanWalletPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClaimWalletPayoutForDispatch leases one queued cash-out for a provider
// attempt. pgx.ErrNoRows means another worker already claimed it.
func (s *Store) ClaimWalletPayoutForDispatch(ctx context.Context, payoutID uuid.UUID) (WalletPayout, error) {
	return scanWalletPayout(s.pool.QueryRow(ctx, `
		UPDATE wallet_payouts SET status='processing', attempts=attempts+1, updated_at=now()
		WHERE id=$1 AND status='queued'
		RETURNING `+walletPayoutCols, payoutID))
}

// CompleteWalletPayout records a confirmed transfer. The ledger was already
// posted when the cash-out was created, so completion only records the
// provider reference and the terminal status.
func (s *Store) CompleteWalletPayout(ctx context.Context, payoutID uuid.UUID, externalRef string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wallet_payouts
		SET status='completed', external_ref=$2, completed_at=now(), updated_at=now(), last_error=''
		WHERE id=$1 AND status='processing'`, payoutID, externalRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		p, err := s.WalletPayoutByID(ctx, payoutID)
		if err != nil {
			return err
		}
		if p.Status == WalletPayoutCompleted {
			return nil
		}
		return fmt.Errorf("wallet payout %s is not processing (status %s)", payoutID, p.Status)
	}
	return nil
}

// FailWalletPayout records a declined or errored provider attempt.
func (s *Store) FailWalletPayout(ctx context.Context, payoutID uuid.UUID, message string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wallet_payouts SET status='failed', last_error=$2, updated_at=now()
		WHERE id=$1 AND status='processing'`, payoutID, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("wallet payout is not processing")
	}
	return nil
}

// RetryWalletPayout re-queues a failed cash-out while attempts remain.
func (s *Store) RetryWalletPayout(ctx context.Context, payoutID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wallet_payouts SET status='queued', last_error='', updated_at=now()
		WHERE id=$1 AND status='failed' AND attempts < $2`, payoutID, PayoutMaxAttempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("wallet payout cannot be retried")
	}
	return nil
}

// ReverseWalletPayout returns a failed (or stuck processing) cash-out to the
// customer's wallet and marks it reversed. Both debits taken at creation are
// reversed under a distinct journal ref, so the original posting stays
// append-only. The money-out allowance reservation is released: the movement
// was cancelled, not held.
//
// Idempotent: a reversed payout is left untouched.
func (s *Store) ReverseWalletPayout(ctx context.Context, payoutID uuid.UUID, reason string) (WalletPayout, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WalletPayout{}, err
	}
	defer tx.Rollback(ctx)

	payout, err := scanWalletPayout(tx.QueryRow(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE id=$1 FOR UPDATE`, payoutID))
	if err != nil {
		return WalletPayout{}, err
	}
	if payout.Status == WalletPayoutReversed {
		return payout, tx.Commit(ctx)
	}
	if payout.Status != WalletPayoutFailed && payout.Status != WalletPayoutProcessing {
		return WalletPayout{}, fmt.Errorf("only a failed or processing cash-out can be reversed, %s is %s",
			payoutID, payout.Status)
	}
	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, payout.UserID, "", WalletStatusPending)
	if err != nil {
		return WalletPayout{}, err
	}
	reversalRef := payout.LedgerJournal + ":reversal"
	var posted bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, reversalRef).Scan(&posted); err != nil {
		return WalletPayout{}, err
	}
	if !posted {
		if err := s.postLedgerPair(ctx, tx, reversalRef, "wallet_payout", payoutID.String(),
			LedgerAccountOperatingBank, wallet.AccountCode, "NGN",
			"Reversal: wallet cash-out "+reason, "system", payout.PayoutKobo, nil); err != nil {
			return WalletPayout{}, err
		}
		if payout.FeeKobo > 0 {
			if err := s.postLedgerPair(ctx, tx, reversalRef, "wallet_payout", payoutID.String(),
				LedgerAccountXegoPayable, wallet.AccountCode, "NGN",
				"Reversal: wallet cash-out fee "+reason, "system", payout.FeeKobo, nil); err != nil {
				return WalletPayout{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE wallet_payouts SET status='reversed', last_error=$2, updated_at=now() WHERE id=$1`,
		payoutID, reason); err != nil {
		return WalletPayout{}, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM allowance_usage WHERE transaction_ref=$1`, payout.ReservationRef); err != nil {
		return WalletPayout{}, err
	}
	reversed, err := scanWalletPayout(tx.QueryRow(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE id=$1`, payoutID))
	if err != nil {
		return WalletPayout{}, err
	}
	return reversed, tx.Commit(ctx)
}

// RequeueStaleWalletPayouts resets cash-outs stuck in processing (crashed
// between claim and outcome) back to queued.
func (s *Store) RequeueStaleWalletPayouts(ctx context.Context, olderThan time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wallet_payouts SET status='queued', updated_at=now()
		WHERE status='processing' AND updated_at <= now()-$1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// WalletPayoutsByUser returns a user's cash-outs, newest first.
func (s *Store) WalletPayoutsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]WalletPayout, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+walletPayoutCols+` FROM wallet_payouts WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WalletPayout
	for rows.Next() {
		p, err := scanWalletPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
