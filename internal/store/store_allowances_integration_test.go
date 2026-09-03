package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
)

func openAllowanceTestRepository(t *testing.T) (*Store, context.Context) {
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
	t.Cleanup(func() { repository.Close() })
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE allowance_usage,business_kyb_profiles,kyc_profiles,users,merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return repository, ctx
}

func TestAllowanceReservationEnforcesIndividualCeilings(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090000001")
	if err != nil {
		t.Fatal(err)
	}
	reserve := func(amount int64, ref string) error {
		return repository.ReserveAllowance(ctx, AllowanceReservation{
			AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirIn,
			Tier: kyc.TierL0, AmountKobo: amount, Ref: ref,
		})
	}
	if err := reserve(1_000_000, "allow:1"); err != nil {
		t.Fatalf("in-ceiling reserve should pass: %v", err)
	}
	used, err := repository.AllowanceUsage(ctx, AccountIndividual, user.ID, kyc.DirIn, kyc.DayWindowStart(time.Now()))
	if err != nil || used != 1_000_000 {
		t.Fatalf("usage after one reserve: used=%d err=%v", used, err)
	}

	err = reserve(3_000_000, "allow:2")
	if !isAllowanceRejection(err) {
		t.Fatalf("single breach should wrap ErrAllowance: %v", err)
	}
	var le *kyc.LimitError
	if !errors.As(err, &le) || le.Limit != kyc.LimitSingle {
		t.Fatalf("expected single-limit payload, got %v", err)
	}

	err = reserve(1_500_000, "allow:3")
	if !isAllowanceRejection(err) {
		t.Fatalf("daily breach should wrap ErrAllowance: %v", err)
	}
	var dle *kyc.LimitError
	if !errors.As(err, &dle) || dle.Limit != kyc.LimitDaily {
		t.Fatalf("expected daily-limit payload, got %v", err)
	}
}

func TestAllowanceReservationIdempotentByRef(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090000002")
	if err != nil {
		t.Fatal(err)
	}
	reserve := func(amount int64, ref string) error {
		return repository.ReserveAllowance(ctx, AllowanceReservation{
			AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirIn,
			Tier: kyc.TierL0, AmountKobo: amount, Ref: ref,
		})
	}
	if err := reserve(500_000, "allow:replay"); err != nil {
		t.Fatal(err)
	}
	if err := reserve(500_000, "allow:replay"); err != nil {
		t.Fatalf("replaying the same ref must be a no-op: %v", err)
	}
	used, err := repository.AllowanceUsage(ctx, AccountIndividual, user.ID, kyc.DirIn, kyc.MonthWindowStart(time.Now()))
	if err != nil || used != 500_000 {
		t.Fatalf("replay double-counted: used=%d err=%v", used, err)
	}
	if err := repository.ReleaseAllowance(ctx, "allow:replay"); err != nil {
		t.Fatal(err)
	}
	used, err = repository.AllowanceUsage(ctx, AccountIndividual, user.ID, kyc.DirIn, kyc.MonthWindowStart(time.Now()))
	if err != nil || used != 0 {
		t.Fatalf("released usage should be zero: used=%d err=%v", used, err)
	}
	// A released intent can be applied fresh again.
	if err := reserve(500_000, "allow:replay"); err != nil {
		t.Fatalf("fresh application after release should pass: %v", err)
	}
}

func TestAllowanceMonthlyCeilingStepsAboveDaily(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090000003")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirIn,
		Tier: kyc.TierL4, AmountKobo: 4_900_000_000, Ref: "allow:m1",
	}); err != nil {
		t.Fatalf("in touch with the L4 monthly ceiling should pass: %v", err)
	}
	err = repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirIn,
		Tier: kyc.TierL4, AmountKobo: 200_000_000, Ref: "allow:m2",
	})
	var le *kyc.LimitError
	if !errors.As(err, &le) || le.Limit != kyc.LimitMonthly {
		t.Fatalf("expected a monthly-limit rejection, got %v", err)
	}
}

