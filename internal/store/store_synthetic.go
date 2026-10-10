package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Post-deploy synthetic checks: the append-only record of every `demo synthetic`
// run, written stage by stage so a rollout's health is auditable after the fact
// and the admin dashboard can show a pass rate rather than one boolean.
// Reads are read-only; the only writer is RecordSyntheticCheck.

// SyntheticCheck is one stage of one run.
type SyntheticCheck struct {
	ID          uuid.UUID
	RunID       uuid.UUID
	RanAt       time.Time
	Stage       string
	OK          bool
	Skipped     bool
	Detail      string
	DurationMS  int
	Source      string
	Environment string
}

// SyntheticCheckSummary aggregates the trailing window and carries the most
// recent run's stages, so the dashboard needs one round trip to render both the
// pass rate and the "what failed last time" detail.
type SyntheticCheckSummary struct {
	Window        time.Duration
	Runs          int
	OKRuns        int
	FailedRuns    int
	HasRun        bool
	LastRanAt     *time.Time
	LastOK        bool
	LastRunID     uuid.UUID
	LastSource    string
	LastStages    []SyntheticCheck
	FailingStages []string
}

// OutboxBacklog is the non-terminal outbox depth plus the age of the oldest
// row still waiting. A backlog that keeps getting older is the signal that the
// message pipeline stopped draining, which no HTTP health probe can see.
type OutboxBacklog struct {
	Pending         int64
	OldestPendingAt *time.Time
}

// RecordSyntheticCheck appends one stage result.
func (s *Store) RecordSyntheticCheck(ctx context.Context, check SyntheticCheck) error {
	durationMS := check.DurationMS
	if durationMS < 0 {
		durationMS = 0
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO synthetic_checks(id,run_id,ran_at,stage,ok,skipped,detail,duration_ms,source,environment)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		check.ID, check.RunID, check.RanAt, check.Stage, check.OK, check.Skipped,
		check.Detail, durationMS, check.Source, check.Environment)
	return err
}

// SyntheticCheckSummary reads the trailing window's pass rate plus the newest
// run's stages. A run counts as OK only when no stage of it failed (skipped
// stages neither pass nor fail a run).
func (s *Store) SyntheticCheckSummary(ctx context.Context, window time.Duration) (SyntheticCheckSummary, error) {
	summary := SyntheticCheckSummary{Window: window}
	since := time.Now().Add(-window)

	var lastRanAt *time.Time
	if err := s.pool.QueryRow(ctx, `
		WITH runs AS (
			SELECT run_id, min(ran_at) AS ran_at, bool_and(ok OR skipped) AS ok
			FROM synthetic_checks
			WHERE ran_at > $1
			GROUP BY run_id
		)
		SELECT count(*), count(*) FILTER (WHERE ok), count(*) FILTER (WHERE NOT ok), max(ran_at)
		FROM runs`, since).Scan(&summary.Runs, &summary.OKRuns, &summary.FailedRuns, &lastRanAt); err != nil {
		return summary, err
	}

	// The newest run regardless of the window: an operator needs to see the
	// last rollout's result even when it predates the window's start.
	rows, err := s.pool.Query(ctx, `
		SELECT id,run_id,ran_at,stage,ok,skipped,detail,duration_ms,source,environment
		FROM synthetic_checks
		WHERE run_id = (SELECT run_id FROM synthetic_checks ORDER BY ran_at DESC, id DESC LIMIT 1)
		ORDER BY ran_at, id`)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var c SyntheticCheck
		if err := rows.Scan(&c.ID, &c.RunID, &c.RanAt, &c.Stage, &c.OK, &c.Skipped,
			&c.Detail, &c.DurationMS, &c.Source, &c.Environment); err != nil {
			return summary, err
		}
		summary.LastStages = append(summary.LastStages, c)
	}
	if err := rows.Err(); err != nil {
		return summary, err
	}
	if len(summary.LastStages) > 0 {
		last := summary.LastStages[0]
		summary.HasRun = true
		summary.LastRanAt = &last.RanAt
		summary.LastRunID = last.RunID
		summary.LastSource = last.Source
		summary.LastOK = true
		for _, stage := range summary.LastStages {
			if !stage.OK && !stage.Skipped {
				summary.LastOK = false
				summary.FailingStages = append(summary.FailingStages, stage.Stage)
			}
		}
	}
	return summary, nil
}

// OutboxBacklog counts non-terminal outbox rows and reports the oldest one's
// age anchor. Terminal rows (sent/delivered/failed) are excluded: a failed
// message is a delivery problem the deliverability guard owns, not a backlog.
func (s *Store) OutboxBacklog(ctx context.Context) (OutboxBacklog, error) {
	var backlog OutboxBacklog
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), min(created_at)
		FROM message_outbox
		WHERE status IN ('pending','sending')`).Scan(&backlog.Pending, &backlog.OldestPendingAt)
	return backlog, err
}
