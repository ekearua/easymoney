package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// G5 (AML spec §5): filing-clock tracking. Generating a report bundle syncs
// one idempotent clock per row; STR deadlines are 24h from the alert, CTR
// deadlines are the 7th of the following month; terminal alert statuses stamp
// their clock complete; overdue clocks surface for the admin panel.
func TestReportFilingClocks(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE report_filing_clocks`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	alertDate := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	txnID := uuid.New().String()

	// STR deadline: 24h from the alert's creation.
	strRef := FilingClockRef("STR", txnID)
	if err := repository.SyncReportFilingClocks(ctx, "STR", []string{strRef}, []time.Time{alertDate}, now); err != nil {
		t.Fatal(err)
	}
	clock, err := repository.ReportFilingClocks(ctx, "STR", 10)
	if err != nil || len(clock) != 1 {
		t.Fatalf("expected 1 STR clock, got %d (err=%v)", len(clock), err)
	}
	wantDue := alertDate.UTC().Add(24 * time.Hour)
	if !clock[0].DueAt.Equal(wantDue) {
		t.Fatalf("STR deadline: want %s, got %s", wantDue, clock[0].DueAt)
	}

	// Idempotency: re-syncing the same reference re-affirms, not duplicates,
	// and never moves the event date (the first determination stands).
	laterDate := alertDate.Add(6 * time.Hour)
	if err := repository.SyncReportFilingClocks(ctx, "STR", []string{strRef}, []time.Time{laterDate}, now); err != nil {
		t.Fatal(err)
	}
	clock, err = repository.ReportFilingClocks(ctx, "STR", 10)
	if err != nil || len(clock) != 1 {
		t.Fatalf("re-sync must not duplicate: got %d clocks (err=%v)", len(clock), err)
	}
	if !clock[0].EventDate.Equal(alertDate.UTC()) {
		t.Fatalf("re-sync must keep the original event date: want %s, got %s", alertDate.UTC(), clock[0].EventDate)
	}

	// CTR deadline: the 7th of the following month, end of day.
	payID := uuid.New().String()
	ctrDate := time.Date(2026, 9, 20, 15, 30, 0, 0, time.UTC)
	if err := repository.SyncReportFilingClocks(ctx, "CTR", []string{FilingClockRef("CTR", payID)}, []time.Time{ctrDate}, now); err != nil {
		t.Fatal(err)
	}
	ctrClocks, err := repository.ReportFilingClocks(ctx, "CTR", 10)
	if err != nil || len(ctrClocks) != 1 {
		t.Fatalf("expected 1 CTR clock, got %d (err=%v)", len(ctrClocks), err)
	}
	wantCTRDue := time.Date(2026, 10, 7, 23, 59, 59, 0, time.UTC)
	if !ctrClocks[0].DueAt.Equal(wantCTRDue) {
		t.Fatalf("CTR deadline: want %s, got %s", wantCTRDue, ctrClocks[0].DueAt)
	}

	// Overdue detection: the STR clock (due 30 Sep 09:00) is already past
	// now (30 Sep 12:00) and unfiled.
	due, err := repository.DueReportFilingClocks(ctx, now, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reference != strRef {
		t.Fatalf("expected exactly the overdue STR clock, got %d clocks", len(due))
	}
	if !due[0].IsOverdue(now) {
		t.Error("the STR clock must report overdue")
	}

	// Filing stamps complete the clock and remove it from the overdue set.
	if err := repository.MarkReportFiled(ctx, clock[0].ID, now); err != nil {
		t.Fatal(err)
	}
	due, err = repository.DueReportFilingClocks(ctx, now, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("filed clock must leave the overdue set, got %d", len(due))
	}
	// Idempotent re-mark keeps the first filing timestamp.
	if err := repository.MarkReportFiled(ctx, clock[0].ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := repository.ReportFilingClocks(ctx, "STR", 10)
	if err != nil || len(after) != 1 {
		t.Fatalf("expected 1 STR clock after re-mark, got %d (err=%v)", len(after), err)
	}
	if after[0].FiledAt == nil || !after[0].FiledAt.Equal(now) {
		t.Fatalf("re-mark must keep the first filing time: want %s, got %v", now, after[0].FiledAt)
	}

	// Terminal-disposition completion: CompleteReportFilingClocks stamps the
	// CTR-style completion by reference.
	if err := repository.SyncReportFilingClocks(ctx, "CTR", []string{FilingClockRef("CTR", payID)}, []time.Time{ctrDate}, now); err != nil {
		t.Fatal(err)
	}
	n, err := repository.CompleteReportFilingClocks(ctx, "CTR", []string{FilingClockRef("CTR", payID)}, now)
	if err != nil || n != 1 {
		t.Fatalf("completion: want 1 clock stamped, got %d (err=%v)", n, err)
	}
	final, err := repository.ReportFilingClocks(ctx, "CTR", 10)
	if err != nil || len(final) != 1 || final[0].FiledAt == nil {
		t.Fatalf("completed CTR clock must carry a filed_at (err=%v)", err)
	}
}

// G15: the AIF accounts-of-record filing follows the monthly registry cadence
// (7th of the following month, end of day) and its clocks sync, surface as
// overdue, and complete by reference exactly like the CTR/PEP clocks.
func TestAIFFilingClock(t *testing.T) { //nolint:parallel // shared scratch database
	// Pure deadline check: the AIF cadence matches CTR/PEP. This runs even
	// when no scratch database is configured.
	eventDate := time.Date(2026, 9, 20, 15, 30, 0, 0, time.UTC)
	wantDue := time.Date(2026, 10, 7, 23, 59, 59, 0, time.UTC)
	if got := FilingDeadline("AIF", eventDate, time.Now()); !got.Equal(wantDue) {
		t.Fatalf("AIF deadline: want %s, got %s", wantDue, got)
	}

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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE report_filing_clocks`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ref := FilingClockRef("AIF", "acct:"+uuid.New().String())
	if err := repository.SyncReportFilingClocks(ctx, "AIF", []string{ref}, []time.Time{eventDate}, now); err != nil {
		t.Fatal(err)
	}
	clock, err := repository.ReportFilingClocks(ctx, "AIF", 10)
	if err != nil || len(clock) != 1 {
		t.Fatalf("expected 1 AIF clock, got %d (err=%v)", len(clock), err)
	}
	if !clock[0].DueAt.Equal(wantDue) {
		t.Fatalf("AIF clock deadline: want %s, got %s", wantDue, clock[0].DueAt)
	}

	// The AIF clock (due 7 Oct) is already past now (8 Oct) and unfiled.
	due, err := repository.DueReportFilingClocks(ctx, now, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reference != ref {
		t.Fatalf("expected exactly the overdue AIF clock, got %d clocks", len(due))
	}

	// Terminal-disposition completion stamps the AIF clock by reference.
	n, err := repository.CompleteReportFilingClocks(ctx, "AIF", []string{ref}, now)
	if err != nil || n != 1 {
		t.Fatalf("AIF completion: want 1 clock stamped, got %d (err=%v)", n, err)
	}
	final, err := repository.ReportFilingClocks(ctx, "AIF", 10)
	if err != nil || len(final) != 1 || final[0].FiledAt == nil {
		t.Fatalf("completed AIF clock must carry a filed_at (err=%v)", err)
	}
}
