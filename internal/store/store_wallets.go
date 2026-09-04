// W1: per-entity wallet accounts. A wallet is the customer/business-visible
// balance of a dedicated double-entry ledger account (2301_user_wallet:<id> /
// 3101_business_wallet:<id>). Wallet rows carry metadata and status; every
// movement is a balanced ledger posting, so a wallet never diverges from the
// append-only journal (030).
//
// Lifecycle: an individual wallet is opened pending at the L0 user-creation
// milestone (EnsureKYCProfile) and activated when the identity ladder reaches
// L1 (AdvanceKYCTier). Business wallets are opened active at KYB verification
// (AdvanceKYBTier / ReviewKYBProfile) and defensively ensured at settlement
// time. Money-in is allowed on any status (a customer is never blocked from
// receiving); money-out requires an active wallet and sufficient balance.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ProviderWallet identifies payments funded from the payer's Xego wallet
// (W1). The money-in posting for such a payment debits the payer's wallet
// account instead of the operating bank (see transitionPayment).
const ProviderWallet = "wallet"

// Wallet statuses.
const (
	WalletStatusPending = "pending" // opened at L0, awaiting L1 activation
	WalletStatusActive  = "active"  // L1+ individual / verified business
	WalletStatusFrozen  = "frozen"  // operator-frozen; blocks money-out
)

// Sentinel errors surfaced to callers (services/conversation) so they can
// render a friendly message instead of a raw DB error.
var (
	// ErrInsufficientWalletBalance means the wallet balance is below the
	// requested money-out amount.
	ErrInsufficientWalletBalance = errors.New("insufficient wallet balance")
	// ErrWalletNotActive means the wallet is pending or frozen; money-out
	// (withdrawal or wallet payment) requires an active wallet (L1+).
	ErrWalletNotActive = errors.New("wallet is not active")
)

// Wallet owner types.
const (
	WalletOwnerUser     = "user"
	WalletOwnerBusiness = "business"
)

// WalletAccount is one entity's wallet row.
type WalletAccount struct {
	ID          uuid.UUID
	OwnerType   string
	OwnerID     uuid.UUID
	AccountCode string
	AccountName string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EnsureUserWallet opens (or returns) the individual wallet for a user. New
// wallets start pending: they are created at the L0 user-creation milestone
// and activated when the identity ladder reaches L1 (AdvanceKYCTier).
func (s *Store) EnsureUserWallet(ctx context.Context, userID uuid.UUID, accountName string) (WalletAccount, error) {
	return s.ensureWallet(ctx, s.pool, WalletOwnerUser, userID, accountName, WalletStatusPending)
}

// ActivateUserWallet moves a pending individual wallet to active (L1
// activation). Idempotent: a wallet already active is left unchanged. Fails
// when the wallet does not exist.
func (s *Store) ActivateUserWallet(ctx context.Context, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wallet_accounts SET status='active', updated_at=now()
		WHERE owner_type='user' AND owner_id=$1 AND status='pending'`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM wallet_accounts WHERE owner_type='user' AND owner_id=$1)`, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("no wallet exists for user %s", userID)
		}
	}
	return nil
}

// EnsureBusinessWallet opens (or returns) the business wallet for a merchant.
// Business wallets are created active: they are integrated at KYB
// verification, and settlement defensively ensures one for every merchant.
func (s *Store) EnsureBusinessWallet(ctx context.Context, merchantID uuid.UUID, accountName string) (WalletAccount, error) {
	return s.ensureWallet(ctx, s.pool, WalletOwnerBusiness, merchantID, accountName, WalletStatusActive)
}

