package store

import (
	"context"
	"fmt"
	"sort"
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
	ConfirmedOutcomes     int64
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

// RuleFPStat is one detection rule's outcome history over a window: the
// labelled dispositions only (confirmed/false_positive), so the rate is
// computed on real labels, not undetermined acknowledgements.
type RuleFPStat struct {
	Rule          string
	Total         int64
	Confirmed     int64
	FalsePositive int64
	// FPPercent is false_positive / (confirmed + false_positive) * 100.
	FPPercent float64
}

// RuleFalsePositiveRates returns per-rule labelled outcomes over the window,
// for the compliance queue's tuning suggestions (the M4 feedback loop made
// actionable). Rules with no labelled outcomes in the window are omitted.
func (s *Store) RuleFalsePositiveRates(ctx context.Context, window time.Duration) ([]RuleFPStat, error) {
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	since := time.Now().UTC().Add(-window)
	rows, err := s.pool.Query(ctx, `
		SELECT rule,
		       count(*) AS total,
		       count(*) FILTER (WHERE outcome='confirmed') AS confirmed,
		       count(*) FILTER (WHERE outcome='false_positive') AS fp
		FROM transaction_alert_outcomes
		WHERE created_at >= $1
		GROUP BY rule ORDER BY rule`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleFPStat
	for rows.Next() {
		var st RuleFPStat
		if err := rows.Scan(&st.Rule, &st.Total, &st.Confirmed, &st.FalsePositive); err != nil {
			return nil, err
		}
		if labelled := st.Confirmed + st.FalsePositive; labelled > 0 {
			st.FPPercent = float64(st.FalsePositive) * 100 / float64(labelled)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// TuningThresholds configures when the compliance queue surfaces a tuning
// suggestion for a rule: its labelled false-positive share over the window
// must reach FPPercent with at least MinLabelled labelled outcomes (too few
// labels would make the rate noise).
type TuningThresholds struct {
	FPPercent   float64
	MinLabelled int64
}

// DefaultTuningThresholds is the shipped posture: suggest at ≥60% FP over
// ≥10 labelled outcomes in the window.
func DefaultTuningThresholds() TuningThresholds {
	return TuningThresholds{FPPercent: 60, MinLabelled: 10}
}

// TuningSuggestion is one rule's feedback-loop signal for the compliance
// queue: the measured false-positive share and what the reviewer might do
// about it. Suggestions never change limits themselves — thresholds stay
// config-owned so money boundaries only move on human sign-off.
type TuningSuggestion struct {
	Rule      string
	FPPercent float64
	Labelled  int64
	Severity  string // "info" or "warn" (warn at ≥80% FP)
	Message   string
}

// BuildTuningSuggestions turns per-rule outcome stats into queue-visible
// tuning suggestions, worst first. Pure function over the stats so the
// policy is unit-testable.
func BuildTuningSuggestions(stats []RuleFPStat, th TuningThresholds) []TuningSuggestion {
	if th.FPPercent <= 0 {
		th = DefaultTuningThresholds()
	}
	var out []TuningSuggestion
	for _, st := range stats {
		labelled := st.Confirmed + st.FalsePositive
		if labelled < th.MinLabelled || st.FPPercent < th.FPPercent {
			continue
		}
		severity := "info"
		if st.FPPercent >= 80 {
			severity = "warn"
		}
		out = append(out, TuningSuggestion{
			Rule:      st.Rule,
			FPPercent: st.FPPercent,
			Labelled:  labelled,
			Severity:  severity,
			Message: fmt.Sprintf(
				"%d%% of %d labelled outcomes in the last 7 days were false positives — consider raising the rule's floor or widening its window before trusting its alerts.",
				int(st.FPPercent+0.5), labelled),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FPPercent > out[j].FPPercent })
	return out
}
