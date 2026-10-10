package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// OnboardingScreeningCost is the aggregated view of screening and onboarding
// verification events over a window, plus an optional estimated cost per
// screening to surface a simple operations meter.
type OnboardingScreeningCost struct {
	WindowDays        int
	Since             time.Time
	ScreeningsTotal   int64
	ScreeningsClear   int64
	ScreeningsPossible int64
	ScreeningsStrong  int64
	ScreeningsBlocked int64
	ScreeningResults  int64
	ScreeningsAM      int64
	PaymentScreenings int64
	// ScreeningCostNGN is an optional estimate: when non-zero the admin
	// template shows an estimated cost figure for the screening window.
	ScreeningCostNGN int64
}

// OnboardingScreeningCostSummary returns a windowed rollup of screening
// events (customer screening_results, adverse-media, and payment_screenings)
// so operations can see onboarding friction and screening volume without
// pulling raw rows. costPerScreenNGN, if >0, is stored on the summary to
// drive the display-only estimate in the template.
func (s *Store) OnboardingScreeningCostSummary(ctx context.Context, windowDays int, costPerScreenNGN int64) (OnboardingScreeningCost, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -windowDays)
	var out OnboardingScreeningCost
	out.WindowDays = windowDays
	out.Since = since
	out.ScreeningCostNGN = costPerScreenNGN
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE sr.screened_at >= $1),
			COUNT(*) FILTER (WHERE sr.screened_at >= $1 AND sr.decision = 'clear'),
			COUNT(*) FILTER (WHERE sr.screened_at >= $1 AND sr.decision = 'possible'),
			COUNT(*) FILTER (WHERE sr.screened_at >= $1 AND sr.decision = 'strong'),
			COUNT(*) FILTER (WHERE sr.screened_at >= $1 AND sr.decision = 'blocked'),
			COUNT(*) FILTER (WHERE sr.screened_at >= $1),
			0,
			0
		FROM screening_results sr
	`, since).Scan(
		&out.ScreeningsTotal,
		&out.ScreeningsClear,
		&out.ScreeningsPossible,
		&out.ScreeningsStrong,
		&out.ScreeningsBlocked,
		&out.ScreeningResults,
		&out.ScreeningsAM,
		&out.PaymentScreenings,
	)
	if err != nil {
		return out, fmt.Errorf("screening_results cost summary: %w", err)
	}
	var am, ps int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM adverse_media_screenings WHERE screened_at >= $1`, since).Scan(&am); err != nil && err != sql.ErrNoRows {
		return out, fmt.Errorf("adverse_media_screenings cost summary: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM payment_screenings WHERE created_at >= $1`, since).Scan(&ps); err != nil && err != sql.ErrNoRows {
		return out, fmt.Errorf("payment_screenings cost summary: %w", err)
	}
	out.ScreeningsAM = am
	out.PaymentScreenings = ps
	out.ScreeningsTotal = out.ScreeningsTotal + am + ps
	return out, nil
}