// ensureWallet creates the wallet row if missing (idempotent) and returns it.
// status is only applied on creation; an existing row keeps its status.
func (s *Store) ensureWallet(ctx context.Context, q walletQuerier, ownerType string, ownerID uuid.UUID, accountName, status string) (WalletAccount, error) {
	if accountName == "" {
		accountName = "Individual wallet"
		if ownerType == WalletOwnerBusiness {
			accountName = "Business wallet"
		}
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO wallet_accounts(owner_type, owner_id, account_code, account_name, status)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (owner_type, owner_id) DO NOTHING`,
		ownerType, ownerID, walletAccountCode(ownerType, ownerID), accountName, status); err != nil {
		return WalletAccount{}, err
	}
	return walletByOwnerQ(ctx, q, ownerType, ownerID)
}

// ensureWalletTx is the transactional variant used inside settlement cuts and
// KYB advancement so the wallet row and the ledger posting commit atomically.
func (s *Store) ensureWalletTx(ctx context.Context, tx pgx.Tx, ownerType string, ownerID uuid.UUID, accountName, status string) (WalletAccount, error) {
	return s.ensureWallet(ctx, tx, ownerType, ownerID, accountName, status)
}

func walletAccountCode(ownerType string, ownerID uuid.UUID) string {
	if ownerType == WalletOwnerBusiness {
		return LedgerAccountBusinessWallet + ":" + ownerID.String()
	}
	return LedgerAccountUserWallet + ":" + ownerID.String()
}

// WalletByOwner resolves an existing wallet row (pgx.ErrNoRows when absent).
func (s *Store) WalletByOwner(ctx context.Context, ownerType string, ownerID uuid.UUID) (WalletAccount, error) {
	return walletByOwnerQ(ctx, s.pool, ownerType, ownerID)
}

func walletByOwnerQ(ctx context.Context, q walletQuerier, ownerType string, ownerID uuid.UUID) (WalletAccount, error) {
	var w WalletAccount
	err := q.QueryRow(ctx, `
		SELECT id, owner_type, owner_id, account_code, account_name, status, created_at, updated_at
		FROM wallet_accounts WHERE owner_type=$1 AND owner_id=$2`,
		ownerType, ownerID).Scan(&w.ID, &w.OwnerType, &w.OwnerID, &w.AccountCode,
		&w.AccountName, &w.Status, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WalletAccount{}, pgx.ErrNoRows
	}
	return w, err
}

// WalletBalance returns the wallet's balance in kobo. A wallet is a liability
// account: credits increase the balance, debits decrease it.
func (s *Store) WalletBalance(ctx context.Context, walletID uuid.UUID) (int64, error) {
	var accountCode string
	if err := s.pool.QueryRow(ctx, `SELECT account_code FROM wallet_accounts WHERE id=$1`, walletID).Scan(&accountCode); err != nil {
		return 0, err
	}
	return walletBalanceQ(ctx, s.pool, accountCode)
}

func walletBalanceQ(ctx context.Context, q walletQuerier, accountCode string) (int64, error) {
	var balance int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN entry_type='credit' THEN amount_kobo ELSE -amount_kobo END),0)
		FROM ledger_entries WHERE account=$1`, accountCode).Scan(&balance)
	return balance, err
}

// CreditUserWallet moves money into a user's wallet from a known ledger
// account (receiving: refunds, individual-pay recipient payouts). Receiving
// is allowed on any wallet status — money owed to a customer is never blocked
// by activation state. Idempotent on journalRef.
func (s *Store) CreditUserWallet(ctx context.Context, userID uuid.UUID, fromAccount string, amountKobo int64, journalRef, description string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.creditWalletTx(ctx, tx, userID, fromAccount, amountKobo, journalRef, description); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// creditWalletTx is the transactional variant of CreditUserWallet: callers
// already holding a tx (refund, individual-pay settlement) post the wallet
// credit atomically with their domain change. The wallet is ensured (created
// pending) inside the same tx so receiving always has a target.
func (s *Store) creditWalletTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, fromAccount string, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("wallet credit requires a positive amount")
	}
	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, userID, "", WalletStatusPending)
	if err != nil {
		return err
	}
	if err := s.postWalletPairTx(ctx, tx, wallet, fromAccount, wallet.AccountCode, amountKobo, journalRef, description); err != nil {
		return err
	}
	return nil
}

