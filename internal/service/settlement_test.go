package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/store"
)

// TestSettlementServiceDispatch covers the S1 orchestration through the
// simulated rail: cutting, dispatching to a good destination, idempotent
// replay, and the fail -> reverse -> redispatch path for a declined bank.
func TestSettlementServiceDispatch(t *testing.T) {
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
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
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

	svc := NewSettlementService(repository, NewSimulatedPayoutProvider(), testLogger())

	batch, err := svc.Cut(ctx, merchant.ID, "BATCH-SVC-1")
	if err != nil {
		t.Fatal(err)
	}
	if batch.TotalKobo != 200_000_000 {
		t.Fatalf("unexpected batch total %d", batch.TotalKobo)
	}

	payout, err := svc.RequestPayout(ctx, "BATCH-SVC-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if payout.Status != store.PayoutCompleted {
		t.Fatalf("expected completed payout, got %+v", payout)
	}
	if !strings.HasPrefix(payout.ExternalRef, "SIM-PAY-") {
		t.Fatalf("unexpected external ref %q", payout.ExternalRef)
	}

	// Replaying the request returns the settled payout unchanged (idempotent).
	replay, err := svc.RequestPayout(ctx, "BATCH-SVC-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != payout.ID || replay.Status != store.PayoutCompleted {
		t.Fatalf("idempotent replay mismatch: %+v vs %+v", replay, payout)
	}

	// A declined bank (011) fails, reverses, and redispatches to the good
	// destination.
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cut(ctx, merchant.ID, "BATCH-SVC-2"); err != nil {
		t.Fatal(err)
	}
	badAccount, err := repository.CreateSettlementAccount(ctx, merchant.ID, "011", "0999999999", "Fail Co")
	if err != nil {
		t.Fatal(err)
	}
	declined, err := svc.RequestPayout(ctx, "BATCH-SVC-2", &badAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if declined.Status != store.PayoutFailed {
		t.Fatalf("expected declined payout, got %+v", declined)
	}
	if declined.LastError == "" {
		t.Fatal("expected a decline message on the failed payout")
	}
	if err := svc.Reverse(ctx, declined.ID, "wrong destination"); err != nil {
		t.Fatal(err)
	}
	settled, err := svc.RequestPayout(ctx, "BATCH-SVC-2", &goodAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != store.PayoutCompleted {
		t.Fatalf("expected completed payout after redispatch, got %+v", settled)
	}

	// DispatchPending settles anything left queued.
	if err := svc.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
}
