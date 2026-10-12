package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
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
		         wallet_accounts, kyc_profiles, users, merchants, inbound_messages
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

// TestClearAllPaymentPreferences pins the "stop remembering me" control: one
// action clears every surface and scope, so a payer does not have to know that
// their habit was remembered per surface to get rid of it.
func TestClearAllPaymentPreferences(t *testing.T) {
	repository, ctx := paymentPreferenceTestStore(t)
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 2 {
		t.Skip("needs at least two seeded active merchants")
	}
	payer, err := repository.GetOrCreateUser(ctx, "+2348012349730")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWhatsApp, merchants[0].ID, "interswitch"); err != nil {
		t.Fatal(err)
	}
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWeb, merchants[1].ID, "wallet"); err != nil {
		t.Fatal(err)
	}
	if prefs, _ := repository.PaymentPreferencesForUser(ctx, payer.ID); len(prefs) != 4 {
		t.Fatalf("expected 4 remembered rows before clearing, got %d: %+v", len(prefs), prefs)
	}

	if err := repository.ClearAllPaymentPreferences(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	prefs, err := repository.PaymentPreferencesForUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 0 {
		t.Fatalf("forgetting all left %d rows behind: %+v", len(prefs), prefs)
	}
	// The control is idempotent: clearing again is a no-op, not an error.
	if err := repository.ClearAllPaymentPreferences(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
}

// TestExportUserDataIncludesPaymentPreferences pins that the remembered rail is
// part of the data-subject inventory: the NDPR right of access has to cover the
// choices we remember about a payer, even though the table holds no card data.
func TestExportUserDataIncludesPaymentPreferences(t *testing.T) {
	repository, ctx := paymentPreferenceTestStore(t)
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) == 0 {
		t.Skip("needs a seeded active merchant")
	}
	payer, err := repository.GetOrCreateUser(ctx, "+2348012349720")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RememberPaymentRail(ctx, payer.ID, PreferenceChannelWhatsApp, merchants[0].ID, "bank_transfer"); err != nil {
		t.Fatal(err)
	}

	export, err := repository.ExportUserData(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	var prefs []map[string]any
	if err := json.Unmarshal(export.PaymentPreferences, &prefs); err != nil {
		t.Fatalf("payment_preferences export is not a JSON array: %v (%s)", err, export.PaymentPreferences)
	}
	// One default row plus the merchant-scoped row the single payment wrote.
	if len(prefs) != 2 {
		t.Fatalf("export carried %d payment preferences, want 2: %s", len(prefs), export.PaymentPreferences)
	}
	if blob := string(export.PaymentPreferences); !strings.Contains(blob, "bank_transfer") {
		t.Fatalf("export missing the remembered rail: %s", blob)
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

	// updated_at is set to the attempt's own time so the follow-up query (which
	// ages a message against the moment the attempt ended) is deterministic.
	seed := func(userID uuid.UUID, provider, status string, at time.Time, ref string) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO payments(user_id, merchant_id, amount_kobo, status, provider, provider_reference, receipt_token, created_at, updated_at)
			VALUES($1, $2, 250000, $3, $4, $5, $6, $7, $7)`,
			userID, merchant.ID, status, provider, ref, "rcpt-"+ref, at); err != nil {
			t.Fatal(err)
		}
	}

	// An inbound message is metadata only for this metric: the sender and the
	// timestamp. The payload is never read (it is sealed at rest), so a dummy
	// body is all the fixture needs.
	message := func(sender string, at time.Time, id string) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO inbound_messages(provider_message_id, sender, payload, status, received_at, processed_at)
			VALUES($1, $2, '{"fixture":true}'::jsonb, 'processed', $3, $3)`, id, sender, at); err != nil {
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

	// The returning payer messaged us five minutes after their card attempt
	// failed — inside the follow-up window, so they count once. The first-time
	// payer also messaged us, but nothing of theirs was abandoned, so they must
	// not count at all: the metric is a follow-up to a failed card attempt, not
	// a count of inbound traffic.
	message("+2348012349710", window.Add(2*time.Hour+5*time.Minute), "msg-followup")
	message("+2348012349711", window.Add(5*time.Hour), "msg-unrelated")

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
	if metrics.CardFollowUps != 1 {
		t.Errorf("follow-ups after a failed card attempt: got %d, want 1", metrics.CardFollowUps)
	}

	// The per-merchant breakdown: one merchant here, carrying every figure the
	// aggregate carries, so "which merchant is losing payers to re-entry" is
	// answerable rather than only "is anyone".
	byMerchant, err := repository.CardReentryMetricsByMerchant(ctx, window, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byMerchant) != 1 {
		t.Fatalf("expected one merchant row, got %d: %+v", len(byMerchant), byMerchant)
	}
	row := byMerchant[0]
	if row.MerchantID != merchant.ID {
		t.Errorf("merchant row is for %s, want %s", row.MerchantID, merchant.ID)
	}
	if row.Payments != 4 || row.CardAttempts != 3 || row.CardAbandoned != 2 || row.RepeatPayments != 3 {
		t.Errorf("merchant breakdown drifted: %+v", row)
	}
	if got, want := row.CardAbandonmentRate(), 2.0/3.0; got != want {
		t.Errorf("merchant abandon rate: got %v, want %v", got, want)
	}

	// A window with no payments has no merchants to report rather than an
	// empty-but-present row.
	emptyByMerchant, err := repository.CardReentryMetricsByMerchant(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyByMerchant) != 0 {
		t.Fatalf("an empty window should report no merchants, got %+v", emptyByMerchant)
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
