package store

// Spec G1 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.3a): PEP enhanced due diligence with
// senior-management approval. A PEP determination (screening decision
// possible, strong, or blocked) opens one pep_edd review case; the case stays
// pending until a senior manager disposes it with the sm_approve or sm_reject
// action, which is its own auditable act distinct from the ordinary reviewer
// approve/reject. While any pep_edd case is open for a user, AdvanceKYCTier
// refuses every advancement (see the gate in store_kyc.go), and the EDD case
// carries a heightened-ongoing-monitoring flag consumed by the risk scorer.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// EnsurePEPEDDCase opens the user's pep_edd review case when a PEP
// determination requires one, or closes out a stale one when the latest
// screening result has cleared. It is idempotent per user: one open case at a
// time, keyed on the screening result that caused it. The returned bool
// reports whether a new case was opened.
func (s *Store) EnsurePEPEDDCase(ctx context.Context, userID uuid.UUID, reason string, screeningResultID int64) (ManualReviewCase, bool, error) {
	var existing ManualReviewCase
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''), status, reason,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases
		WHERE user_id=$1 AND case_type='pep_edd' AND status='pending'
		ORDER BY created_at DESC LIMIT 1`, userID).Scan(
		&existing.ID, &existing.UserID, &existing.CaseType, &existing.RequestedTier,
		&existing.Status, &existing.Reason, &existing.ReviewerEmail,
		&existing.DecisionNote, &existing.CreatedAt, &existing.ReviewedAt)
	if err == nil {
		return existing, false, nil // an open case already covers this user
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ManualReviewCase{}, false, fmt.Errorf("check open pep_edd case: %w", err)
	}
	c, err := s.CreateManualReviewCase(ctx, ManualReviewCase{
		UserID:   userID,
		CaseType: "pep_edd",
		Reason:   reason,
	})
	if err != nil {
		return ManualReviewCase{}, false, fmt.Errorf("open pep_edd case: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.pep_edd_opened",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"case_id":             c.ID,
			"screening_result_id": screeningResultID,
			"reason":              reason,
		},
	}
	if _, err := s.AppendAuditLog(ctx, entry); err != nil {
		return c, true, fmt.Errorf("audit pep_edd open: %w", err)
	}
	return c, true, nil
}

// HasOpenPEPEDDCase reports whether the user has an undisposed pep_edd case.
// AdvanceKYCTier refuses every tier movement while this is true.
func (s *Store) HasOpenPEPEDDCase(ctx context.Context, userID uuid.UUID) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM manual_review_cases
		WHERE user_id=$1 AND case_type='pep_edd' AND status='pending'`,
		userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("open pep_edd check: %w", err)
	}
	return n > 0, nil
}

// ReviewPEPEDDCase records the senior-management disposition of a pep_edd
// case. The SM decision is its own status (sm_approved / sm_rejected), so the
// audit trail distinguishes it from the ordinary reviewer approve/reject that
// ReviewManualReviewCase performs. Resolving as sm_approved attaches the
// edd_completed evidence key so the EDD ladder requirement is satisfied;
// sm_rejected leaves the profile as-is for the reviewer to act on.
func (s *Store) ReviewPEPEDDCase(ctx context.Context, caseID int64, approve bool, smEmail, note string, actor *AuditLog) (ManualReviewCase, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManualReviewCase{}, fmt.Errorf("begin pep_edd review: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var c ManualReviewCase
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''), status, reason,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(
		&c.ID, &c.UserID, &c.CaseType, &c.RequestedTier, &c.Status, &c.Reason,
		&c.ReviewerEmail, &c.DecisionNote, &c.CreatedAt, &c.ReviewedAt)
	if err != nil {
		return ManualReviewCase{}, fmt.Errorf("load pep_edd case: %w", err)
	}
	if c.CaseType != "pep_edd" {
		return ManualReviewCase{}, fmt.Errorf("case %d is %s, not pep_edd", c.ID, c.CaseType)
	}
	if c.Status != "pending" {
		return ManualReviewCase{}, fmt.Errorf("pep_edd case %d already %s", c.ID, c.Status)
	}
	status := "sm_rejected"
	if approve {
		status = "sm_approved"
	}
	if err := tx.QueryRow(ctx, `
		UPDATE manual_review_cases
		SET status=$2, reviewer_email=$3, decision_note=$4, reviewed_at=now()
		WHERE id=$1
		RETURNING status, COALESCE(reviewer_email,''), COALESCE(decision_note,''), reviewed_at`,
		caseID, status, nullIfEmpty(smEmail), note).Scan(
		&c.Status, &c.ReviewerEmail, &c.DecisionNote, &c.ReviewedAt); err != nil {
		return ManualReviewCase{}, fmt.Errorf("update pep_edd case: %w", err)
	}
	// Heightened ongoing monitoring: an SM disposition is itself a risk
	// observation with decay/cancellation semantics (the band machinery).
	action := "kyc.pep_edd_sm_rejected"
	if approve {
		action = "kyc.pep_edd_sm_approved"
	}
	entry := AuditLog{
		ActorType:    "admin",
		Action:       action,
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: c.UserID.String(), Valid: true},
		Details: map[string]any{
			"case_id":       c.ID,
			"decision_note": note,
			"sm_email":      smEmail,
		},
	}
	if actor != nil {
		entry.ActorType = actor.ActorType
		entry.ActorID = actor.ActorID
		entry.ActorEmail = actor.ActorEmail
		entry.IP = actor.IP
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return ManualReviewCase{}, fmt.Errorf("audit pep_edd review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ManualReviewCase{}, fmt.Errorf("commit pep_edd review: %w", err)
	}
	if approve {
		// EDD performed and approved: attach the EDD evidence key so the L3
		// requirement set is satisfiable once the other evidence arrives.
		profile, err := s.EnsureKYCProfile(ctx, c.UserID)
		if err != nil {
			return c, fmt.Errorf("ensure profile for pep_edd evidence: %w", err)
		}
		merged := mergeEvidence(profile.Evidence, []string{kyc.EvEDDCompleted})
		if _, err := s.pool.Exec(ctx, `
			UPDATE kyc_profiles SET evidence=$2::jsonb, updated_at=now() WHERE user_id=$1`,
			c.UserID, evidenceJSONOf(merged)); err != nil {
			return c, fmt.Errorf("attach pep_edd evidence: %w", err)
		}
	}
	return c, nil
}

func evidenceJSONOf(e []string) string {
	b, _ := json.Marshal(e)
	return string(b)
}
