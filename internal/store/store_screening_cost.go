package store

import (
	"context"
	"fmt"
	"time"
)

// OnboardingScreeningCost is the aggregated view of screening and onboarding
// verification events over a window, plus an optional estimated cost per
// screening to surface a simple operations meter.
type OnboardingScreeningCost struct {
	WindowDays         int
	Since              time.Time
	ScreeningsTotal    int64 // customer screening_results rows
	ScreeningsClear    int64
	ScreeningsPossible int64
	ScreeningsStrong   int64
	ScreeningsBlocked  int64
	ScreeningResults   int64 // raw screening_results rows (parity)
	ScreeningsAM       int64 // adverse-media screenings
	PaymentScreenings  int64 // payment-time counterparty screenings
	// ScreeningCostNGN is the optional per-check estimate. EstimatedCostNGN is
	// that estimate multiplied by every check in the window (customer,
	// adverse media and payment); both stay zero when no estimate is set.
	ScreeningCostNGN int64
	EstimatedCostNGN int64
}

// OnboardingScreeningCostSummary returns a windowed rollup of screening
// events (customer screening_results, adverse-media, and payment_screenings)
// so operations can see onboarding friction and screening volume without
// pulling raw rows. costPerScreenNGN, if >0, drives the display-only cost
// estimate in the template.
func (s *Store) OnboardingScreeningCostSummary(ctx context.Context, windowDays int, costPerScreenNGN int64) (OnboardingScreeningCost, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -windowDays)
	out := OnboardingScreeningCost{
		WindowDays:       windowDays,
		Since:            since,
		ScreeningCostNGN: costPerScreenNGN,
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE decision = 'clear'),
			COUNT(*) FILTER (WHERE decision = 'possible'),
			COUNT(*) FILTER (WHERE decision = 'strong'),
			COUNT(*) FILTER (WHERE decision = 'blocked')
		FROM screening_results
		WHERE screened_at >= $1
	`, since).Scan(
		&out.ScreeningsTotal,
		&out.ScreeningsClear,
		&out.ScreeningsPossible,
		&out.ScreeningsStrong,
		&out.ScreeningsBlocked,
	)
	if err != nil {
		return out, fmt.Errorf("screening_results cost summary: %w", err)
	}
	out.ScreeningResults = out.ScreeningsTotal
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM adverse_media_screenings WHERE screened_at >= $1`, since).Scan(&out.ScreeningsAM); err != nil {
		return out, fmt.Errorf("adverse_media_screenings cost summary: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM payment_screenings WHERE created_at >= $1`, since).Scan(&out.PaymentScreenings); err != nil {
		return out, fmt.Errorf("payment_screenings cost summary: %w", err)
	}
	if costPerScreenNGN > 0 {
		checks := out.ScreeningsTotal + out.ScreeningsAM + out.PaymentScreenings
		out.EstimatedCostNGN = checks * costPerScreenNGN
	}
	return out, nil
}
