package store

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// TestWalletCashOut covers the wallet→bank cash-out store contract: creation
// debits the wallet for the payout principal (to the operating bank) and the
// NIP fee (to Xego's payable) in one atomic posting; the reservation ref is an
// idempotency key; a failed transfer can be reversed back into the wallet; and
// a pending wallet or insufficient balance is refused.
func TestWalletCashOut(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE wallet_payouts, wallet_accounts, ledger_entries, allowance_usage,
		         payment_screenings, kyc_profiles, business_kyb_profiles, users
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012349501")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Status != WalletStatusActive {
		t.Fatalf("wallet should be active after L1, got %s", wallet.Status)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 1_000_000, "cashout-test:credit", "fund"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual,
		SubjectID:   user.ID,
		Direction:   kyc.DirOut,
		Tier:        profile.Tier,
		AmountKobo:  300_000,
		Ref:         "wallet-payout:test-1",
	}); err != nil {
		t.Fatal(err)
	}

	payout, err := repository.CreateWalletPayout(ctx, CreateWalletPayoutInput{
		UserID:         user.ID,
		BankCode:       "044",
		BankName:       "Access Bank",
		AccountNumber:  "0123456789",
		AccountName:    "Ada Obi",
		AmountKobo:     300_000,
		PayoutKobo:     290_000,
		FeeKobo:        10_000,
		ReservationRef: "wallet-payout:test-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if payout.Status != WalletPayoutQueued {
		t.Fatalf("new cash-out status = %s, want queued", payout.Status)
	}
	if payout.LedgerJournal != "wallet-payout:"+payout.ID.String() {
		t.Fatalf("unexpected journal %q", payout.LedgerJournal)
	}

	balance, err := repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 700_000 {
		t.Fatalf("wallet balance after cash-out = %d, want 700000", balance)
	}

	// One debit to the operating bank and one to Xego's payable, each with a
	// matching credit, all under the payout journal.
	assertJournal(t, repository, payout.LedgerJournal, map[string]int64{
		wallet.AccountCode:         +300_000,
		LedgerAccountOperatingBank: -290_000,
		LedgerAccountXegoPayable:   -10_000,
	})

	// Replaying the same reservation ref returns the same row and posts nothing.
	replay, err := repository.CreateWalletPayout(ctx, CreateWalletPayoutInput{
		UserID:         user.ID,
		BankCode:       "044",
		BankName:       "Access Bank",
		AccountNumber:  "0123456789",
		AccountName:    "Ada Obi",
		AmountKobo:     300_000,
		PayoutKobo:     290_000,
		FeeKobo:        10_000,
		ReservationRef: "wallet-payout:test-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != payout.ID {
		t.Fatalf("replay created a new payout: %s != %s", replay.ID, payout.ID)
	}
	balance, _ = repository.WalletBalance(ctx, wallet.ID)
	if balance != 700_000 {
		t.Fatalf("replay moved money: balance = %d", balance)
	}

	// A provider failure is reversible: the full amount (principal + fee)
	// returns to the wallet and the allowance reservation is released.
	claim, err := repository.ClaimWalletPayoutForDispatch(ctx, payout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status != WalletPayoutProcessing || claim.Attempts != 1 {
		t.Fatalf("claim = status %s attempts %d", claim.Status, claim.Attempts)
	}
	if err := repository.FailWalletPayout(ctx, payout.ID, "bank declined"); err != nil {
		t.Fatal(err)
	}
	if err := repository.RetryWalletPayout(ctx, payout.ID); err != nil {
		t.Fatal(err)
	}
	claim, err = repository.ClaimWalletPayoutForDispatch(ctx, payout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Attempts != 2 {
		t.Fatalf("second attempt count = %d, want 2", claim.Attempts)
	}
	if err := repository.FailWalletPayout(ctx, payout.ID, "still declined"); err != nil {
		t.Fatal(err)
	}
	reversed, err := repository.ReverseWalletPayout(ctx, payout.ID, "test reversal")
	if err != nil {
		t.Fatal(err)
	}
	if reversed.Status != WalletPayoutReversed {
		t.Fatalf("reversed status = %s", reversed.Status)
	}
	balance, _ = repository.WalletBalance(ctx, wallet.ID)
	if balance != 1_000_000 {
		t.Fatalf("wallet balance after reversal = %d, want 1000000", balance)
	}
	var reservations int
	if err := repository.pool.QueryRow(ctx,
		`SELECT count(*) FROM allowance_usage WHERE transaction_ref=$1`, "wallet-payout:test-1").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("allowance reservation should be released, found %d rows", reservations)
	}

	// Reversing again is a no-op.
	if _, err := repository.ReverseWalletPayout(ctx, payout.ID, "test reversal"); err != nil {
		t.Fatalf("idempotent reversal failed: %v", err)
	}
	balance, _ = repository.WalletBalance(ctx, wallet.ID)
	if balance != 1_000_000 {
		t.Fatalf("second reversal moved money: balance = %d", balance)
	}

	// Insufficient balance is refused and posts nothing.
	_, err = repository.CreateWalletPayout(ctx, CreateWalletPayoutInput{
		UserID:         user.ID,
		BankCode:       "044",
		AccountNumber:  "0123456789",
		AccountName:    "Ada Obi",
		AmountKobo:     2_000_000,
		PayoutKobo:     1_990_000,
		FeeKobo:        10_000,
		ReservationRef: "wallet-payout:test-2",
	})
	if !errors.Is(err, ErrInsufficientWalletBalance) {
		t.Fatalf("expected insufficient balance, got %v", err)
	}
}

// assertJournal checks every account's net movement under a journal ref.
func assertJournal(t *testing.T, repository *Store, journal string, want map[string]int64) {
	t.Helper()
	rows, err := repository.pool.Query(context.Background(),
		`SELECT account, entry_type, amount_kobo FROM ledger_entries WHERE journal_ref=$1`, journal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var account, entryType string
		var amount int64
		if err := rows.Scan(&account, &entryType, &amount); err != nil {
			t.Fatal(err)
		}
		if entryType == "debit" {
			got[account] += amount
		} else {
			got[account] -= amount
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for account, wantNet := range want {
		if got[account] != wantNet {
			t.Errorf("journal %s account %s net = %d, want %d", journal, account, got[account], wantNet)
		}
	}
}

// TestWalletCashOutPendingWallet asserts money-out is refused before L1.
func TestWalletCashOutPendingWallet(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012349502")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 500_000, "cashout-test:pending-credit", "fund"); err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateWalletPayout(ctx, CreateWalletPayoutInput{
		UserID:         user.ID,
		BankCode:       "044",
		AccountNumber:  "0123456789",
		AccountName:    "Ada Obi",
		AmountKobo:     100_000,
		PayoutKobo:     90_000,
		FeeKobo:        10_000,
		ReservationRef: "wallet-payout:pending-1",
	})
	if !errors.Is(err, ErrWalletNotActive) {
		t.Fatalf("expected ErrWalletNotActive, got %v", err)
	}
	if _, err := repository.WalletPayoutByReservationRef(ctx, "wallet-payout:pending-1"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no payout row should exist, got %v", err)
	}
}
