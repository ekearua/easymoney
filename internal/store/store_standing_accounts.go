package store

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-payment-demo/internal/redact"
)

// StandingAccount is the reusable payer-facing bank account reserved for a
// user at Level 1. The account number is reserved locally (9029-*) even before
// an upstream standing virtual account provider is provisioned, so a user can
// always be paid by bank transfer.
type StandingAccount struct {
	UserID               uuid.UUID
	AccountNumber        string
	AccountName          string
	BankName             string
	BankCode             string
	Provider             string
	ProviderReference    string
	ProviderWalletID     string
	ProviderWalletRecordID int64
	AccountPayableCode   string
	ProviderWalletStatus string
	WalletCreationStatus string
	ProvisioningStatus   string
	ProvisioningError    string
	ReservedAt           time.Time
	ProvisionedAt        *time.Time
	UpdatedAt            time.Time
}

const standingAccountColumns = `user_id,account_number,account_name,bank_name,bank_code,provider,provider_reference,
	provider_wallet_id,provider_wallet_record_id,account_payable_code,provider_wallet_status,wallet_creation_status,
	provisioning_status,provisioning_error,reserved_at,provisioned_at,updated_at`

// StandingWalletProvisioning contains non-secret data returned by the
// Interswitch Merchant Wallet API. The wallet PIN must never be added here.
type StandingWalletProvisioning struct {
	WalletID             string
	ProviderWalletRecordID int64
	AccountPayableCode   string
	WalletStatus         string
	WalletCreationStatus string
	AccountNumber        string
	AccountName          string
	BankName             string
	BankCode             string
	CurrencyCode         string
}

// StandingCredit is one bank-transfer webhook awaiting provider-side wallet
// transaction verification before the local ledger can mirror the credit.
type StandingCredit struct {
	UserID            uuid.UUID
	AccountNumber     string
	ProviderReference string
	AmountKobo        int64
}

// standingAccountBase and prefix implement the deterministic local allocator.
// The reserved number always starts 9029 and the numeric suffix is derived
// from the user identifier; on collision the candidate is stepped forward at
// most a bounded number of times before the reservation fails.
const (
	standingAccountBase = 9_029_000_000
	standingAccountSpan = 1_000_000
)

// EnsureStandingAccount reserves a local standing account for the user if one
// is not already present, and returns the active reservation. Account numbers
// are assigned from the 9330000000 pool (shared with merchants) with collision
// checks against merchants and existing standing accounts.
func (s *Store) EnsureStandingAccount(ctx context.Context, userID uuid.UUID) (StandingAccount, error) {
	existing, err := s.StandingAccountByUser(ctx, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return StandingAccount{}, err
	}
	if s != nil {
		if num, err := s.NextAvailableWalletAccountNumber(ctx); err == nil {
			if got, err := s.insertStandingReservation(ctx, userID, num); err == nil {
				return got, nil
			} else if !isUniqueViolation(err) {
				return StandingAccount{}, err
			}
		}
	}
	prefix := crc32.ChecksumIEEE(userID[:])
	for step := 0; step < 100; step++ {
		candidate := standingAccountBase + int((uint64(prefix)+uint64(step))%standingAccountSpan)
		number := fmt.Sprintf("%d", candidate)
		got, err := s.insertStandingReservation(ctx, userID, number)
		if err == nil {
			return got, nil
		}
		if isUniqueViolation(err) {
			continue
		}
		return StandingAccount{}, err
	}
	return StandingAccount{}, fmt.Errorf("store: no free standing account number for user %s", userID)
}