// WithdrawFromUserWallet moves money from a user's wallet to the operating
// bank (the external payout rail). Money-out requires an active wallet (L1+)
// and sufficient balance. Callers reserve the money-out allowance first and
// release it on error, mirroring the payment draft pattern. Idempotent on
// journalRef: a replayed withdrawal is a no-op.
func (s *Store) WithdrawFromUserWallet(ctx context.Context, userID uuid.UUID, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("wallet withdrawal requires a positive amount")
	}
	if journalRef == "" {
		return errors.New("wallet withdrawal requires a journal reference")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, userID, "", WalletStatusPending)
	if err != nil {
		return err
	}
	// Replays short-circuit before the status/balance checks so a redelivery
	// cannot fail on state that changed after the first successful post.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, journalRef).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if wallet.Status != WalletStatusActive {
		return fmt.Errorf("%w: wallet %s is %s (reach L1 to withdraw)", ErrWalletNotActive, wallet.ID, wallet.Status)
	}
	balance, err := walletBalanceQ(ctx, tx, wallet.AccountCode)
	if err != nil {
		return err
	}
	if balance < amountKobo {
		return fmt.Errorf("%w: have %d kobo, need %d kobo", ErrInsufficientWalletBalance, balance, amountKobo)
	}
	if err := s.postLedgerPair(ctx, tx, journalRef, "wallet", wallet.ID.String(),
		wallet.AccountCode, LedgerAccountOperatingBank, "NGN", description, "system", amountKobo, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// postWalletPairTx posts a balanced pair touching a wallet account, keyed by
// journalRef (idempotent). Merchant scope is nil: wallet postings are
// platform-level (the wallet's owner is encoded in the account code).
func (s *Store) postWalletPairTx(ctx context.Context, tx pgx.Tx, wallet WalletAccount, debitAccount, creditAccount string, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("wallet posting requires a positive amount")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, journalRef).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.postLedgerPair(ctx, tx, journalRef, "wallet", wallet.ID.String(),
		debitAccount, creditAccount, "NGN", description, "system", amountKobo, nil)
}

// paymentTouchesWalletTx reports whether a payment's ledger flow moves money
// through the payer's wallet under the payment journal: wallet-funded payments
// (a wallet debit) or wallet top-ups (a wallet credit). Either way the standard
// refund reversal (keyed on the same journal) restores the wallet to its
// pre-payment position, so the caller must not credit the wallet a second time.
func (s *Store) paymentTouchesWalletTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (bool, error) {
	var touches bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM ledger_entries
			WHERE journal_ref=$1 AND account LIKE $2
		)`, paymentID.String(), LedgerAccountUserWallet+":%").Scan(&touches)
	return touches, err
}

// ApplyWalletTopup credits a wallet top-up payment's amount from the customer
// float into the payer's wallet (dr 2100 / cr 2301_user_wallet:<user>). The
// posting shares the payment's journal ref, so a refund of the top-up unwinds
// it with the money-in reversal, and the checks below make replays (retry
// worker, refunded payment, already-refunded payment) a no-op.
func (s *Store) ApplyWalletTopup(ctx context.Context, paymentID, userID uuid.UUID, amountKobo int64) error {
	if amountKobo <= 0 {
		return errors.New("wallet top-up requires a positive amount")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize against refunds: the payment row is locked FOR UPDATE, so a
	// concurrent refund either commits first (this hook then sees status
	// refunded and skips) or waits for this credit (its reversal then unwinds
	// it). Without the lock, the refund path's :refund-wallet credit (a
	// different journal ref) would be invisible to the EXISTS guard below and
	// a late retry could double-credit the wallet.
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The payment is gone; nothing left to credit.
			return tx.Commit(ctx)
		}
		return err
	}
	if status != "succeeded" {
		// Refunded or otherwise no longer succeeded: the refund path already
		// returned the money, so crediting now would pay the customer twice.
		return tx.Commit(ctx)
	}
	var posted bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM ledger_entries
			WHERE journal_ref=$1 AND account LIKE $2
		)`, paymentID.String(), LedgerAccountUserWallet+":%").Scan(&posted); err != nil {
		return err
	}
	if posted {
		return tx.Commit(ctx)
	}
	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, userID, "", WalletStatusPending)
	if err != nil {
		return err
	}
	if err := s.postLedgerPair(ctx, tx, paymentID.String(), "wallet", paymentID.String(),
		LedgerAccountCustomerFloat, wallet.AccountCode, "NGN", "Wallet top-up", "system", amountKobo, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WalletTopupSystemMerchant returns the inactive internal merchant used for
// wallet top-up payments (mirrors IndividualPaySystemMerchant).
func (s *Store) WalletTopupSystemMerchant(ctx context.Context) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants WHERE slug='xego-wallet-topup'`).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// walletQuerier is satisfied by both *pgxpool.Pool and pgx.Tx so wallet
// helpers can run standalone or inside a caller's transaction.
type walletQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
