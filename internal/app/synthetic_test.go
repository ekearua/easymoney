package app

import (
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/store"
)

func stage(name string, ok, skipped bool, ms int) store.SyntheticCheck {
	return store.SyntheticCheck{Stage: name, OK: ok, Skipped: skipped, DurationMS: ms, Detail: name + " detail"}
}

// TestBuildSyntheticPanel pins the panel's pass/fail semantics: no run reads as
// "no run recorded yet", an all-ok run reads as passed, a run with one failed
// stage is named in the title and in FailingStages, a skipped stage neither
// fails the run nor renders as a failure, and the outbox row only turns red
// when the oldest queued row crosses the configured ceiling.
func TestBuildSyntheticPanel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	t.Run("no run yet", func(t *testing.T) {
		panel := buildSyntheticPanel(store.SyntheticCheckSummary{}, store.OutboxBacklog{}, now, 30*time.Minute)
		if panel.HasRun {
			t.Fatal("expected HasRun false with no stages")
		}
		if !strings.Contains(panel.Title, "no run recorded") {
			t.Fatalf("title should say no run recorded, got %q", panel.Title)
		}
		if panel.Rows != nil {
			t.Fatalf("expected no rows, got %d", len(panel.Rows))
		}
	})

	t.Run("all stages pass", func(t *testing.T) {
		ranAt := now.Add(-90 * time.Second)
		summary := store.SyntheticCheckSummary{
			HasRun: true, Runs: 3, OKRuns: 3,
			LastRanAt: &ranAt, LastOK: true, LastSource: "pipeline",
			LastStages: []store.SyntheticCheck{
				stage("db", true, false, 4),
				stage("schema", true, false, 11),
				stage("payment_path", true, false, 7),
				stage("outbox", true, false, 3),
			},
		}
		panel := buildSyntheticPanel(summary, store.OutboxBacklog{}, now, 30*time.Minute)
		if !panel.LastOK || !strings.Contains(panel.Title, "passed") {
			t.Fatalf("expected a passing panel, got title %q ok=%v", panel.Title, panel.LastOK)
		}
		if len(panel.Rows) != 4 {
			t.Fatalf("expected 4 rows, got %d", len(panel.Rows))
		}
		for _, row := range panel.Rows {
			if row.State != "ok" {
				t.Fatalf("stage %s expected ok, got %s", row.Stage, row.State)
			}
		}
		// FormatAge is the backup panel's humaniser: minutes under an hour.
		if panel.RanAt != "1m ago" {
			t.Fatalf("expected a humanised age, got %q", panel.RanAt)
		}
		if panel.Source != "pipeline" {
			t.Fatalf("expected source pipeline, got %q", panel.Source)
		}
	})

	t.Run("one failed stage names itself", func(t *testing.T) {
		ranAt := now.Add(-5 * time.Minute)
		summary := store.SyntheticCheckSummary{
			HasRun: true, Runs: 2, OKRuns: 1, FailedRuns: 1,
			LastRanAt: &ranAt, LastOK: false,
			FailingStages: []string{"outbox"},
			LastStages: []store.SyntheticCheck{
				stage("db", true, false, 4),
				stage("outbox", false, false, 6),
			},
		}
		panel := buildSyntheticPanel(summary, store.OutboxBacklog{}, now, 30*time.Minute)
		if panel.LastOK {
			t.Fatal("a failed stage must make the run not-ok")
		}
		if !strings.Contains(panel.Title, "FAILED") || !strings.Contains(panel.Title, "outbox") {
			t.Fatalf("title should name the failing stage, got %q", panel.Title)
		}
		if panel.Rows[1].State != "failed" {
			t.Fatalf("expected the outbox row to read failed, got %s", panel.Rows[1].State)
		}
	})

	t.Run("a skipped stage is neither pass nor failure", func(t *testing.T) {
		ranAt := now
		summary := store.SyntheticCheckSummary{
			HasRun: true, Runs: 1, OKRuns: 1, LastRanAt: &ranAt, LastOK: true,
			LastStages: []store.SyntheticCheck{
				stage("db", true, false, 1),
				stage("payment_path", false, true, 0),
			},
		}
		panel := buildSyntheticPanel(summary, store.OutboxBacklog{}, now, 30*time.Minute)
		if !panel.LastOK {
			t.Fatal("a skipped stage must not fail the run")
		}
		if panel.Rows[1].State != "skipped" {
			t.Fatalf("expected skipped, got %s", panel.Rows[1].State)
		}
	})

	t.Run("outbox ceiling", func(t *testing.T) {
		oldest := now.Add(-45 * time.Minute)
		backlog := store.OutboxBacklog{Pending: 3, OldestPendingAt: &oldest}
		stale := buildSyntheticPanel(store.SyntheticCheckSummary{}, backlog, now, 30*time.Minute)
		if !stale.OutboxStale {
			t.Fatal("a 45m-old backlog must be stale against a 30m ceiling")
		}
		if !strings.Contains(stale.Outbox, "3 queued") || !strings.Contains(stale.Outbox, "oldest") {
			t.Fatalf("unexpected backlog label %q", stale.Outbox)
		}
		// Zero disables the assertion without hiding the row.
		disabled := buildSyntheticPanel(store.SyntheticCheckSummary{}, backlog, now, 0)
		if disabled.OutboxStale {
			t.Fatal("a zero ceiling must disable the backlog alert")
		}
		// An empty queue is never stale.
		empty := buildSyntheticPanel(store.SyntheticCheckSummary{}, store.OutboxBacklog{}, now, 30*time.Minute)
		if empty.OutboxStale || empty.Outbox != "empty" {
			t.Fatalf("expected an empty, non-stale backlog, got %q stale=%v", empty.Outbox, empty.OutboxStale)
		}
	})
}

// TestJoinStages pins the fallback used in the failure title.
func TestJoinStages(t *testing.T) {
	t.Parallel()
	if got := joinStages(nil); got != "unknown stage" {
		t.Fatalf("expected a placeholder for no stages, got %q", got)
	}
	if got := joinStages([]string{"db", "outbox"}); got != "db, outbox" {
		t.Fatalf("unexpected join: %q", got)
	}
}
