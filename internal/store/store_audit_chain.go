package store

// Spec G7 residual (CBN_AML_COMPLIANCE_SPEC.md §5): scheduled audit
// chain-verification. VerifyAuditChain recomputes the audit_logs hash chain
// and the dashboard renders the result on page load; this layer makes the
// sweep persistent and alerting:
//
//   - every scheduled sweep records a row in audit_chain_checks (rows
//     verified, break position, whether an alert was live);
//   - the first failing sweep after a clean one opens one audit_chain_alerts
//     row (a transition rule: a chain that stays broken — even after a
//     reviewer acknowledges it — does not raise a new alert every sweep);
//   - the next clean sweep marks the open alert recovered, preserving the
//     evidence; a reviewer disposes the alert as acknowledged_bug or
//     confirmed_tampering with an audited note.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditChainCheck is one scheduled sweep's outcome.
type AuditChainCheck struct {
	ID          int64
	CheckedAt   time.Time
	RowsChecked int
	BrokenAt    int // -1 when the chain is intact
	AlertOpen   bool
}

// AuditChainAlert is the human side of a chain break: what broke, and the
// reviewer's disposition (recovered by a clean sweep, or acknowledged as a
// bug, or confirmed as tampering).
type AuditChainAlert struct {
	ID            int64
	BrokenAtRow   int
	Status        string
	OpenedAt      time.Time
	RecoveredAt   *time.Time
	ReviewerEmail string
	OutcomeNote   string
}

const auditChainAlertCols = `id, broken_at_row, status, opened_at, recovered_at, reviewer_email, outcome_note`

func scanAuditChainAlert(row pgx.Row) (AuditChainAlert, error) {
	var a AuditChainAlert
	err := row.Scan(&a.ID, &a.BrokenAtRow, &a.Status, &a.OpenedAt, &a.RecoveredAt,
		&a.ReviewerEmail, &a.OutcomeNote)
	return a, err
}

// VerifyAuditChainScheduled runs the chain verification, records the sweep,
// and manages the alert lifecycle inside one transaction. The transition
// rule reads the previous sweep's outcome: an alert opens only when this
// sweep found a break and the previous sweep did not.
func (s *Store) VerifyAuditChainScheduled(ctx context.Context, now time.Time) (AuditChainCheck, error) {
	rowsChecked, broken, err := s.VerifyAuditChain(ctx)
	if err != nil {
		return AuditChainCheck{}, err
	}
	var check AuditChainCheck
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var prevBroken bool
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE((
				SELECT broken_at >= 0 FROM audit_chain_checks
				ORDER BY checked_at DESC, id DESC LIMIT 1), FALSE)`).Scan(&prevBroken); err != nil {
			return err
		}
		if broken >= 0 && !prevBroken {
			// Clean → broken transition: raise the alert (no-op when one is
			// somehow already open — the partial unique index admits one).
			if _, err := tx.Exec(ctx, `
				INSERT INTO audit_chain_alerts (broken_at_row) VALUES ($1)
				ON CONFLICT DO NOTHING`, broken); err != nil {
				return err
			}
		}
		if broken < 0 {
			// Chain intact again: an open alert is recovered, and an
			// acknowledged_bug alert is stamped recovered too — the clean
			// sweep is when the fix landed, which is evidence worth keeping
			// (the break window reads opened_at → recovered_at). A
			// confirmed_tampering determination is never rewritten.
			if _, err := tx.Exec(ctx, `
				UPDATE audit_chain_alerts SET status='recovered', recovered_at=$1
				WHERE status='open'
				   OR (status='acknowledged_bug' AND recovered_at IS NULL)`, now); err != nil {
				return err
			}
		}
		var alertOpen bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM audit_chain_alerts WHERE status='open')`).Scan(&alertOpen); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO audit_chain_checks (checked_at, rows_checked, broken_at, alert_open)
			VALUES ($1,$2,$3,$4)
			RETURNING id, checked_at, rows_checked, broken_at, alert_open`,
			now, rowsChecked, broken, alertOpen).Scan(
			&check.ID, &check.CheckedAt, &check.RowsChecked, &check.BrokenAt, &check.AlertOpen)
	})
	return check, err
}

// RecentAuditChainChecks returns the newest sweeps first, for the dashboard.
func (s *Store) RecentAuditChainChecks(ctx context.Context, limit int) ([]AuditChainCheck, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, checked_at, rows_checked, broken_at, alert_open
		FROM audit_chain_checks ORDER BY checked_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditChainCheck
	for rows.Next() {
		var c AuditChainCheck
		if err := rows.Scan(&c.ID, &c.CheckedAt, &c.RowsChecked, &c.BrokenAt, &c.AlertOpen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// OpenAuditChainAlert returns the live alert, if any.
func (s *Store) OpenAuditChainAlert(ctx context.Context) (AuditChainAlert, bool, error) {
	a, err := scanAuditChainAlert(s.pool.QueryRow(ctx,
		`SELECT `+auditChainAlertCols+` FROM audit_chain_alerts WHERE status='open' ORDER BY opened_at DESC LIMIT 1`))
	if err == pgx.ErrNoRows {
		return AuditChainAlert{}, false, nil
	}
	if err != nil {
		return AuditChainAlert{}, false, err
	}
	return a, true, nil
}

// ListAuditChainAlerts returns the alert history, newest first.
func (s *Store) ListAuditChainAlerts(ctx context.Context, limit int) ([]AuditChainAlert, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+auditChainAlertCols+` FROM audit_chain_alerts ORDER BY opened_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditChainAlert
	for rows.Next() {
		a, err := scanAuditChainAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DisposeAuditChainAlert records the reviewer's decision on the open alert.
// Disposing while the chain is still broken is allowed — that is exactly the
// "acknowledged bug, fix in progress" case — and the transition rule in
// VerifyAuditChainScheduled keeps further sweeps from re-raising until the
// chain verifies clean and breaks again.
func (s *Store) DisposeAuditChainAlert(ctx context.Context, id int64, reviewerEmail, outcomeNote string, confirmTampering bool) (AuditChainAlert, error) {
	status := "acknowledged_bug"
	if confirmTampering {
		status = "confirmed_tampering"
	}
	return scanAuditChainAlert(s.pool.QueryRow(ctx, `
		UPDATE audit_chain_alerts SET status=$2, reviewer_email=$3, outcome_note=$4
		WHERE id=$1 AND status='open'
		RETURNING `+auditChainAlertCols,
		id, status, reviewerEmail, outcomeNote))
}
