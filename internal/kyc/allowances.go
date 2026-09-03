package kyc

import (
	"errors"
	"fmt"
	"time"
)

// Business tiers (KYB ladder), lowest to highest. These mirror the L0-L4
// customer ladder but are keyed on merchant accounts via
// business_kyb_profiles.
const (
	TierB0 = "B0"
	TierB1 = "B1"
	TierB2 = "B2"
	TierB3 = "B3"
)

// Business evidence keys a transition must satisfy. Recorded in
// business_kyb_profiles.evidence (registered via admin KYB review).
const (
	// EvBusinessDocs means RC/CAC registration documents plus directors are
	// verified. Satisfies B0 -> B1.
	EvBusinessDocs = "business_docs_verified"
	// EvBusinessBank means the settlement bank account is verified against the
	// registered business identity. Satisfies B1 -> B2.
	EvBusinessBank = "business_bank_verified"
	// EvBusinessEDD means enhanced due diligence (beneficial owners, source of
	// funds) completed. Satisfies B2 -> B3.
	EvBusinessEDD = "business_edd_completed"
)

// ValidBusinessTier reports whether t is one of the B0-B3 tiers.
func ValidBusinessTier(t string) bool {
	switch t {
	case TierB0, TierB1, TierB2, TierB3:
		return true
	}
	return false
}

// BusinessOrder returns the numeric position of a business tier (0-3).
// Unknown tiers return -1.
func BusinessOrder(t string) int {
	switch t {
	case TierB0:
		return 0
	case TierB1:
		return 1
	case TierB2:
		return 2
	case TierB3:
		return 3
	}
	return -1
}

// CanAdvanceBusiness reports whether the B0-B3 ladder permits moving from -> to.
// Advancement must be to exactly the next tier with the required evidence.
func CanAdvanceBusiness(from, to string, evidence []string) error {
	if !ValidBusinessTier(from) || !ValidBusinessTier(to) {
		return fmt.Errorf("unknown business tier: %q -> %q", from, to)
	}
	if BusinessOrder(to) != BusinessOrder(from)+1 {
		return fmt.Errorf("transition %s -> %s is not an adjacent advancement", from, to)
	}
	switch to {
	case TierB1:
		return requireEvidence(to, evidence, EvBusinessDocs)
	case TierB2:
		return requireEvidence(to, evidence, EvBusinessBank)
	case TierB3:
		return requireEvidence(to, evidence, EvBusinessEDD)
	}
	return nil
}

// CanDowngradeBusiness reports whether moving from -> to is an allowed KYB
// downgrade (any lower tier). Callers should persist a reason to the audit log.
func CanDowngradeBusiness(from, to string) error {
	if !ValidBusinessTier(from) || !ValidBusinessTier(to) {
		return fmt.Errorf("unknown business tier: %q -> %q", from, to)
	}
	if BusinessOrder(to) >= BusinessOrder(from) {
		return fmt.Errorf("transition %s -> %s is not a downgrade", from, to)
	}
	return nil
}

// Allowance directions. "in" is money received into the platform (customer
// payments); "out" is money leaving the platform (settlement payouts and
// individual-pay disbursements).
const (
	DirIn  = "in"
	DirOut = "out"
)

// ValidDirection reports whether d is an accepted allowance direction.
func ValidDirection(d string) bool {
	return d == DirIn || d == DirOut
}

// Limits is one (account type, tier, direction) ceiling row, in kobo.
type Limits struct {
	SingleLimitKobo  int64
	DailyLimitKobo   int64
	MonthlyLimitKobo int64
}

// LimitName values reported by LimitError.
const (
	LimitSingle  = "single"
	LimitDaily   = "daily"
	LimitMonthly = "monthly"
)

// LimitError reports exactly which ceiling a prospective transaction would
// breach, so callers (conversation prompts, Partner API) can both surface the
// right copy and map it to the correct response code.
type LimitError struct {
	Limit   string
	Ceiling int64
	Used    int64
	Request int64
}

func (e *LimitError) Error() string {
	switch e.Limit {
	case LimitSingle:
		return fmt.Sprintf("amount %d exceeds the single-transaction ceiling of %d for this tier", e.Request, e.Ceiling)
	case LimitDaily:
		return fmt.Sprintf("this would bring today's total to %d, over the daily ceiling of %d (already used %d)", e.Request+e.Used, e.Ceiling, e.Used)
	case LimitMonthly:
		return fmt.Sprintf("this would bring this month's total to %d, over the monthly ceiling of %d (already used %d)", e.Request+e.Used, e.Ceiling, e.Used)
	}
	return fmt.Sprintf("allowance %s exceeded (ceiling %d)", e.Limit, e.Ceiling)
}

// ErrAllowance is wrapped around LimitError so callers can detect any
// allowance rejection with errors.Is.
var ErrAllowance = errors.New("kyc: allowance ceiling exceeded")

// Unwrap exposes ErrAllowance for errors.Is matching.
func (e *LimitError) Unwrap() error { return ErrAllowance }

// Validate checks a prospective transaction of amountKobo against the tier
// ceilings, given how much has already been applied to the current day and
// month windows (usedInDay/usedInMonth, excluding this transaction). It
// returns *LimitError, which wraps ErrAllowance, on a breach.
// The caller is responsible for resolving and locking the windows atomically.
func Validate(lim Limits, amountKobo, usedInDay, usedInMonth int64) error {
	if amountKobo <= 0 {
		return fmt.Errorf("allowance: amount must be positive")
	}
	if lim.SingleLimitKobo > 0 && amountKobo > lim.SingleLimitKobo {
		return &LimitError{Limit: LimitSingle, Ceiling: lim.SingleLimitKobo, Used: usedInDay, Request: amountKobo}
	}
	if lim.DailyLimitKobo > 0 && usedInDay+amountKobo > lim.DailyLimitKobo {
		return &LimitError{Limit: LimitDaily, Ceiling: lim.DailyLimitKobo, Used: usedInDay, Request: amountKobo}
	}
	if lim.MonthlyLimitKobo > 0 && usedInMonth+amountKobo > lim.MonthlyLimitKobo {
		return &LimitError{Limit: LimitMonthly, Ceiling: lim.MonthlyLimitKobo, Used: usedInMonth, Request: amountKobo}
	}
	return nil
}

// RemainingDaily returns how much of the daily ceiling remains after usage.
func RemainingDaily(lim Limits, usedInDay int64) int64 {
	remaining := lim.DailyLimitKobo - usedInDay
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingMonthly returns how much of the monthly ceiling remains after usage.
func RemainingMonthly(lim Limits, usedInMonth int64) int64 {
	remaining := lim.MonthlyLimitKobo - usedInMonth
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Lagos is Nigeria's operating timezone (WAT, UTC+1, no DST). All allowance
// windows roll over on the WAT calendar, matching the regulator's business day.
var Lagos = time.FixedZone("WAT", 60*60)

// DayWindowStart returns the start of the WAT day containing now.
func DayWindowStart(now time.Time) time.Time {
	l := now.In(Lagos)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, Lagos)
}

// MonthWindowStart returns the start of the WAT month containing now.
func MonthWindowStart(now time.Time) time.Time {
	l := now.In(Lagos)
	return time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, Lagos)
}
