package store

import (
	"context"
	"time"
)

// TrustFabricKPIs carries the M5 intelligence metrics — the doctrine's own
// KPI set, computed from existing tables so any future scoring work has a
// labelled baseline to compare against.
type TrustFabricKPIs struct {
	// AlertFalsePositivePct is the share of terminal alert outcomes recorded
	// as false positives (M4 rows); high values mean a rule is too noisy.
	AlertFalsePositivePct float64
	// ConfirmedOutcomes and FalsePositiveOutcomes are the raw counts behind
	// the percentage (undetermined acknowledgements are excluded).
	ConfirmedOutcomes    int64
	FalsePositiveOutcomes int64
	// MedianAlertResolutionMins is the median minutes from alert raised to
	// first reviewer disposition.
	MedianAlertResolutionMins float64
	// MonitoredMovements7d and MonitoredReferences7d count the settled money
	// movements the rail-aware engine covers over the trailing week, and how
	// many distinct rail references fired alerts (signal coverage).
	MonitoredMovements7d int64
	// BandCounts is the current risk-band mix across KYC profiles.
	BandCounts map[string]int64
}

// TrustFabricKPIs computes the intelligence metrics over the trailing window.
func (s *Store) TrustFabricKPIs(ctx context.Context, window time.Duration) (TrustFabricKPIs, error) {
	var k TrustFabricKPIs
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	since := time.Now().UTC().Add(-window)

	if err := s.pool.QueryRow(ctx, `
		SELECT
		    count(*) FILTER (WHERE outcome='confirmed'),
		    count(*) FILTER (WHERE outcome='false_positive')
		FROM transaction_alert_outcomes WHERE created_at >= $1`, since).
		Scan(&k.ConfirmedOutcomes, &k.FalsePositiveOutcomes); err != nil {
		return k, err
	}
	if total := k.ConfirmedOutcomes + k.FalsePositiveOutcomes; total > 0 {
		k.AlertFalsePositivePct = float64(k.FalsePositiveOutcomes) * 100 / float64(total)
	}

	// Median alert resolution: minutes between raised and the first
	// disposition (percentile_cont over per-alert minimums).
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (
		           ORDER BY EXTRACT(EPOCH FROM (t.reviewed_at - t.created_at))/60), 0)
		FROM transaction_alerts t
		WHERE t.reviewed_at IS NOT NULL AND t.created_at >= $1`, since).
		Scan(&k.MedianAlertResolutionMins); err != nil {
		return k, err
	}

	// Signal coverage: settled movements across the monitored rails in the
	// window (payments incl. top-ups, applied standing credits, succeeded
	// refunds, completed payouts) — the inputs the engine already unions.
	if err := s.pool.QueryRow(ctx, `
		SELECT
		    (SELECT count(*) FROM payments WHERE status='succeeded' AND paid_at >= $1)
		  + (SELECT count(*) FROM standing_credits WHERE status='applied' AND created_at >= $1)
		  + (SELECT count(*) FROM refunds WHERE status='succeeded' AND COALESCE(completed_at, created_at) >= $1)
		  + (SELECT count(*) FROM payouts WHERE status='completed' AND updated_at >= $1)`,
		since).Scan(&k.MonitoredMovements7d); err != nil {
		return k, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(risk_band,'low'), count(*)
		FROM kyc_profiles GROUP BY 1`)
	if err != nil {
		return k, err
	}
	defer rows.Close()
	k.BandCounts = map[string]int64{}
	for rows.Next() {
		var band string
		var n int64
		if err := rows.Scan(&band, &n); err != nil {
			return k, err
		}
		k.BandCounts[band] = n
	}
	return k, rows.Err()
}
