package store

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
)

// TestWalletLifecycle covers W1 end to end: a pending individual wallet is
// opened at the L0 user-creation milestone and activated when the identity
// ladder reaches L1; a business wallet is integrated on KYB verification;
// credit/withdraw movements post balanced double-entry pairs keyed by their
// journal refs; money-out is gated on an active wallet and sufficient balance;
// and the ledger chain stays sound throughout.
func TestWalletLifecycle(t *testing.T) {
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
		TRUNCATE wallet_accounts, ledger_entries, allowance_usage, kyc_profiles,
		         business_kyb_profiles, users, merchants, payments, payment_events
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	// User creation (first contact) opens a pending wallet via the L0 KYC
	// profile.
	user, err := repository.GetOrCreateUser(ctx, "+2348012349001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Status != WalletStatusPending {
		t.Fatalf("fresh user wallet should be pending, got %s", wallet.Status)
	}
	if wallet.AccountCode != LedgerAccountUserWallet+":"+user.ID.String() {
		t.Fatalf("unexpected user wallet account code %q", wallet.AccountCode)
	}
	// Ensure is idempotent.
	again, err := repository.EnsureUserWallet(ctx, user.ID, "")
	if err != nil || again.ID != wallet.ID {
		t.Fatalf("EnsureUserWallet should be idempotent: id=%s err=%v", again.ID, err)
	}

	// Receiving is allowed on any status and is idempotent per journal ref.
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 500_000, "wallet-test:credit-1", "test credit"); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 500_000, "wallet-test:credit-1", "test credit replay"); err != nil {
		t.Fatal(err)
	}
	balance, err := repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 500_000 {
		t.Fatalf("wallet balance after credit = %d, want 500000", balance)
	}

	// Money-out is blocked while the wallet is pending (L0).
	if err := repository.WithdrawFromUserWallet(ctx, user.ID, 100_000, "wallet-test:withdraw-pending", "withdraw"); err == nil {
		t.Fatal("money-out from a pending wallet must fail")
	}

	// Reaching L1 activates the wallet.
	profile, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Tier != kyc.TierL1 {
		t.Fatalf("expected L1, got %s", profile.Tier)
	}
	wallet, err = repository.WalletByOwner(ctx, WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Status != WalletStatusActive {
		t.Fatalf("wallet should be active after L1, got %s", wallet.Status)
	}

	// Withdrawals enforce the balance.
	if err := repository.WithdrawFromUserWallet(ctx, user.ID, 600_000, "wallet-test:withdraw-too-much", "withdraw"); err == nil {
		t.Fatal("withdrawal above the balance must fail")
	}
	if err := repository.WithdrawFromUserWallet(ctx, user.ID, 200_000, "wallet-test:withdraw-1", "withdraw"); err != nil {
		t.Fatal(err)
	}
	// Replay of the same journal ref is a no-op.
	if err := repository.WithdrawFromUserWallet(ctx, user.ID, 200_000, "wallet-test:withdraw-1", "withdraw replay"); err != nil {
		t.Fatal(err)
	}
	balance, err = repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 300_000 {
		t.Fatalf("wallet balance after withdrawal = %d, want 300000", balance)
	}

	// Business wallet is integrated on KYB verification.
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	kybProfile, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, []string{kyc.EvBusinessDocs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kybProfile.Tier != kyc.TierB1 {
		t.Fatalf("expected B1, got %s", kybProfile.Tier)
	}
	businessWallet, err := repository.WalletByOwner(ctx, WalletOwnerBusiness, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if businessWallet.Status != WalletStatusActive {
		t.Fatalf("business wallet should be active on KYB verification, got %s", businessWallet.Status)
	}
	if businessWallet.AccountCode != LedgerAccountBusinessWallet+":"+merchant.ID.String() {
		t.Fatalf("unexpected business wallet account code %q", businessWallet.AccountCode)
	}

	// The ledger chain and double-entry balance stay sound after wallet postings.
	count, broken, err := repository.VerifyLedgerChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken != -1 {
		t.Fatalf("ledger hash chain broken at entry %d of %d", broken, count)
	}
	if _, balanced, err := repository.VerifyDoubleEntry(ctx); err != nil || !balanced {
		t.Fatalf("ledger must balance to zero: balanced=%v err=%v", balanced, err)
	}

	// Frozen wallets cannot withdraw (defensive operator control).
	if _, err := repository.pool.Exec(ctx, `
		UPDATE wallet_accounts SET status='frozen' WHERE id=$1`, wallet.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.WithdrawFromUserWallet(ctx, user.ID, 10_000, "wallet-test:withdraw-frozen", "withdraw"); err == nil {
		t.Fatal("money-out from a frozen wallet must fail")
	}
}

// TestWalletPaymentAndRefund covers paying a merchant collection from the
// payer's wallet (W1 provider 'wallet'): the money-in posting debits the
// wallet instead of the operating bank, insufficient balance fails the whole
// confirmation atomically, and a refund returns the money to the wallet via
// the ledger reversal exactly once (no double credit).
func TestWalletPaymentAndRefund(t *testing.T) {
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
		TRUNCATE wallet_accounts, ledger_entries, allowance_usage, kyc_profiles,
		         refunds, payment_events, business_event_outbox, message_outbox,
		         payments, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012349002")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	// Activate the wallet (L1) and fund it.
	if _, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 500_000, "wallet-test:fund", "fund wallet"); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	createWalletPayment := func(reference string, amount int64) uuid.UUID {
		t.Helper()
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: amount,
			Currency: "NGN", Status: domain.StatusDraft, Provider: ProviderWallet,
			ProviderReference: reference, ReceiptToken: "tok-" + reference,
		})
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
			t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
		}
		return payment.ID
	}

	// Overdraft: the wallet holds 500k, so a 600k wallet payment must fail
	// atomically without moving money or changing the payment status.
	overdraft := createWalletPayment("ref-wallet-over", 600_000)
	if _, err := repository.TransitionPaymentWithOutbox(ctx, overdraft, domain.StatusSucceeded, "test", nil, OutboxSpec{Channel: ChannelAPI}); err == nil {
		t.Fatal("wallet payment above the balance must fail")
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 500_000 {
		t.Fatalf("wallet balance after overdraft attempt = %d, want 500000", balance)
	}

	// A successful wallet payment debits the wallet into the customer float.
	paymentID := createWalletPayment("ref-wallet-pay", 120_000)
	changed, err := repository.TransitionPaymentWithOutbox(ctx, paymentID, domain.StatusSucceeded, "test", nil, OutboxSpec{Channel: ChannelAPI})
	if err != nil || !changed {
		t.Fatalf("wallet payment succeed: changed=%v err=%v", changed, err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 380_000 {
		t.Fatalf("wallet balance after payment = %d, want 380000", balance)
	}
	var walletDebit bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM ledger_entries
			WHERE journal_ref=$1 AND entry_type='debit' AND account=$2)`, paymentID.String(), wallet.AccountCode).Scan(&walletDebit); err != nil {
		t.Fatal(err)
	}
	if !walletDebit {
		t.Fatal("wallet payment must post a wallet debit as its money-in")
	}

	// Refund returns the money to the wallet via the ledger reversal exactly
	// once — the reversal restores the full amount, and the extra refund
	// wallet credit is skipped for wallet-funded payments.
	if _, err := repository.RefundPayment(ctx, paymentID, "test refund", "test", nil); err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 500_000 {
		t.Fatalf("wallet balance after refund = %d, want 500000 (not double-paid)", balance)
	}

	if count, broken, err := repository.VerifyLedgerChain(ctx); err != nil || broken != -1 {
		t.Fatalf("ledger chain broken at %d of %d: %v", broken, count, err)
	}
	if _, balanced, err := repository.VerifyDoubleEntry(ctx); err != nil || !balanced {
		t.Fatalf("ledger must balance: balanced=%v err=%v", balanced, err)
	}

	// Reconciliation accepts the wallet debit as the money-in posting.
	run, items, err := repository.RunReconciliation(ctx, "auto", "test")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "clean" {
		t.Fatalf("expected clean reconciliation, got %s with %v", run.Status, items)
	}
}

// TestWalletTopup covers funding a wallet ("send money to wallet"): the
// gateway payment's money-in posts as usual, the wallet_topup hook credits the
// payer's wallet under the payment journal (replay-safe), and a refund unwinds
// the top-up via the reversal alone — the wallet is never double-credited.
func TestWalletTopup(t *testing.T) {
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
		TRUNCATE wallet_accounts, ledger_entries, allowance_usage, kyc_profiles,
		         refunds, payment_events, business_event_outbox, message_outbox,
		         payments, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012349003")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 0 {
		t.Fatalf("wallet should start empty, balance = %d", balance)
	}

	topupMerchant, err := repository.WalletTopupSystemMerchant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: topupMerchant.ID, AmountKobo: 100_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "interswitch",
		ProviderReference: "ref-topup-1", ReceiptToken: "tok-ref-topup-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{Channel: ChannelAPI}); err != nil || !changed {
		t.Fatalf("succeed top-up payment: changed=%v err=%v", changed, err)
	}

	// The wallet_topup hook credits the full amount from the customer float.
	if err := repository.ApplyWalletTopup(ctx, payment.ID, user.ID, 100_000); err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 100_000 {
		t.Fatalf("wallet balance after top-up = %d, want 100000", balance)
	}
	// Replays (retry worker) are no-ops.
	if err := repository.ApplyWalletTopup(ctx, payment.ID, user.ID, 100_000); err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 100_000 {
		t.Fatalf("wallet balance after top-up replay = %d, want 100000", balance)
	}

	// Refunding the top-up unwinds both the money-in and the wallet credit
	// through the payment-journal reversal; the extra refund credit is skipped,
	// so the wallet returns to zero exactly once.
	if _, err := repository.RefundPayment(ctx, payment.ID, "test top-up refund", "test", nil); err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 0 {
		t.Fatalf("wallet balance after top-up refund = %d, want 0 (no double credit)", balance)
	}

	// A hook retry that fires after the refund (inline attempt failed, refund
	// processed, worker drained it late) must no-op: the payment is no longer
	// succeeded, so the credit would otherwise double-pay the customer.
	if err := repository.ApplyWalletTopup(ctx, payment.ID, user.ID, 100_000); err != nil {
		t.Fatal(err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 0 {
		t.Fatalf("wallet balance after post-refund hook retry = %d, want 0 (no double credit)", balance)
	}

	if count, broken, err := repository.VerifyLedgerChain(ctx); err != nil || broken != -1 {
		t.Fatalf("ledger chain broken at %d of %d: %v", broken, count, err)
	}
	if _, balanced, err := repository.VerifyDoubleEntry(ctx); err != nil || !balanced {
		t.Fatalf("ledger must balance: balanced=%v err=%v", balanced, err)
	}
}
