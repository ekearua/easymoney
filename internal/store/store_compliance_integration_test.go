package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// G8 (AML spec §5): the annual independent-validation attestation. The
// current cycle is ensured idempotently on the clock; the operator records
// scope, findings and evidence; a submitted cycle needs its completion date;
// an unsubmitted cycle past its due date reads as overdue.
func TestComplianceAttestationLifecycle(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE compliance_attestations`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	// Ensure is idempotent: two calls, one row, no duplicate cycle.
	first, created1, err := repository.EnsureComplianceAttestation(ctx, 2026, "Automated AML solution — Baseline Standards", now)
	if err != nil {
		t.Fatal(err)
	}
	second, created2, err := repository.EnsureComplianceAttestation(ctx, 2026, "Automated AML solution — Baseline Standards", now)
	if err != nil {
		t.Fatal(err)
	}
	if !created1 || created2 {
		t.Fatalf("ensure idempotency broken: created1=%v created2=%v", created1, created2)
	}
	if first.ID != second.ID {
		t.Fatalf("ensure returned different rows: %d vs %d", first.ID, second.ID)
	}
	if first.Status != AttestationScheduled {
		t.Fatalf("new cycle status = %q, want scheduled", first.Status)
	}
	if got := first.DueDate; got.Month() != time.December || got.Day() != 31 || got.Year() != 2026 {
		t.Fatalf("due date = %v, want 31 Dec 2026", got)
	}
	if first.IsOverdue(now) {
		t.Fatal("cycle overdue before its due date")
	}

	// Recording evidence without a status keeps the current one.
	kept, err := repository.UpdateComplianceAttestation(ctx, first.ID,
		"Automated AML solution — Baseline Standards",
		"Independent reviewer engaged", "", "", "https://evidence.internal/attestations/2026-pending",
		"", first.DueDate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Status != AttestationScheduled {
		t.Fatalf("empty status flipped the row to %q, want unchanged scheduled", kept.Status)
	}

	// Submitting needs the completion date (enforced at the handler, the
	// store records what it is given).
	submittedAt := now.AddDate(0, 1, 3)
	final, err := repository.UpdateComplianceAttestation(ctx, first.ID,
		"Automated AML solution — Baseline Standards",
		"Scope: monitoring, screening, reporting, record-keeping",
		"No material findings; two low-severity observations on rule thresholds",
		"Thresholds re-tuned and documented; evidence attached",
		"https://evidence.internal/attestations/2026-signed.pdf",
		AttestationSubmitted, first.DueDate, &submittedAt)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != AttestationSubmitted || final.CompletedAt == nil {
		t.Fatalf("submission lost: status=%q completed=%v", final.Status, final.CompletedAt)
	}
	if final.IsOverdue(submittedAt) {
		t.Fatal("submitted cycle reads as overdue")
	}

	// An unsubmitted cycle past its due date is the overdue signal.
	overdue := ComplianceAttestation{Status: AttestationInProgress, DueDate: now.Add(-48 * time.Hour)}
	if !overdue.IsOverdue(now) {
		t.Fatal("unsubmitted cycle past its due date must read as overdue")
	}
}
