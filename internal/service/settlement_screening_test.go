package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// stubPaymentScreener is the hermetic G2 vendor stand-in for service tests:
// verdicts come from the configured map (defaulting to clear), and an
// optional error simulates a provider outage.
type stubPaymentScreener struct {
	verdicts map[string]ports.PaymentScreenDecision
	err      error
	calls    int
}

func (s *stubPaymentScreener) ScreenPayment(_ context.Context, req ports.PaymentScreenRequest) (ports.PaymentScreenDecision, error) {
	s.calls++
	if s.err != nil {
		return ports.PaymentScreenDecision{}, s.err
	}
	if dec, ok := s.verdicts[req.CounterpartyName]; ok {
		return dec, nil
	}
	return ports.PaymentScreenDecision{Decision: kyc.ScreenClear, ProviderRef: "stub-clear"}, nil
}

// TestPayoutScreeningGate covers the G2 seam in the ReserveAllowance →
// dispatch window: ambiguous verdicts hold the payout with the money-out
// reservation in place, blocked verdicts interdict it, clear dispatches, a
// provider outage holds, and the admin dispositions (release / reject +
// re-settle) complete the loop.
func TestPayoutScreeningGate(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	// Screening rows and payout-keyed reservations persist across runs (they
	// are not in the shared truncate list), and this test's batch numbers are
	// fixed — so clear the G2 tables here or the recorded-decision replay
	// would short-circuit the fresh screens below.
	cleanupPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cleanupPool.Exec(ctx, `DELETE FROM payment_screenings`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanupPool.Exec(ctx, `DELETE FROM allowance_usage WHERE transaction_ref LIKE 'payout:%'`); err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	// Batches below total ₦2m; B0's single out-ceiling is ₦200k, so climb the
	// merchant to B2 exactly as the settlement suite does (documents + reviewer
	// dispositions, then the ladder), mirroring the KYB gates.
	corporateDocs := store.KYBDocuments{
		CACRegNumber:     "RC1234567",
		TIN:              "1234567890",
		BeneficialOwners: "Ada Obi 60%, Chidi Obi 40%",
		BusinessAddress:  "12 Marina Road, Lagos Island, Lagos",
		SCUMLNumber:      "SC123456",
		CACDocument:      "RC1234567 LAGOS LUNCHBOX LTD",
		TINDocument:      "TIN 1234567890 LAGOS LUNCHBOX LTD",
		AddressDocument:  "Meter 5551234, 12 Marina Road, Lagos",
	}
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, corporateDocs); err != nil {
		t.Fatal(err)
	}
	for _, key := range store.DeriveKYBEvidence(corporateDocs) {
		if _, err := repository.ReviewKYBDocument(ctx, merchant.ID, key, store.KYBDocumentVerified, "compliance@xego.test", "seeded for test"); err != nil {
			t.Fatal(err)
		}
		// G12: the corporate hard gate counts a verified document only when the
		// authenticity provider judged it genuine.
		if err := repository.SaveKYBDocumentAuthenticity(ctx, merchant.ID, key, store.KYBDocumentAuthenticity{
			Status: ports.AuthenticityGenuine, Provider: "simulated",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1,
		[]string{kyc.EvCACVerified, kyc.EvTINVerified, kyc.EvUBOCleared, kyc.EvLocationVerified, kyc.EvSCUMLSubmitted}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB2, []string{kyc.EvBusinessBank}, nil); err != nil {
		t.Fatal(err)
	}
	// The four scenarios below each move ₦2m (the shared settled-payment
	// fixture), which exhausts the B2 ₦5m daily-out default before the block
	// phase — the gate under test would never be reached. The tier-limits
	// table is admin-editable by design, so raise the ceiling like an
	// operator would for a high-volume merchant.
	if _, err := cleanupPool.Exec(ctx, `UPDATE kyc_tier_limits SET daily_limit_kobo=5000000000 WHERE account_type='business' AND tier='B2'`); err != nil {
		t.Fatal(err)
	}
	cleanupPool.Close()
	user, err := repository.GetOrCreateUser(ctx, "+2348012348802")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	goodAccount, err := repository.CreateSettlementAccount(ctx, merchant.ID, "044", "0123456789", "Lagos Lunchbox Ltd")
	if err != nil {
		t.Fatal(err)
	}
	similarAccount, err := repository.CreateSettlementAccount(ctx, merchant.ID, "044", "0123456790", "Similar Sanctioned Ltd")
	if err != nil {
		t.Fatal(err)
	}
	blockedAccount, err := repository.CreateSettlementAccount(ctx, merchant.ID, "044", "0123456791", "Watchlisted Corp")
	if err != nil {
		t.Fatal(err)
	}

	svc := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0,
		WithPaymentScreener(&stubPaymentScreener{verdicts: map[string]ports.PaymentScreenDecision{
			"Similar Sanctioned Ltd": {Decision: kyc.ScreenStrong, MatchedNames: []string{"Similar Sanctioned Ltd"}, Message: "fuzzy hit"},
			"Watchlisted Corp":       {Decision: kyc.ScreenBlocked, MatchedNames: []string{"Watchlisted Corp"}, Message: "consolidated list hit"},
		}}))

	// 1) Ambiguous verdict: the payout is created, screened, and held — with
	// the money-out reservation left in place (the hold holds the money).
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cut(ctx, merchant.ID, "BATCH-HOLD-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestPayout(ctx, "BATCH-HOLD-1", &similarAccount.ID); !errors.Is(err, store.ErrMovementHeld) {
		t.Fatalf("expected ErrMovementHeld, got %v", err)
	}
	held, err := repository.PayoutByBatchNo(ctx, "BATCH-HOLD-1")
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != store.PayoutHeld {
		t.Fatalf("payout status = %s, want held", held.Status)
	}
	if reservationCount(ctx, t, databaseURL, "payout:BATCH-HOLD-1") != 1 {
		t.Fatal("held payout must keep its allowance reservation")
	}
	scr, err := repository.PaymentScreeningByRef(ctx, store.RailMerchantPayout, "payout:BATCH-HOLD-1:"+similarAccount.ID.String())
	if err != nil || scr.Decision != kyc.ScreenStrong {
		t.Fatalf("screening row = %+v err=%v, want strong", scr, err)
	}

	// Replay of the held movement reuses the recorded decision (no re-screen)
	// and stays held.
	screener := &stubPaymentScreener{}
	svc2 := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0, WithPaymentScreener(screener))
	if _, err := svc2.RequestPayout(ctx, "BATCH-HOLD-1", &similarAccount.ID); !errors.Is(err, store.ErrMovementHeld) {
		t.Fatalf("replay of held payout: expected ErrMovementHeld, got %v", err)
	}
	if screener.calls != 0 {
		t.Fatal("replay must reuse the recorded screening, not re-call the vendor")
	}

	// 2) Release: the reviewer approves; the payout re-queues and dispatches,
	// and the recorded screening flips to manually_cleared.
	released, err := repository.ReleaseHeldPayout(ctx, held.ID, true, "compliance@xego.test", "verified beneficial owner")
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != store.PayoutQueued {
		t.Fatalf("released payout status = %s, want queued", released.Status)
	}
	final, err := svc2.RequestPayout(ctx, "BATCH-HOLD-1", &similarAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.PayoutCompleted {
		t.Fatalf("released payout final status = %s, want completed", final.Status)
	}
	scr, err = repository.PaymentScreeningByRef(ctx, store.RailMerchantPayout, "payout:BATCH-HOLD-1:"+similarAccount.ID.String())
	if err != nil || scr.Decision != kyc.ScreenManuallyCleared {
		t.Fatalf("released screening decision = %s err=%v, want manually_cleared", scr.Decision, err)
	}

	// 3) Blocked verdict: the payout is interdicted (failed outright) while
	// its reservation stays counted against the ceiling.
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cut(ctx, merchant.ID, "BATCH-BLOCK-1"); err != nil {
		t.Fatal(err)
	}
	blockErr := error(nil)
	if _, blockErr = svc.RequestPayout(ctx, "BATCH-BLOCK-1", &blockedAccount.ID); blockErr == nil || !strings.Contains(blockErr.Error(), "interdicted by counterparty screening") {
		t.Fatalf("blocked payout should fail with the interdiction error, got %v", blockErr)
	}
	blocked, err := repository.PayoutByBatchNo(ctx, "BATCH-BLOCK-1")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != store.PayoutFailed {
		t.Fatalf("interdicted payout status = %s, want failed", blocked.Status)
	}
	if reservationCount(ctx, t, databaseURL, "payout:BATCH-BLOCK-1") != 1 {
		t.Fatal("interdicted payout must keep its allowance reservation")
	}

	// 4) Provider outage: the payout is held, not failed, and no screening row
	// is recorded for the un-completed screen.
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cut(ctx, merchant.ID, "BATCH-OUTAGE-1"); err != nil {
		t.Fatal(err)
	}
	outageSvc := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0,
		WithPaymentScreener(&stubPaymentScreener{err: errors.New("vendor 503")}))
	if _, err := outageSvc.RequestPayout(ctx, "BATCH-OUTAGE-1", &goodAccount.ID); !errors.Is(err, store.ErrMovementHeld) {
		t.Fatalf("outage: expected ErrMovementHeld, got %v", err)
	}
	outage, err := repository.PayoutByBatchNo(ctx, "BATCH-OUTAGE-1")
	if err != nil {
		t.Fatal(err)
	}
	if outage.Status != store.PayoutHeld {
		t.Fatalf("outage payout status = %s, want held", outage.Status)
	}
	if _, err := repository.PaymentScreeningByRef(ctx, store.RailMerchantPayout, "payout:BATCH-OUTAGE-1:"+goodAccount.ID.String()); err == nil {
		t.Fatal("an un-completed screen must not record a screening row")
	}

	// 5) Reject + re-settle: the reviewer refuses the counterparty; the payout
	// is reversed, the reservation released, and the batch re-settles to a
	// clean destination (a fresh payout row that re-screens and dispatches).
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cut(ctx, merchant.ID, "BATCH-REJECT-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestPayout(ctx, "BATCH-REJECT-1", &similarAccount.ID); !errors.Is(err, store.ErrMovementHeld) {
		t.Fatalf("reject setup: expected ErrMovementHeld, got %v", err)
	}
	rejected, err := repository.PayoutByBatchNo(ctx, "BATCH-REJECT-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReleaseHeldPayout(ctx, rejected.ID, false, "compliance@xego.test", "counterparty refused"); err != nil {
		t.Fatal(err)
	}
	rejectedAfter, err := repository.PayoutByID(ctx, rejected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rejectedAfter.Status != store.PayoutReversed {
		t.Fatalf("rejected payout status = %s, want reversed", rejectedAfter.Status)
	}
	if reservationCount(ctx, t, databaseURL, "payout:BATCH-REJECT-1") != 0 {
		t.Fatal("rejected payout must release its allowance reservation")
	}
	reSettled, err := svc2.RequestPayout(ctx, "BATCH-REJECT-1", &goodAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reSettled.ID == rejected.ID || reSettled.Status != store.PayoutCompleted {
		t.Fatalf("re-settled payout = %+v, want a fresh completed row", reSettled)
	}
}

// reservationCount counts allowance_usage rows for one reservation ref.
func reservationCount(ctx context.Context, t *testing.T, databaseURL, ref string) int {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM allowance_usage WHERE transaction_ref=$1`, ref).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
