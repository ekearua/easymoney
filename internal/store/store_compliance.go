package store

// Spec G8 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §5): the annual independent-validation
// attestation. One row per validation cycle year; the admin console lists
// them as a checklist (cycle year, scope, status, due date, evidence link)
// and the operator records the validation once it happens. Every mutation is
// audited with the acting admin's identity, so the evidence trail is
// attributable.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Attestation statuses: scheduled → in_progress → submitted. The console
// derives "overdue" from the due date, it is not a stored status.
const (
	AttestationScheduled  = "scheduled"
	AttestationInProgress = "in_progress"
	AttestationSubmitted  = "submitted"
)

// ComplianceAttestation is one year's independent-validation evidence. The
// fields mirror what a regulator asks to see: who validated what scope, the
// findings, how they were remediated, and where the signed report lives.
type ComplianceAttestation struct {
	ID          int64
	CycleYear   int
	Scope       string
	Summary     string
	Findings    string
	Remediation string
	EvidenceURL string
	Status      string
	DueDate     time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsOverdue reports whether the cycle's due date passed without a submitted
// attestation — the console highlights these.
func (c ComplianceAttestation) IsOverdue(now time.Time) bool {
	return c.Status != AttestationSubmitted && now.After(c.DueDate)
}

const attestationCols = `id, cycle_year, scope, summary, findings, remediation, evidence_url, status, due_date, completed_at, created_at, updated_at`

func scanAttestation(row pgx.Row) (ComplianceAttestation, error) {
	var c ComplianceAttestation
	err := row.Scan(&c.ID, &c.CycleYear, &c.Scope, &c.Summary, &c.Findings,
		&c.Remediation, &c.EvidenceURL, &c.Status, &c.DueDate, &c.CompletedAt,
		&c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// EnsureComplianceAttestation creates the cycle's row when missing (status
// scheduled, due 31 December of the cycle year) and returns whether it was
// created. Called on app start so the current year is always on the clock.
func (s *Store) EnsureComplianceAttestation(ctx context.Context, cycleYear int, scope string, now time.Time) (ComplianceAttestation, bool, error) {
	due := time.Date(cycleYear, time.December, 31, 23, 59, 59, 0, time.UTC)
	var c ComplianceAttestation
	var created bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO compliance_attestations (cycle_year, scope, due_date)
		VALUES ($1, $2, $3)
		ON CONFLICT (cycle_year) DO UPDATE SET updated_at = now()
		RETURNING `+attestationCols+`, (xmax = 0)`,
		cycleYear, scope, due).Scan(
		&c.ID, &c.CycleYear, &c.Scope, &c.Summary, &c.Findings, &c.Remediation,
		&c.EvidenceURL, &c.Status, &c.DueDate, &c.CompletedAt, &c.CreatedAt,
		&c.UpdatedAt, &created)
	return c, created, err
}

// ListComplianceAttestations returns every cycle, newest first.
func (s *Store) ListComplianceAttestations(ctx context.Context) ([]ComplianceAttestation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+attestationCols+` FROM compliance_attestations ORDER BY cycle_year DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComplianceAttestation
	for rows.Next() {
		c, err := scanAttestation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateComplianceAttestation records the operator's evidence for a cycle.
// An empty status keeps the stored one (partial updates: findings before
// submission); a NULL completed_at keeps the stored timestamp. Any non-empty
// status must be one of the table's CHECK values — the console maps its form
// actions onto valid values before calling.
func (s *Store) UpdateComplianceAttestation(ctx context.Context, id int64, scope, summary, findings, remediation, evidenceURL, status string, dueDate time.Time, completedAt *time.Time) (ComplianceAttestation, error) {
	return scanAttestation(s.pool.QueryRow(ctx, `
		UPDATE compliance_attestations SET
			scope=$2, summary=$3, findings=$4, remediation=$5, evidence_url=$6,
			status=CASE WHEN $7='' THEN status ELSE $7 END,
			due_date=$8, completed_at=COALESCE($9, completed_at), updated_at=now()
		WHERE id=$1
		RETURNING `+attestationCols,
		id, scope, summary, findings, remediation, evidenceURL, status, dueDate, completedAt))
}
