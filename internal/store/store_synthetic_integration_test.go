package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSyntheticChecksRecordAndSummarize covers the post-deploy check store:
// stages are appended per run, the window aggregate counts a run as passed only
// when none of its stages failed, the summary carries the newest run's stages
// and names its failing stages, and the outbox backlog counts only
// non-terminal rows and reports the oldest one's age.
func TestSyntheticChecksRecordAndSummarize(t *testing.T) {
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
	// The table is global, so each run works on its own band of rows and cleans
	// up after itself rather than truncating everyone's history.
	const marker = "synthetic_test"
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(ctx, `DELETE FROM synthetic_checks WHERE source=$1`, marker)
		_, _ = repository.pool.Exec(ctx, `DELETE FROM message_outbox WHERE kind=$1`, marker)
	})

	now := time.Now().UTC()
	passingRun := uuid.New()
	older := now.Add(-2 * time.Hour)
	stages := []SyntheticCheck{
		{ID: uuid.New(), RunID: passingRun, RanAt: older, Stage: "db", OK: true, DurationMS: 4, Source: marker},
		{ID: uuid.New(), RunID: passingRun, RanAt: older.Add(time.Millisecond), Stage: "schema", OK: true, DurationMS: 9, Source: marker},
		{ID: uuid.New(), RunID: passingRun, RanAt: older.Add(2 * time.Millisecond), Stage: "payment_path", Skipped: true, Detail: "no payments yet", Source: marker},
		{ID: uuid.New(), RunID: passingRun, RanAt: older.Add(3 * time.Millisecond), Stage: "outbox", OK: true, DurationMS: 2, Source: marker},
	}
	failingRun := uuid.New()
	fresh := now.Add(-1 * time.Minute)
	failing := []SyntheticCheck{
		{ID: uuid.New(), RunID: failingRun, RanAt: fresh, Stage: "db", OK: true, DurationMS: 3, Source: marker},
		{ID: uuid.New(), RunID: failingRun, RanAt: fresh.Add(time.Millisecond), Stage: "outbox", OK: false, Detail: "5 queued — over the 30m0s ceiling", DurationMS: 5, Source: marker},
	}
	for _, check := range append(append([]SyntheticCheck{}, stages...), failing...) {
		if err := repository.RecordSyntheticCheck(ctx, check); err != nil {
			t.Fatalf("record %s: %v", check.Stage, err)
		}
	}

	summary, err := repository.SyntheticCheckSummary(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Runs < 2 || summary.OKRuns < 1 || summary.FailedRuns < 1 {
		t.Fatalf("expected >=2 runs with both outcomes, got runs=%d ok=%d failed=%d",
			summary.Runs, summary.OKRuns, summary.FailedRuns)
	}
	if !summary.HasRun {
		t.Fatal("expected a latest run in the summary")
	}
	if summary.LastOK {
		t.Fatal("the newest run has a failed stage, so LastOK must be false")
	}
	if len(summary.FailingStages) != 1 || summary.FailingStages[0] != "outbox" {
		t.Fatalf("expected exactly the outbox stage to be named, got %v", summary.FailingStages)
	}
	if len(summary.LastStages) != 2 {
		t.Fatalf("expected the newest run's 2 stages, got %d", len(summary.LastStages))
	}
	if summary.LastStages[0].RunID != failingRun || summary.LastSource != marker {
		t.Fatalf("summary described the wrong run: run=%s source=%s", summary.LastStages[0].RunID, summary.LastSource)
	}

	// A skipped stage in a run with no failures is what makes that run pass:
	// the older run must still read as ok.
	windowStart := older.Add(-time.Minute)
	var olderOK bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT bool_and(ok OR skipped) FROM synthetic_checks
		WHERE run_id=$1 AND ran_at > $2`, passingRun, windowStart).Scan(&olderOK); err != nil {
		t.Fatal(err)
	}
	if !olderOK {
		t.Fatal("a run with a skipped stage and no failures must count as passed")
	}

	// Outbox backlog: one pending and one terminal row. Only the non-terminal
	// row counts, and its created_at anchors the oldest age.
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO message_outbox(recipient,kind,payload,status,created_at)
		VALUES($1,$2,'{}','pending',$3), ($1,$2,'{}','delivered',$3)`,
		"+2348000000000", marker, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	backlog, err := repository.OutboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.Pending < 1 {
		t.Fatalf("expected the pending row to be counted, got %d", backlog.Pending)
	}
	if backlog.OldestPendingAt == nil {
		t.Fatal("expected an oldest-pending timestamp")
	}
	if age := time.Since(*backlog.OldestPendingAt); age < 9*time.Minute {
		t.Fatalf("expected the oldest pending row to be ~10m old, got %s", age)
	}
}
