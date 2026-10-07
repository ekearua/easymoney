package store

// Backup & WAL health (pre-deploy resilience prerequisites): the admin
// metrics panel ages the heartbeats deploy/backup.sh records in backup_runs
// (kind 'base' | 'wal' | 'drill', success-only) and cross-checks PostgreSQL's
// own archiver stats, so "the PITR pipeline is dead" is visible on a page an
// operator looks at instead of only in a timer's journal.
//
// Reads are strictly read-only; heartbeats are written by the scheduler via
// psql (see deploy/backup.sh --record), so this code never depends on the
// app's own workers being alive to report a stale backup.

import (
	"context"
	"errors"
	"time"
)

// BackupHealth is the freshest heartbeat per kind plus the archiver's own
// view of WAL activity. Nil timestamps mean "no successful run ever" —
// the oldest possible age, which every threshold treats as stale.
type BackupHealth struct {
	LastBaseBackupAt  *time.Time
	LastWALShipAt     *time.Time
	LastRestoreDrillAt *time.Time

	// Archiver stats straight from pg_stat_archiver: when the newest WAL
	// segment was last archived locally, and the last failed attempt.
	// The failure only matters when it is *newer* than the last success
	// (Postgres keeps last_failed_wal set after later successes), so the
	// caller compares the two times. ArchiverUnavailable carries the
	// error text when the view cannot be read (permissions), so the panel
	// can say "unknown" rather than pretending everything is fine.
	LastArchivedWALAt *time.Time
	LastWALFailedAt    *time.Time
	LastFailedWAL      string
	ArchiverUnavailable string
}

// BackupHealth reads the newest heartbeat per kind and the archiver stats in
// two queries. It never fails the whole panel on the archiver leg: a missing
// table pre-migration or a restricted role degrades that field, while a
// backup_runs outage (the primary signal) still returns an error the handler
// logs.
func (s *Store) BackupHealth(ctx context.Context) (BackupHealth, error) {
	var h BackupHealth

	rows, err := s.pool.Query(ctx, `
		SELECT kind, max(finished_at)
		FROM backup_runs
		GROUP BY kind`)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var at time.Time
		if err := rows.Scan(&kind, &at); err != nil {
			return h, err
		}
		switch kind {
		case "base":
			h.LastBaseBackupAt = &at
		case "wal":
			h.LastWALShipAt = &at
		case "drill":
			h.LastRestoreDrillAt = &at
		}
	}
	if err := rows.Err(); err != nil {
		return h, err
	}

	// pg_stat_archiver is advisory context, not the alert source: WAL may
	// legitimately have no recent segment on a quiet database (it only
	// archives on segment switch or archive_timeout), so the panel shows
	// it as supporting detail next to the heartbeat ages.
	var archived, failedAt *time.Time
	var failedWAL *string
	err = s.pool.QueryRow(ctx, `
		SELECT last_archived_time, last_failed_time, last_failed_wal
		FROM pg_stat_archiver`).
		Scan(&archived, &failedAt, &failedWAL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return h, err
		}
		h.ArchiverUnavailable = err.Error()
	} else {
		h.LastArchivedWALAt = archived
		h.LastWALFailedAt = failedAt
		if failedWAL != nil {
			h.LastFailedWAL = *failedWAL
		}
	}
	return h, nil
}
