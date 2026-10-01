package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// G7 residual (AML spec §5): scheduled audit chain-verification with
// alerting. The sweep persists one check row per run; a clean→broken
// transition raises exactly one alert; a disposed-while-still-broken chain
// does not re-raise on later sweeps; a clean sweep marks the open alert
// recovered; the reviewer disposition records its own audited act.
func TestAuditChainScheduledVerification(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE audit_chain_checks, audit_chain_alerts`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	// Seed one audited action so the chain is non-empty and self-contained.
	if _, err := repository.AppendAuditLog(ctx, AuditLog{
		OccurredAt: now.Add(-time.Minute), ActorType: "system", Action: "test.seed",
		Details: map[string]any{"purpose": "chain verification test"},
	}); err != nil {
		t.Fatal(err)
	}

	// Clean chain: the sweep records an intact check and raises nothing.
	check1, err := repository.VerifyAuditChainScheduled(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if check1.BrokenAt != -1 || check1.AlertOpen {
		t.Fatalf("clean sweep recorded brokenAt=%d alertOpen=%v", check1.BrokenAt, check1.AlertOpen)
	}

	// Read the tail audit row (as an AuditLog) so the hash can be repaired
	// after corruption.
	last, err := repository.tailAuditRow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the tail row's hash: the chain no longer verifies. This must
	// disable the append-only trigger first — the guard rejects UPDATE —
	// which is the realistic bypass: a privileged actor mutes the guard; the
	// hash chain is what detects the tampering anyway. The trigger is
	// restored in a defer so the guarantee outlives the test.
	if _, err := repository.pool.Exec(ctx, `ALTER TABLE audit_logs DISABLE TRIGGER audit_logs_no_mutation`); err != nil {
		t.Fatal(err)
	}
	defer repository.pool.Exec(ctx, `ALTER TABLE audit_logs ENABLE TRIGGER audit_logs_no_mutation`)
	if _, err := repository.pool.Exec(ctx,
		`UPDATE audit_logs SET hash='deadbeef' WHERE id=$1`, last.ID); err != nil {
		t.Fatal(err)
	}

	// First failing sweep: alert raised (clean→broken transition).
	now = now.Add(time.Hour)
	check2, err := repository.VerifyAuditChainScheduled(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if check2.BrokenAt < 0 || !check2.AlertOpen {
		t.Fatalf("broken sweep recorded brokenAt=%d alertOpen=%v", check2.BrokenAt, check2.AlertOpen)
	}
	alert, open, err := repository.OpenAuditChainAlert(ctx)
	if err != nil || !open {
		t.Fatalf("open alert after first failing sweep: open=%v err=%v", open, err)
	}

	// Still broken, second sweep: no duplicate alert.
	now = now.Add(time.Hour)
	if _, err := repository.VerifyAuditChainScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	alerts, err := repository.ListAuditChainAlerts(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected exactly 1 alert, got %d", len(alerts))
	}

	// Reviewer disposes while still broken: allowed (acknowledge a bug).
	disposed, err := repository.DisposeAuditChainAlert(ctx, alert.ID, "mlro@faya.example", "investigating; looks like a bad backfill", false)
	if err != nil {
		t.Fatal(err)
	}
	if disposed.Status != "acknowledged_bug" {
		t.Fatalf("disposition status = %q", disposed.Status)
	}

	// Still broken, third sweep: the transition rule must not re-raise.
	now = now.Add(time.Hour)
	check3, err := repository.VerifyAuditChainScheduled(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if check3.AlertOpen {
		t.Fatal("a disposed-while-broken chain must not re-raise on the next sweep")
	}

	// Repair the row: recompute the correct hash and restore linkage. The
	// guard is still disabled at this point (restored by the defer).
	fixed := auditHash(last.PrevHash, last)
	if _, err := repository.pool.Exec(ctx,
		`UPDATE audit_logs SET hash=$1 WHERE id=$2`, fixed, last.ID); err != nil {
		t.Fatal(err)
	}

	// Clean sweep: alert marked recovered, evidence preserved.
	now = now.Add(time.Hour)
	check4, err := repository.VerifyAuditChainScheduled(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if check4.BrokenAt != -1 {
		t.Fatalf("repaired chain verified as broken at %d", check4.BrokenAt)
	}
	alerts, err = repository.ListAuditChainAlerts(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Status != "recovered" || alerts[0].RecoveredAt == nil {
		t.Fatalf("recovery not recorded: %+v", alerts)
	}

	// History and recent checks read back: one row per sweep — clean,
	// broken, broken (no duplicate alert), broken post-dispose, repaired.
	checks, err := repository.RecentAuditChainChecks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 5 {
		t.Fatalf("expected 5 recorded sweeps, got %d", len(checks))
	}
}

// tailAuditRow loads the newest audit entry in the same shape auditHash needs.
func (s *Store) tailAuditRow(ctx context.Context) (AuditLog, error) {
	var e AuditLog
	var actorID uuid.NullUUID
	err := s.pool.QueryRow(ctx, `
		SELECT id, occurred_at, actor_type, actor_id, actor_email, ip,
			action, resource_type, resource_id, details, prev_hash, hash
		FROM audit_logs ORDER BY id DESC LIMIT 1`).Scan(
		&e.ID, &e.OccurredAt, &e.ActorType, &actorID, &e.ActorEmail, &e.IP,
		&e.Action, &e.ResourceType, &e.ResourceID, &e.Details, &e.PrevHash, &e.Hash)
	if err != nil {
		return AuditLog{}, err
	}
	e.ActorID = actorID
	if !e.ActorEmail.Valid {
		e.ActorEmail = sql.NullString{}
	}
	return e, nil
}