// insertStandingReservation writes one reservation candidate, failing on
// unique-number collision so the caller can step to the next candidate.
func (s *Store) insertStandingReservation(ctx context.Context, userID uuid.UUID, number string) (StandingAccount, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO standing_accounts(user_id, account_number)
		VALUES($1,$2)`, userID, number)
	if err != nil {
		return StandingAccount{}, err
	}
	return s.StandingAccountByUser(ctx, userID)
}

// StandingAccountByUser loads the user's standing account, if reserved.
func (s *Store) StandingAccountByUser(ctx context.Context, userID uuid.UUID) (StandingAccount, error) {
	var account StandingAccount
	err := s.pool.QueryRow(ctx, `
		SELECT `+standingAccountColumns+`
		FROM standing_accounts
		WHERE user_id=$1`, userID).Scan(standingAccountDestinations(&account)...)
	return account, err
}

// StandingAccountByNumber resolves the owning user of a standing account from
// its account number, used by the bank-transfer credit ingestion path.
func (s *Store) StandingAccountByNumber(ctx context.Context, accountNumber string) (StandingAccount, error) {
	var account StandingAccount
	err := s.pool.QueryRow(ctx, `
		SELECT `+standingAccountColumns+`
		FROM standing_accounts
		WHERE account_number=$1`, accountNumber).Scan(standingAccountDestinations(&account)...)
	return account, err
}

func standingAccountDestinations(account *StandingAccount) []any {
	return []any{
		&account.UserID, &account.AccountNumber, &account.AccountName, &account.BankName, &account.BankCode,
		&account.Provider, &account.ProviderReference, &account.ProviderWalletID,
		&account.ProviderWalletRecordID, &account.AccountPayableCode, &account.ProviderWalletStatus, &account.WalletCreationStatus,
		&account.ProvisioningStatus, &account.ProvisioningError, &account.ReservedAt,
		&account.ProvisionedAt, &account.UpdatedAt,
	}
}

// ClaimStandingWalletProvisioning gives one onboarding request permission to
// call Interswitch's non-idempotent wallet-create endpoint. A request cannot
// reclaim a provisioning or unknown outcome; that requires operator review.
func (s *Store) ClaimStandingWalletProvisioning(ctx context.Context, userID uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE standing_accounts
		SET provisioning_status='provisioning',provisioning_error='',updated_at=now()
		WHERE user_id=$1 AND provisioning_status='not_started'`, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkStandingWalletProvisioningUnknown prevents an automatic duplicate create
// after a provider error with an ambiguous outcome.
func (s *Store) MarkStandingWalletProvisioningUnknown(ctx context.Context, userID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE standing_accounts
		SET provisioning_status='unknown',provisioning_error=$2,updated_at=now()
		WHERE user_id=$1 AND provisioning_status='provisioning'`,
		userID, redact.Error(reason, redact.DefaultMaxLen))
	return err
}

// CompleteStandingWalletProvisioning persists the provider wallet and its
// linked virtual account after Interswitch confirms wallet creation.
func (s *Store) CompleteStandingWalletProvisioning(ctx context.Context, userID uuid.UUID, wallet StandingWalletProvisioning) error {
	if strings.TrimSpace(wallet.WalletID) == "" || strings.TrimSpace(wallet.AccountNumber) == "" {
		return errors.New("store: provider wallet ID and virtual account number are required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE standing_accounts
		SET account_number=$2,account_name=$3,bank_name=$4,bank_code=$5,
		    provider='interswitch',provider_reference=$6,provider_wallet_id=$6,provider_wallet_record_id=$7,
		    account_payable_code=$8,provider_wallet_status=$9,wallet_creation_status=$10,
		    provisioning_status='provisioned',provisioning_error='',provisioned_at=now(),updated_at=now()
		WHERE user_id=$1 AND provisioning_status='provisioning'`,
		userID, strings.TrimSpace(wallet.AccountNumber), strings.TrimSpace(wallet.AccountName),
		strings.TrimSpace(wallet.BankName), strings.TrimSpace(wallet.BankCode), strings.TrimSpace(wallet.WalletID),
		wallet.ProviderWalletRecordID, strings.TrimSpace(wallet.AccountPayableCode), strings.TrimSpace(wallet.WalletStatus),
		strings.TrimSpace(wallet.WalletCreationStatus))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("store: standing wallet provisioning claim is no longer active")
	}
	return nil
}

