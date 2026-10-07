package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// Backup-health heartbeats (pre-deploy resilience prerequisites): the store
// reports the newest successful run per kind, treats an empty table as "no
// heartbeat ever" (nil), and always attempts the pg_stat_archiver leg —
// reporting the stats when readable and degrading to ArchiverUnavailable
// rather than failing the whole read when they are not.
func TestBackupHealthReads(t *testing.T) { //nolint:parallel // shared scratch database
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `TRUNCATE backup_runs`); err != nil {
		t.Fatal(err)
	}

	// Empty table: every heartbeat nil (the "never ran" state the panel
	// renders as always-stale), read still succeeds.
	h, err := repository.BackupHealth(ctx)
	if err != nil {
		t.Fatalf("empty backup_runs must still read: %v", err)
	}
	if h.LastBaseBackupAt != nil || h.LastWALShipAt != nil || h.LastRestoreDrillAt != nil {
		t.Fatalf("expected no heartbeats, got %+v", h)
	}

	// Two heartbeats per kind: only the newest must win, and kinds must
	// not bleed into each other.
	insert := func(kind string, at time.Time, detail string) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO backup_runs (kind, finished_at, detail) VALUES ($1, $2, $3)`,
			kind, at, detail); err != nil {
			t.Fatal(err)
		}
	}
	baseOld := time.Now().Add(-30 * time.Hour)
	baseNew := time.Now().Add(-3 * time.Hour)
	insert("base", baseOld, "older base")
	insert("base", baseNew, "newest base")
	insert("wal", time.Now().Add(-12*time.Minute), "wal ship")
	insert("drill", time.Now().Add(-72*time.Hour), "weekly drill")

	h, err = repository.BackupHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.LastBaseBackupAt == nil || !h.LastBaseBackupAt.After(baseOld.Add(time.Minute)) {
		t.Fatalf("base heartbeat must be the newest row, got %v", h.LastBaseBackupAt)
	}
	if h.LastWALShipAt == nil {
		t.Fatal("wal heartbeat missing")
	}
	if h.LastRestoreDrillAt == nil {
		t.Fatal("drill heartbeat missing")
	}

	// pg_stat_archiver: either readable (with whatever state the scratch
	// cluster has) or explicitly unavailable — never silently ignored.
	if h.ArchiverUnavailable != "" {
		t.Logf("pg_stat_archiver not readable here (%s); unavailable path covered", h.ArchiverUnavailable)
	}

	// An unsupported kind cannot be recorded (CHECK constraint): the
	// scheduler's contract with this table is enforced by the schema.
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO backup_runs (kind, finished_at) VALUES ('nonsense', now())`); err == nil {
		t.Fatal("expected CHECK constraint to reject unknown heartbeat kind")
	}
}
