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

// TestCheckoutTransitionInPassthroughMode is a regression test for the
// ChannelCheckout outbox sentinel: resultOutbox returns
// OutboxSpec{Channel: "checkout"} with no payload for browser-initiated
// payments, and transitionPayment used to write that as a message_outbox row
// with an empty payload. In passthrough mode (no DATA_ENCRYPTION_KEY) the empty
// string is not valid jsonb, so every checkout/web-flow payment failed to
// transition at all. The transition must commit without touching the outbox.
func TestCheckoutTransitionInPassthroughMode(t *testing.T) {
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
	if err := repository.resetWithSeed(ctx); err != nil {
		t.Fatal(err)
	}
	// Passthrough mode: no data key set.

	user, err := repository.GetOrCreateUser(ctx, "+2348012349091")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 120_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "interswitch",
		ProviderReference: "ref-checkout-passthrough", Channel: ChannelCheckout,
		ReceiptToken: "tok-checkout-passthrough", Recipient: user.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
	}
	// Gateway payments hand off to the hosted page (initialized) before the
	// terminal verification, exactly like the real checkout pipeline.
	if err := repository.SetCheckout(ctx, payment.ID, "https://checkout.example/x"); err != nil {
		t.Fatal(err)
	}
	changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{Channel: ChannelCheckout})
	if err != nil {
		t.Fatalf("checkout-channel transition must commit in passthrough mode, got: %v", err)
	}
	if !changed {
		t.Fatal("checkout-channel transition must report a change")
	}
	if n := countCheckoutOutboxRows(ctx, t, repository, user.ID); n != 0 {
		t.Fatalf("checkout-channel transition wrote %d message_outbox rows, want none", n)
	}
}

// TestCheckoutTransitionInKeyedMode asserts the same contract when encryption
// at rest is enabled: the transition succeeds AND no junk outbox row is left
// behind for the flow-supplied confirmation.
func TestCheckoutTransitionInKeyedMode(t *testing.T) {
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
	if err := repository.resetWithSeed(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])

	user, err := repository.GetOrCreateUser(ctx, "+2348012349092")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 120_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "interswitch",
		ProviderReference: "ref-checkout-keyed", Channel: ChannelCheckout,
		ReceiptToken: "tok-checkout-keyed", Recipient: user.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
	}
	if err := repository.SetCheckout(ctx, payment.ID, "https://checkout.example/x"); err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{Channel: ChannelCheckout}); err != nil || !changed {
		t.Fatalf("checkout-channel transition: changed=%v err=%v", changed, err)
	}
	if n := countCheckoutOutboxRows(ctx, t, repository, user.ID); n != 0 {
		t.Fatalf("checkout-channel transition wrote %d message_outbox rows, want none", n)
	}
}

// countCheckoutOutboxRows counts message_outbox rows for the user on the
// checkout channel (the flow-supplied confirmation would otherwise double
// notify, or worse the pre-fix empty-payload row would have poisoned the queue).
func countCheckoutOutboxRows(ctx context.Context, t *testing.T, repository *Store, userID uuid.UUID) int {
	t.Helper()
	var count int
	if err := repository.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM message_outbox
		WHERE channel=$1 AND user_id=$2`,
		ChannelCheckout, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestWalletAwaitingToSucceeded is a regression test for the inline wallet
// rail: ConfirmWalletPayment drives a wallet payment straight from
// awaiting_confirmation to succeeded (there is no external "initialized" hop),
// which the domain FSM forbids for every other provider. The store must allow
// the shortcut ONLY for provider wallet, still debiting the wallet atomically.
func TestWalletAwaitingToSucceeded(t *testing.T) {
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
	if err := repository.resetWithSeed(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])

	user, err := repository.GetOrCreateUser(ctx, "+2348012349093")
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
	if wallet.Status != WalletStatusActive {
		t.Fatalf("wallet status = %q, want active", wallet.Status)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountOperatingBank, 500_000, "repro:wallet-confirm-fund", "fund"); err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	create := func(provider string) uuid.UUID {
		t.Helper()
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 120_000,
			Currency: "NGN", Status: domain.StatusDraft, Provider: provider,
			ProviderReference: "ref-" + provider + "-" + uuid.NewString()[:6], Channel: ChannelCheckout,
			ReceiptToken: "tok-" + provider + "-" + uuid.NewString()[:6], Recipient: user.WhatsAppNumber,
		})
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
			t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
		}
		return payment.ID
	}

	// Wallet: awaiting_confirmation -> succeeded is legal and debits the wallet.
	paymentID := create(ProviderWallet)
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, paymentID, domain.StatusSucceeded, "wallet", nil, OutboxSpec{Channel: ChannelCheckout}); err != nil || !changed {
		t.Fatalf("wallet awaiting->succeeded: changed=%v err=%v", changed, err)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 500_000-120_000 {
		t.Fatalf("wallet balance after inline confirm = %d, want 380000", balance)
	}

	// Any other provider must still be rejected at the same hop.
	gatewayID := create("interswitch")
	if _, err := repository.TransitionPaymentWithOutbox(ctx, gatewayID, domain.StatusSucceeded, "gateway", nil, OutboxSpec{Channel: ChannelCheckout}); err == nil {
		t.Fatal("gateway payment must NOT skip awaiting_confirmation -> succeeded")
	}
}

// resetWithSeed truncates the tables these tests touch and reloads the seed
// fixtures, mirroring the other store integration tests.
func (s *Store) resetWithSeed(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		TRUNCATE wallet_accounts, ledger_entries, allowance_usage, kyc_profiles,
		         business_kyb_profiles, users, merchants, payments, payment_events,
		         message_outbox
		RESTART IDENTITY CASCADE`); err != nil {
		return err
	}
	return s.Seed(ctx)
}