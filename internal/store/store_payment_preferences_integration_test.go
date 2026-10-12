package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These are PostgreSQL integration tests: they need TEST_DATABASE_URL pointing
// at a scratch database that this test may truncate.

func paymentPreferenceTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE payment_preferences, payments, ledger_entries, allowance_usage,
		         wallet_accounts, kyc_profiles, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return repository, ctx
}

// TestPaymentPreferenceLifecycle pins the saved-methods store contract
// (SAVED_CARD_METHODS_PLAN.md Phase 1):
//
//   - nothing is remembered for a payer we have not seen pay;
//   - a successful payment remembers the habit at that merchant *and* the
//     standing choice, and resolution prefers the merchant's;
//   - remembering is an update, not an append, so a payer who pays the same
//     merchant ten times has two rows and not twenty;
//   - preferences are per surface, so a habit formed in chat does not silently
//     follow the payer onto the browser;
//   - clearing one scope leaves the other intact;
//   - deleting the payer takes their preferences with them (the NDPA erasure
//     path deletes the users row, so the FK has to do the work).
func TestPaymentPreferenceLifecycle(t *testing.T) {
	repository, ctx := paymentPreferenceTestStore(t)
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 2 {
		t.Skip("needs at least two seeded active merchants")
	}
	first, second := merchants[0], merchants[1]
	payer, err := repository.GetOrCreateUser(ctx, "+2348012349700")
	if err != nil {
		t.Fatal(err)
	}

	// A payer we have never seen pay remembers nothing, on any surface.
	if rail, err := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID); err != nil || rail != "" {
		t.Fatalf("unseen payer should have no preference, got %q (err %v)", rail, err)
	}

	// One successful payment to the first merchant.
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID, "interswitch"); err != nil {
		t.Fatal(err)
	}
	if rail, err := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID); err != nil || rail != "interswitch" {
		t.Fatalf("merchant habit not remembered: %q (err %v)", rail, err)
	}
	// A merchant they have never paid falls back to the standing choice.
	if rail, err := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, second.ID); err != nil || rail != "interswitch" {
		t.Fatalf("default preference not applied to a new merchant: %q (err %v)", rail, err)
	}
	// Surfaces are separate: chat remembers nothing on the browser.
	if rail, err := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWeb, first.ID); err != nil || rail != "" {
		t.Fatalf("web surface should be unremembered, got %q (err %v)", rail, err)
	}

	// Paying the second merchant by bank transfer makes that merchant's habit
	// bank transfer while leaving the first merchant's card habit intact.
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWhatsApp, second.ID, "bank_transfer"); err != nil {
		t.Fatal(err)
	}
	if rail, _ := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, second.ID); rail != "bank_transfer" {
		t.Fatalf("second merchant habit not remembered, got %q", rail)
	}
	if rail, _ := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID); rail != "interswitch" {
		t.Fatalf("first merchant habit was overwritten, got %q", rail)
	}

	// Repeating the same payment updates those two rows rather than appending.
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID, "interswitch"); err != nil {
		t.Fatal(err)
	}
	prefs, err := repository.PaymentPreferencesForUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	var whatsappRows int
	for _, p := range prefs {
		if p.Channel == PreferenceChannelWhatsApp {
			whatsappRows++
		}
	}
	if whatsappRows != 3 { // 1 default + 2 merchant habits
		t.Fatalf("expected 3 chat preferences (default + two merchants), got %d: %+v", whatsappRows, prefs)
	}

	// A merchant scope with no reference is a default in disguise; the store
	// normalises it rather than writing an ambiguous row.
	if err := repository.UpsertPaymentPreference(ctx, payer.ID, PreferenceChannelWeb, PreferenceScopeMerchant, "", "wallet"); err != nil {
		t.Fatal(err)
	}
	if rail, _ := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWeb, uuid.Nil); rail != "wallet" {
		t.Fatalf("a merchant scope with no reference must resolve as the default, got %q", rail)
	}

	// Clearing the standing choice leaves the merchant habits alone.
	if err := repository.ClearPaymentPreference(ctx, payer.ID, PreferenceChannelWhatsApp, PreferenceScopeDefault, ""); err != nil {
		t.Fatal(err)
	}
	if rail, _ := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, first.ID); rail != "interswitch" {
		t.Fatalf("clearing the default removed a merchant habit, got %q", rail)
	}
	if rail, _ := repository.PaymentPreferenceFor(ctx, payer.ID, PreferenceChannelWhatsApp, second.ID); rail != "bank_transfer" {
		t.Fatalf("clearing the default removed a merchant habit, got %q", rail)
	}

	// Erasure: the users row goes, the preferences go with it.
	if _, err := repository.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, payer.ID); err != nil {
		t.Fatal(err)
	}
	prefs, err = repository.PaymentPreferencesForUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 0 {
		t.Fatalf("erasure left %d payment preferences behind: %+v", len(prefs), prefs)
	}
}