func TestAllowanceUsageScopedToWindow(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090000004")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirIn,
		Tier: kyc.TierL0, AmountKobo: 250_000, Ref: "allow:w1",
	}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Hour)
	after := time.Now().Add(time.Hour)
	used, err := repository.AllowanceUsage(ctx, AccountIndividual, user.ID, kyc.DirIn, before)
	if err != nil || used != 250_000 {
		t.Fatalf("usage from the past: used=%d err=%v", used, err)
	}
	used, err = repository.AllowanceUsage(ctx, AccountIndividual, user.ID, kyc.DirIn, after)
	if err != nil || used != 0 {
		t.Fatalf("usage from the future must be zero: used=%d err=%v", used, err)
	}
}

func TestUpdateTierLimitValidatesAndPersists(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	limits, err := repository.ListTierLimits(ctx)
	if err != nil || len(limits) == 0 {
		t.Fatalf("no tier limits seeded: count=%d err=%v", len(limits), err)
	}
	row := limits[0]
	original := limits[0]

	if _, err := repository.UpdateTierLimit(ctx, row.ID, 500_000, 200_000, 100_000, nil); err == nil {
		t.Fatal("single > daily must be rejected")
	}
	if _, err := repository.UpdateTierLimit(ctx, row.ID, -1, 0, 0, nil); err == nil {
		t.Fatal("negative limits must be rejected")
	}
	updated, err := repository.UpdateTierLimit(ctx, row.ID, 1_500_000, 2_000_000, 10_000_000, nil)
	if err != nil {
		t.Fatalf("valid update should persist: %v", err)
	}
	if updated.SingleLimitKobo != 1_500_000 || updated.DailyLimitKobo != 2_000_000 || updated.MonthlyLimitKobo != 10_000_000 {
		t.Fatalf("update persisted wrong values: %+v", updated)
	}
	// Restore so other expectations about the CBN defaults hold.
	if _, err := repository.UpdateTierLimit(ctx, row.ID, original.SingleLimitKobo, original.DailyLimitKobo, original.MonthlyLimitKobo, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAdvanceKYBTierGating(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := repository.EnsureKYBProfile(ctx, merchant.ID)
	if err != nil || profile.Tier != kyc.TierB0 {
		t.Fatalf("fresh business profile should be B0: tier=%s err=%v", profile.Tier, err)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, nil, nil); err == nil {
		t.Fatal("B0->B1 without evidence must fail")
	}
	profile, err = repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, []string{kyc.EvBusinessDocs}, nil)
	if err != nil || profile.Tier != kyc.TierB1 {
		t.Fatalf("B0->B1 with docs should pass: tier=%s err=%v", profile.Tier, err)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB2, []string{kyc.EvBusinessBank}, nil); err == nil {
		t.Fatal("jumping from B1 to B2 while screening is blocked must fail")
	}

	if err := repository.RecordKYBScreening(ctx, merchant.ID, "blocked", []string{"BLOCKED NAME"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB2, []string{kyc.EvBusinessBank}, nil); err == nil {
		t.Fatal("advance past B0 under a blocked screening must fail")
	}
	if err := repository.RecordKYBScreening(ctx, merchant.ID, "clear", nil, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	profile, err = repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB2, []string{kyc.EvBusinessBank}, nil)
	if err != nil || profile.Tier != kyc.TierB2 {
		t.Fatalf("B1->B2 with clear screening and bank evidence should pass: tier=%s err=%v", profile.Tier, err)
	}
}

func TestRequestKYBAdvancementStagesRequest(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYBProfile(ctx, merchant.ID); err != nil {
		t.Fatal(err)
	}
	// Fresh profile: a request from B0 targets B1 and records pending review.
	profile, err := repository.RequestKYBAdvancement(ctx, merchant.ID, []string{kyc.EvBusinessDocs}, "uploaded RC and director IDs")
	if err != nil {
		t.Fatalf("request from B0 should succeed: %v", err)
	}
	if profile.AdvancementRequest == nil {
		t.Fatal("a submitted request should be present on the profile")
	}
	if profile.AdvancementRequest.RequestedTier != kyc.TierB1 {
		t.Fatalf("expected request for B1, got %s", profile.AdvancementRequest.RequestedTier)
	}
	if profile.ReviewStatus != "pending" {
		t.Fatalf("request should set review pending, got %s", profile.ReviewStatus)
	}
	if profile.AdvancementRequestedAt == nil {
		t.Fatal("requested_at should be set")
	}

	// Duplicate request for the same next tier is idempotent (replaces).
	profile, err = repository.RequestKYBAdvancement(ctx, merchant.ID, []string{kyc.EvBusinessDocs}, "updated")
	if err != nil || profile.AdvancementRequest == nil {
		t.Fatalf("re-request should replace: err=%v", err)
	}
	if profile.AdvancementRequest.Note != "updated" {
		t.Fatalf("expected request note to be replaced, got %q", profile.AdvancementRequest.Note)
	}

	// Clearing removes the request and resets review status.
	profile, err = repository.RequestKYBAdvancement(ctx, merchant.ID, nil, "")
	if err != nil {
		t.Fatalf("clearing should succeed: %v", err)
	}
	if profile.AdvancementRequest != nil || profile.ReviewStatus != "none" {
		t.Fatalf("clear should remove request and reset review: %+v", profile)
	}

	// After advancing to B1, a request targets B2.
	if err := repository.RecordKYBScreening(ctx, merchant.ID, "clear", nil, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, []string{kyc.EvBusinessDocs}, nil); err != nil {
		t.Fatalf("advance to B1 should pass: %v", err)
	}
	profile, err = repository.RequestKYBAdvancement(ctx, merchant.ID, []string{kyc.EvBusinessBank}, "")
	if err != nil {
		t.Fatalf("request from B1 should succeed: %v", err)
	}
	if profile.AdvancementRequest.RequestedTier != kyc.TierB2 {
		t.Fatalf("expected request for B2, got %s", profile.AdvancementRequest.RequestedTier)
	}

	// Approving the request via AdvanceKYBTier clears it and marks approved.
	profile, err = repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB2, []string{kyc.EvBusinessBank}, nil)
	if err != nil {
		t.Fatalf("approving request via advance should pass: %v", err)
	}
	if profile.AdvancementRequest != nil {
		t.Fatal("approved advancement should clear the request")
	}
	if profile.ReviewStatus != "approved" {
		t.Fatalf("advanced tier should be marked approved, got %s", profile.ReviewStatus)
	}
}

func TestBusinessKYBRescreenWindow(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYBProfile(ctx, merchant.ID); err != nil {
		t.Fatal(err)
	}
	// Clear the migration backfill window so the profile is due immediately.
	if _, err := repository.pool.Exec(ctx,
		`UPDATE business_kyb_profiles SET rescreen_due=NULL,last_screen_at=NULL WHERE merchant_id=$1`, merchant.ID); err != nil {
		t.Fatal(err)
	}
	due, err := repository.BusinessKYBProfilesDueForRescreen(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(due) == 0 {
		t.Fatalf("profile with no rescreen window should be due: count=%d err=%v", len(due), err)
	}
	found := false
	for _, p := range due {
		if p.MerchantID == merchant.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected the merchant in the due list")
	}
	if err := repository.RecordKYBScreening(ctx, merchant.ID, "clear", nil, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	due, err = repository.BusinessKYBProfilesDueForRescreen(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range due {
		if p.MerchantID == merchant.ID {
			t.Fatal("freshly screened profile must not be due for rescreen")
		}
	}
}

func isAllowanceRejection(err error) bool {
	return err != nil && errors.Is(err, kyc.ErrAllowance)
}
