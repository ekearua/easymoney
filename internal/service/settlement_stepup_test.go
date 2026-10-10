package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// TestSettlementStepUpGate covers the G13 band action end to end: a payout
// owned by a high-risk profile is refused with ErrStepUpRequired until a fresh
// step-up grant is recorded, and then the gate clears.
func TestSettlementStepUpGate(t *testing.T) {
	repository := stepUpTestRepository(t)
	ctx := context.Background()
	merchant := stepUpTestMerchant(t, ctx, repository)
	stepUpSetOwnerBand(t, ctx, repository, merchant.ID, "high")

	svc := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0,
		WithStepUpReauth(true, "high"))

	// High band, no grant: refused before any payout row is created.
	if _, err := svc.RequestPayout(ctx, "BATCH-STEPUP-1", nil); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("expected ErrStepUpRequired, got %v", err)
	}

	// A grant clears the gate; the request then reaches the allowance path
	// (which may still refuse, but no longer for step-up).
	if err := repository.RecordStepUpGrant(ctx, store.StepUpScopeMerchant, merchant.ID,
		store.StepUpActionMerchantPayout, store.MFAKindTOTP, time.Minute); err != nil {
		t.Fatal(err)
	}
	granted, err := repository.HasStepUpGrant(ctx, store.StepUpScopeMerchant, merchant.ID,
		store.StepUpActionMerchantPayout, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !granted {
		t.Fatal("expected the recorded grant to be visible")
	}
	if _, err := svc.RequestPayout(ctx, "BATCH-STEPUP-1", nil); errors.Is(err, ErrStepUpRequired) {
		t.Fatal("grant did not clear the step-up gate")
	}
}

// TestSettlementStepUpBypassedForLowerBand shows the gate is band-scoped: a
// merchant whose owner is not in the trigger band settles without a grant.
func TestSettlementStepUpBypassedForLowerBand(t *testing.T) {
	repository := stepUpTestRepository(t)
	ctx := context.Background()
	merchant := stepUpTestMerchant(t, ctx, repository)
	stepUpSetOwnerBand(t, ctx, repository, merchant.ID, "low")

	svc := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0,
		WithStepUpReauth(true, "high"))
	if _, err := svc.RequestPayout(ctx, "BATCH-STEPUP-1", nil); errors.Is(err, ErrStepUpRequired) {
		t.Fatal("low-band owner must not trigger step-up")
	}
}

func stepUpTestRepository(t *testing.T) *store.Store {
	t.Helper()
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
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return repository
}

func stepUpTestMerchant(t *testing.T, ctx context.Context, repository *store.Store) store.Merchant {
	t.Helper()
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012348801")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSettlementAccount(ctx, merchant.ID, "044", "0123456789", "Lagos Lunchbox Ltd"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSettlementService(repository, stubPayoutProvider{now: time.Now}, testLogger(), 0).
		Cut(ctx, merchant.ID, "BATCH-STEPUP-1"); err != nil {
		t.Fatal(err)
	}
	return merchant
}

// stepUpSetOwnerBand seeds a risk band on the merchant's primary owner, the
// same attribution the payout step-up reads.
func stepUpSetOwnerBand(t *testing.T, ctx context.Context, repository *store.Store, merchantID uuid.UUID, band string) {
	t.Helper()
	ownerID, err := repository.MerchantOwnerID(ctx, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RawExec(ctx,
		fmt.Sprintf(`UPDATE kyc_profiles SET risk_band='%s' WHERE user_id='%s'`, band, ownerID.String())); err != nil {
		t.Fatal(err)
	}
}