// TestCardReentryMetrics pins the Phase 0.5 measurement: the numbers that decide
// whether the saved-methods work earns its cost. They are derived from payments
// the platform already stores, so the test seeds payments directly and asserts
// each figure against a hand-counted expectation.
func TestCardReentryMetrics(t *testing.T) {
	repository, ctx := paymentPreferenceTestStore(t)
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) == 0 {
		t.Skip("needs a seeded active merchant")
	}
	merchant := merchants[0]

	// Seeding creates demo activity; the metric must be measured against a
	// known window, so start from an empty ledger.
	if _, err := repository.pool.Exec(ctx, `TRUNCATE payments RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	window := time.Now().Add(-24 * time.Hour)
	before := window.Add(-time.Hour)

	// A returning payer who paid before the window opened.
	returning, err := repository.GetOrCreateUser(ctx, "+2348012349710")
	if err != nil {
		t.Fatal(err)
	}
	// A payer whose first ever payment falls inside the window.
	fresh, err := repository.GetOrCreateUser(ctx, "+2348012349711")
	if err != nil {
		t.Fatal(err)
	}

	seed := func(userID uuid.UUID, provider, status string, at time.Time, ref string) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO payments(user_id, merchant_id, amount_kobo, status, provider, provider_reference, receipt_token, created_at)
			VALUES($1, $2, 250000, $3, $4, $5, $6, $7)`,
			userID, merchant.ID, status, provider, ref, "rcpt-"+ref, at); err != nil {
			t.Fatal(err)
		}
	}

	// Before the window: one payment from the returning payer — the only thing
	// that makes them "returning". The other payer has no history at all.
	seed(returning.ID, "interswitch", "succeeded", before, "pre-1")

	// Inside the window: 3 card attempts from the returning payer (1 succeeded,
	// 2 abandoned) and 1 wallet payment from the first-time payer.
	seed(returning.ID, "interswitch", "succeeded", window.Add(time.Hour), "in-1")
	seed(returning.ID, "interswitch", "failed", window.Add(2*time.Hour), "in-2")
	seed(returning.ID, "interswitch", "expired", window.Add(3*time.Hour), "in-3")
	seed(fresh.ID, "wallet", "succeeded", window.Add(4*time.Hour), "in-4")

	metrics, err := repository.CardReentryMetricsSince(ctx, window)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CardAttempts != 3 {
		t.Errorf("card attempts: got %d, want 3", metrics.CardAttempts)
	}
	if metrics.CardAbandoned != 2 {
		t.Errorf("abandoned card attempts: got %d, want 2 (failed + expired)", metrics.CardAbandoned)
	}
	if metrics.Payments != 4 {
		t.Errorf("payments in window: got %d, want 4", metrics.Payments)
	}
	if metrics.RepeatPayments != 3 {
		t.Errorf("repeat-payer payments: got %d, want 3 (the returning payer only)", metrics.RepeatPayments)
	}
	if metrics.RepeatPaymentPayerCount != 1 {
		t.Errorf("distinct repeat payers: got %d, want 1", metrics.RepeatPaymentPayerCount)
	}
	if got, want := metrics.CardAbandonmentRate(), 2.0/3.0; got != want {
		t.Errorf("abandonment rate: got %v, want %v", got, want)
	}
	if got, want := metrics.RepeatPaymentShare(), 3.0/4.0; got != want {
		t.Errorf("repeat-payer share: got %v, want %v", got, want)
	}

	// An empty window reads as zero rather than dividing by zero.
	empty, err := repository.CardReentryMetricsSince(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if empty.CardAttempts != 0 || empty.Payments != 0 {
		t.Fatalf("a window with no payments should be empty, got %+v", empty)
	}
	if empty.CardAbandonmentRate() != 0 || empty.RepeatPaymentShare() != 0 {
		t.Fatal("rates over an empty window must be zero, not NaN")
	}
}