// MarkStandingAccountProvisioned records the upstream provider account that
// replaced the local reservation. The local number is retired: future credits
// resolve from the provider account number.
func (s *Store) MarkStandingAccountProvisioned(ctx context.Context, userID uuid.UUID, number, accountName, bankName, provider, providerReference string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE standing_accounts
		SET account_number=$2, account_name=$3, bank_name=$4, provider=$5,
		    provider_reference=$6, provisioning_status='provisioned', provisioned_at=now(), updated_at=now()
		WHERE user_id=$1`, userID, number, accountName, bankName, provider, providerReference)
	return err
}

// RecordStandingCredit persists a bank-transfer credit notification
// idempotently by provider reference. A duplicate reference is ignored.
func (s *Store) RecordStandingCredit(ctx context.Context, userID uuid.UUID, accountNumber, providerReference string, amountKobo int64) (bool, error) {
	if userID == uuid.Nil || strings.TrimSpace(accountNumber) == "" || strings.TrimSpace(providerReference) == "" || amountKobo <= 0 {
		return false, errors.New("store: invalid standing credit")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO standing_credits(user_id, account_number, provider_reference, amount_kobo, status)
		VALUES($1,$2,$3,$4,'pending')
		ON CONFLICT(provider_reference) DO NOTHING`, userID, accountNumber, providerReference, amountKobo)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, tx.Commit(ctx)
	}
	var existing StandingCredit
	if err := tx.QueryRow(ctx, `
		SELECT user_id,account_number,provider_reference,amount_kobo
		FROM standing_credits WHERE provider_reference=$1 FOR UPDATE`, providerReference).Scan(
		&existing.UserID, &existing.AccountNumber, &existing.ProviderReference, &existing.AmountKobo); err != nil {
		return false, err
	}
	if existing.UserID != userID || existing.AccountNumber != accountNumber || existing.AmountKobo != amountKobo {
		return false, errors.New("store: standing credit reference conflicts with a different notification")
	}
	return false, tx.Commit(ctx)
}

// PendingStandingCredits returns the oldest standing credits not yet mirrored
// into the local ledger. Callers must verify each against Interswitch first
// whenever provider-wallet mode is enabled.
func (s *Store) PendingStandingCredits(ctx context.Context, limit int) ([]StandingCredit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id,account_number,provider_reference,amount_kobo
		FROM standing_credits WHERE status='pending'
		ORDER BY created_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credits []StandingCredit
	for rows.Next() {
		var credit StandingCredit
		if err := rows.Scan(&credit.UserID, &credit.AccountNumber, &credit.ProviderReference, &credit.AmountKobo); err != nil {
			return nil, err
		}
		credits = append(credits, credit)
	}
	return credits, rows.Err()
}

// ApplyStandingCredit applies a recorded credit to the user wallet exactly once
// and flips the record to 'applied' in one transaction: the wallet posting and
// the status flip commit together, so a failed posting leaves the credit
// pending and retryable instead of permanently marked applied. The debit
// side is the operating bank — the same book every other inbound rail posts
// from. It reports whether this call performed the credit; an already-applied
// reference is a no-op.
func (s *Store) ApplyStandingCredit(ctx context.Context, userID uuid.UUID, providerReference string, amountKobo int64, description string) (bool, error) {
	if amountKobo <= 0 {
		return false, errors.New("standing credit requires a positive amount")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE standing_credits
		SET status='applied'
		WHERE provider_reference=$1 AND user_id=$2 AND amount_kobo=$3 AND status='pending'`,
		providerReference, userID, amountKobo)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		var existingUser uuid.UUID
		var existingAmount int64
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT user_id,amount_kobo,status FROM standing_credits WHERE provider_reference=$1`,
			providerReference).Scan(&existingUser, &existingAmount, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, errors.New("standing credit must be recorded before it is applied")
			}
			return false, err
		}
		if existingUser != userID || existingAmount != amountKobo {
			return false, errors.New("standing credit reference does not match the recorded user and amount")
		}
		return false, nil
	}
	if err := s.creditWalletTx(ctx, tx, userID, LedgerAccountOperatingBank, amountKobo, providerReference, description); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique/primary key
// violation, so the allocator can step to the next candidate number.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
