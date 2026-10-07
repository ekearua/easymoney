-- Pre-deploy resilience prerequisites: the admin dashboard can only alert on
-- "the backup is stale" or "WAL stopped shipping" if it knows when the last
-- successful run happened. The scheduler (deploy/backup.sh under systemd
-- timers or the compose one-shot) records a heartbeat row after each
-- successful step; failure is represented by the *absence* of a fresh row,
-- which is exactly what a staleness alert keys on.
--
--   kind = 'base'  — pg_basebackup + offsite ship succeeded (daily timer)
--   kind = 'wal'   — WAL archive sync to object storage succeeded (15-min
--                    timer; the split that makes BACKUP_RPO=1h real instead
--                    of "whenever the daily backup last ran")
--   kind = 'drill' — the full PITR restore drill replayed WAL and verified
--                    the payments relation (weekly timer)
--
-- Heartbeats are success-only on purpose: a failed run stops producing rows,
-- the age crosses the configured threshold, and the panel goes red. The
-- failure itself still shows up in the timer's journal.

CREATE TABLE IF NOT EXISTS backup_runs (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('base','wal','drill')),
    finished_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS backup_runs_kind_finished_idx
    ON backup_runs (kind, finished_at DESC);
