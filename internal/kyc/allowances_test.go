package kyc

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCeilings(t *testing.T) {
	lim := Limits{SingleLimitKobo: 2_000_000, DailyLimitKobo: 2_000_000, MonthlyLimitKobo: 10_000_000}
	if err := Validate(lim, 2_000_000, 0, 0); err != nil {
		t.Fatalf("exact single-limit payment should pass: %v", err)
	}
	if err := Validate(lim, 100_000, 1_500_000, 5_000_000); err != nil {
		t.Fatalf("within daily and monthly should pass: %v", err)
	}
}

func TestValidateSingleBreach(t *testing.T) {
	lim := Limits{SingleLimitKobo: 2_000_000, DailyLimitKobo: 2_000_000, MonthlyLimitKobo: 10_000_000}
	err := Validate(lim, 3_000_000, 0, 0)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T", err)
	}
	if le.Limit != LimitSingle || le.Ceiling != 2_000_000 || le.Request != 3_000_000 {
		t.Fatalf("unexpected single breach payload: %+v", le)
	}
	if !errors.Is(err, ErrAllowance) {
		t.Fatal("single breach must wrap ErrAllowance")
	}
	if err.Error() == "" {
		t.Fatal("LimitError must render a message")
	}
}

func TestValidateDailyBreach(t *testing.T) {
	lim := Limits{SingleLimitKobo: 2_000_000, DailyLimitKobo: 2_000_000, MonthlyLimitKobo: 10_000_000}
	err := Validate(lim, 1_000_000, 1_500_000, 0)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T", err)
	}
	if le.Limit != LimitDaily || le.Used != 1_500_000 {
		t.Fatalf("unexpected daily breach payload: %+v", le)
	}
}

func TestValidateMonthlyBreach(t *testing.T) {
	lim := Limits{SingleLimitKobo: 1_000_000_000, DailyLimitKobo: 1_000_000_000, MonthlyLimitKobo: 10_000_000}
	err := Validate(lim, 100_000, 0, 9_950_000)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T", err)
	}
	if le.Limit != LimitMonthly {
		t.Fatalf("expected monthly breach, got %s", le.Limit)
	}
}

func TestValidateZeroCeilingIsUnlimited(t *testing.T) {
	if err := Validate(Limits{}, 1_000_000_000_000, 9_000_000_000_000, 0); err != nil {
		t.Fatalf("zero ceilings should mean no cap: %v", err)
	}
}

func TestValidateAmountMustBePositive(t *testing.T) {
	lim := Limits{SingleLimitKobo: 2_000_000, DailyLimitKobo: 2_000_000, MonthlyLimitKobo: 10_000_000}
	if err := Validate(lim, 0, 0, 0); err == nil {
		t.Fatal("zero amount must be rejected")
	}
	if err := Validate(lim, -100, 0, 0); err == nil {
		t.Fatal("negative amount must be rejected")
	}
}

func TestCanAdvanceBusiness(t *testing.T) {
	if err := CanAdvanceBusiness(TierB0, TierB1, []string{EvBusinessDocs}); err != nil {
		t.Fatalf("B0->B1 with docs should pass: %v", err)
	}
	if err := CanAdvanceBusiness(TierB0, TierB1, nil); err == nil {
		t.Fatal("B0->B1 without evidence must fail")
	}
	if err := CanAdvanceBusiness(TierB0, TierB2, []string{EvBusinessDocs}); err == nil {
		t.Fatal("non-adjacent advancement must fail")
	}
	if err := CanAdvanceBusiness(TierB2, TierB3, []string{EvBusinessEDD}); err != nil {
		t.Fatalf("B2->B3 with EDD should pass: %v", err)
	}
	if err := CanAdvanceBusiness(TierB3, TierB3, []string{EvBusinessEDD}); err == nil {
		t.Fatal("top-of-ladder advancement must fail")
	}
}

func TestCanDowngradeBusiness(t *testing.T) {
	if err := CanDowngradeBusiness(TierB3, TierB0); err != nil {
		t.Fatalf("B3->B0 downgrade should pass: %v", err)
	}
	if err := CanDowngradeBusiness(TierB1, TierB0); err != nil {
		t.Fatalf("B1->B0 downgrade should pass: %v", err)
	}
	if err := CanDowngradeBusiness(TierB0, TierB1); err == nil {
		t.Fatal("upwards move must not be a downgrade")
	}
	if err := CanDowngradeBusiness(TierB2, TierB2); err == nil {
		t.Fatal("same-tier move must not be a downgrade")
	}
}

func TestLadderPredicates(t *testing.T) {
	if !ValidBusinessTier(TierB0) || !ValidBusinessTier(TierB3) || ValidBusinessTier("B9") {
		t.Fatal("ValidBusinessTier bounds wrong")
	}
	if BusinessOrder(TierB0) != 0 || BusinessOrder(TierB3) != 3 || BusinessOrder("BZ") != -1 {
		t.Fatal("BusinessOrder bounds wrong")
	}
	if !ValidDirection(DirIn) || !ValidDirection(DirOut) || ValidDirection("sideways") {
		t.Fatal("ValidDirection bounds wrong")
	}
}

func TestWindowsRollOnWATCaldar(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 30, 45, 0, time.UTC)
	day := DayWindowStart(now)
	month := MonthWindowStart(now)
	for _, w := range []time.Time{day, month} {
		if w.Location() != Lagos {
			t.Fatalf("window must be in WAT, got %v", w.Location())
		}
	}
	if day.Hour() != 0 || day.Minute() != 0 || day.Day() != 15 {
		t.Fatalf("day window wrong: %v", day)
	}
	if month.Day() != 1 || month.Month() != time.September || month.Year() != 2026 {
		t.Fatalf("month window wrong: %v", month)
	}
	if !day.Before(now) || !month.Before(now) {
		t.Fatal("windows must precede now")
	}
}

func TestRemainingClampsAtZero(t *testing.T) {
	lim := Limits{DailyLimitKobo: 2_000_000, MonthlyLimitKobo: 10_000_000}
	if RemainingDaily(lim, 1_200_000) != 800_000 {
		t.Fatalf("daily remaining wrong: %d", RemainingDaily(lim, 1_200_000))
	}
	if RemainingDaily(lim, 9_000_000) != 0 {
		t.Fatal("daily remaining must clamp to zero")
	}
	if RemainingMonthly(lim, 10_000_001) != 0 {
		t.Fatal("monthly remaining must clamp to zero")
	}
}
