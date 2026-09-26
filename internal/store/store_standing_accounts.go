package store

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// StandingAccount is the reusable payer-facing bank account reserved for a
// user at Level 1. The account number is reserved locally (9029-*) even before
// an upstream standing virtual account provider is provisioned, so a user can
// always be paid by bank transfer.
type StandingAccount struct {
	UserID            uuid.UUID
	AccountNumber     string
	AccountName       string
	BankName          string
	Provider          string
	ProviderReference string
	ReservedAt        time.Time
	ProvisionedAt     *time.Time
	UpdatedAt         time.Time
}

// standingAccountBase and prefix implement the deterministic local allocator.
// The reserved number always starts 9029 and the numeric suffix is derived
// from the user identifier; on collision the candidate is stepped forward at
// most a bounded number of times before the reservation fails.
const (
	standingAccountBase = 9_029_000_000
	standingAccountSpan = 1_000_000
)

// EnsureStandingAccount reserves the deterministic local standing account for
// the user if one is not already present, and returns the active reservation.
// Reserved rows are never deleted once provisioned so a webhook credit can
// always resolve the owning user.
func (s *Store) EnsureStandingAccount(ctx context.Context, userID uuid.UUID) (StandingAccount, error) {
	existing, err := s.StandingAccountByUser(ctx, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return StandingAccount{}, err
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
		SELECT user_id, account_number, account_name, bank_name, provider,
		       provider_reference, reserved_at, provisioned_at, updated_at
		FROM standing_accounts
		WHERE user_id=$1`, userID).Scan(
		&account.UserID, &account.AccountNumber, &account.AccountName, &account.BankName,
		&account.Provider, &account.ProviderReference, &account.ReservedAt,
		&account.ProvisionedAt, &account.UpdatedAt)
	return account, err
}

// StandingAccountByNumber resolves the owning user of a standing account from
// its account number, used by the bank-transfer credit ingestion path.
func (s *Store) StandingAccountByNumber(ctx context.Context, accountNumber string) (StandingAccount, error) {
	var account StandingAccount
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, account_number, account_name, bank_name, provider,
		       provider_reference, reserved_at, provisioned_at, updated_at
		FROM standing_accounts
		WHERE account_number=$1`, accountNumber).Scan(
		&account.UserID, &account.AccountNumber, &account.AccountName, &account.BankName,
		&account.Provider, &account.ProviderReference, &account.ReservedAt,
		&account.ProvisionedAt, &account.UpdatedAt)
	return account, err
}

// MarkStandingAccountProvisioned records the upstream provider account that
// replaced the local reservation. The local number is retired: future credits
// resolve from the provider account number.
func (s *Store) MarkStandingAccountProvisioned(ctx context.Context, userID uuid.UUID, number, accountName, bankName, provider, providerReference string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE standing_accounts
		SET account_number=$2, account_name=$3, bank_name=$4, provider=$5,
		    provider_reference=$6, provisioned_at=now(), updated_at=now()
		WHERE user_id=$1`, userID, number, accountName, bankName, provider, providerReference)
	return err
}

// RecordStandingCredit persists a bank-transfer credit notification
// idempotently by provider reference. A duplicate reference is ignored.
func (s *Store) RecordStandingCredit(ctx context.Context, userID uuid.UUID, accountNumber, providerReference string, amountKobo int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO standing_credits(user_id, account_number, provider_reference, amount_kobo, status)
		VALUES($1,$2,$3,$4,'pending')
		ON CONFLICT(provider_reference) DO NOTHING`, userID, accountNumber, providerReference, amountKobo)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
		WHERE provider_reference=$1 AND status='pending'`, providerReference)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
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
