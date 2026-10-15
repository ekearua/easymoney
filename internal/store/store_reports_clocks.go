package store

// Spec G5 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.4): filing-clock tracking. NFIU
// instructions set real deadlines — an STR within 24 hours of the
// determination of suspicion, a CTR by the 7th of the following month — and
// the PEP/AIF registry extracts follow that monthly cadence; a missed clock is
// itself a reportable failure. Report rows are regenerated on demand, so the
// clock lives in report_filing_clocks keyed by the row's stable identity
// (report type + reference): SyncReportFilingClocks is idempotent,
// DueReportFilingClocks answers "what is late", and completion is stamped when
// the row's underlying alert/case reaches a terminal status.

import (
	"context"
	"fmt"
	"time"
)

// FilingDeadline computes the NFIU filing deadline for one report row.
//
//   - STR: within 24 hours of the determination of suspicion — the alert's
//     creation time is that determination.
//   - CTR: by the 7th day of the month following the transaction.
//   - PEP: no statutory clock; the registry extract is due on the same
//     monthly cadence as a CTR so the surface stays uniform.
//   - AIF: no statutory clock; the accounts-of-record register is filed on
//     request or on the same monthly registry cadence as a CTR/PEP.
func FilingDeadline(reportType string, eventDate time.Time, now time.Time) time.Time {
	eventUTC := eventDate.UTC()
	// monthly is the 7th of the following month, end of day: the registry
	// cadence shared by CTR, PEP, and AIF.
	monthly := func() time.Time {
		firstOfNext := time.Date(eventUTC.Year(), eventUTC.Month(), 1, 0, 0, 0, 0, time.UTC).
			AddDate(0, 1, 0)
		return firstOfNext.AddDate(0, 0, 6).Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	}
	switch reportType {
	case "STR":
		return eventUTC.Add(24 * time.Hour)
	case "AIF":
		return monthly()
	default: // CTR and PEP: 7th of the following month, end of day
		return monthly()
	}
}

// FilingClockRef returns the stable reference a generated row's clock is
// keyed on. STR rows key on the alert-backed transaction id (stable across
// regenerations); CTR rows key on the payment id; PEP rows key on the
// customer id (one clock per customer determination); AIF rows key on the
// account-of-record reference ("acct:"+account id).
func FilingClockRef(reportType string, stableID string) string {
	return reportType + ":" + stableID
}

// ReportFilingClock is one tracked filing obligation.
type ReportFilingClock struct {
	ID         int64
	ReportType string
	Reference  string
	EventDate  time.Time
	DueAt      time.Time
	FiledAt    *time.Time
	Notified   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// IsOverdue reports whether the clock has passed its deadline unfiled.
func (c ReportFilingClock) IsOverdue(now time.Time) bool {
	return c.FiledAt == nil && now.After(c.DueAt)
}

// SyncReportFilingClocks upserts one clock per report row. Rows are keyed by
// (report_type, reference), so regenerating the same bundle re-affirms the
// same clocks instead of duplicating them; an existing clock's event date is
// never moved (the first determination of suspicion stands).
func (s *Store) SyncReportFilingClocks(ctx context.Context, reportType string, refs []string, eventDates []time.Time, now time.Time) error {
	if len(refs) != len(eventDates) {
		return fmt.Errorf("filing clock sync: %d refs for %d dates", len(refs), len(eventDates))
	}
	for i, ref := range refs {
		eventDate := eventDates[i].UTC()
		due := FilingDeadline(reportType, eventDate, now)
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO report_filing_clocks(report_type, reference, event_date, due_at, created_at, updated_at)
			VALUES($1,$2,$3,$4,$5,$5)
			ON CONFLICT (report_type, reference) DO UPDATE
			SET updated_at = EXCLUDED.updated_at`,
			reportType, ref, eventDate, due, now.UTC()); err != nil {
			return fmt.Errorf("filing clock sync %s: %w", ref, err)
		}
	}
	return nil
}

// DueReportFilingClocks lists unfiled clocks whose deadline has passed (or
// passes within the lookahead), oldest due first.
func (s *Store) DueReportFilingClocks(ctx context.Context, now time.Time, lookahead time.Duration, limit int) ([]ReportFilingClock, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if lookahead < 0 {
		lookahead = 0
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, report_type, reference, event_date, due_at, filed_at, overdue_notified, created_at, updated_at
		FROM report_filing_clocks
		WHERE filed_at IS NULL AND due_at <= $1
		ORDER BY due_at ASC LIMIT $2`,
		now.UTC().Add(lookahead), limit)
	if err != nil {
		return nil, fmt.Errorf("due filing clocks: %w", err)
	}
	defer rows.Close()
	return scanFilingClocks(rows)
}

// ReportFilingClocks returns every clock of a report type, newest deadline
// first, for the /admin/reports clock surface.
func (s *Store) ReportFilingClocks(ctx context.Context, reportType string, limit int) ([]ReportFilingClock, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, report_type, reference, event_date, due_at, filed_at, overdue_notified, created_at, updated_at
		FROM report_filing_clocks
		WHERE ($1 = '' OR report_type = $1)
		ORDER BY due_at ASC LIMIT $2`, reportType, limit)
	if err != nil {
		return nil, fmt.Errorf("filing clocks: %w", err)
	}
	defer rows.Close()
	return scanFilingClocks(rows)
}

// MarkReportFiled stamps the filing timestamp on one clock. Idempotent: a
// re-mark keeps the first filing time.
func (s *Store) MarkReportFiled(ctx context.Context, id int64, filedAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE report_filing_clocks
		SET filed_at = COALESCE(filed_at, $2), updated_at = now()
		WHERE id = $1`, id, filedAt.UTC())
	if err != nil {
		return fmt.Errorf("mark report filed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark report filed: clock %d not found", id)
	}
	return nil
}

// CompleteReportFilingClocks stamps the filing timestamp on every clock whose
// report type + reference matches a terminal disposition — the alert-side
// analogue of the reviewer closing the case behind the report.
func (s *Store) CompleteReportFilingClocks(ctx context.Context, reportType string, refs []string, completedAt time.Time) (int64, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	var n int64
	for _, ref := range refs {
		tag, err := s.pool.Exec(ctx, `
			UPDATE report_filing_clocks
			SET filed_at = COALESCE(filed_at, $3), updated_at = now()
			WHERE report_type = $1 AND reference = $2 AND filed_at IS NULL`,
			reportType, ref, completedAt.UTC())
		if err != nil {
			return n, fmt.Errorf("complete filing clock %s: %w", ref, err)
		}
		n += tag.RowsAffected()
	}
	return n, nil
}

func scanFilingClocks(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]ReportFilingClock, error) {
	var out []ReportFilingClock
	for rows.Next() {
		var c ReportFilingClock
		if err := rows.Scan(&c.ID, &c.ReportType, &c.Reference, &c.EventDate, &c.DueAt,
			&c.FiledAt, &c.Notified, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan filing clock: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
