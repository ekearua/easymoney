package store

// Spec gap G9: adverse-media screening. The recorded row is the adverse-media
// lane's audit trail; a non-clear decision opens (idempotently) an
// 'adverse_media' manual review case, disposed from the KYC review queue.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// AdverseMediaHit is one adverse-media record a provider matched, persisted in
// the matched_hits JSONB column.
type AdverseMediaHit struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	PublishedAt string `json:"published_at"`
	ProviderRef string `json:"reference"`
	ListVersion string `json:"list_version"`
}

// AdverseMediaScreening is one recorded adverse-media screen for a user.
type AdverseMediaScreening struct {
	ID           int64
	UserID       uuid.UUID
	Provider     string
	Decision     string
	MatchedHits  []AdverseMediaHit
	MatchedNames []string
	LegalName    string
	ListVersion  string
	ProviderRef  string
	Message      string
	ScreenedAt   time.Time
	RescreenDue  *time.Time
}

// RecordAdverseMediaScreening persists an adverse-media outcome and, when the
// decision is not clear, opens the user's adverse_media review case (or leaves
// the already-open one untouched). It mirrors EnsurePEPEDDCase's idempotency:
// one open adverse_media case per user at a time.
func (s *Store) RecordAdverseMediaScreening(ctx context.Context, scr AdverseMediaScreening) (AdverseMediaScreening, error) {
	if scr.ScreenedAt.IsZero() {
		scr.ScreenedAt = time.Now().UTC()
	}
	hits, _ := json.Marshal(scr.MatchedHits)
	if hits == nil {
		hits = []byte("[]")
	}
	names, _ := json.Marshal(scr.MatchedNames)
	if names == nil {
		names = []byte("[]")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO adverse_media_screenings(user_id,provider,decision,matched_hits,matched_names,
		                                    legal_name,list_version,provider_ref,message,screened_at,rescreen_due)
		VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,$10,$11)
		RETURNING id, screened_at, rescreen_due`,
		scr.UserID, scr.Provider, scr.Decision, string(hits), string(names),
		scr.LegalName, scr.ListVersion, scr.ProviderRef, scr.Message, scr.ScreenedAt, scr.RescreenDue,
	).Scan(&scr.ID, &scr.ScreenedAt, &scr.RescreenDue)
	if err != nil {
		return AdverseMediaScreening{}, fmt.Errorf("record adverse-media screening: %w", err)
	}
	if scr.Decision != kyc.ScreenClear {
		reason := "adverse-media determination (" + scr.Provider + "): " + strings.TrimSpace(scr.Message)
		if _, _, err := s.ensureAdverseMediaCase(ctx, scr.UserID, reason, scr.ID); err != nil {
			return AdverseMediaScreening{}, fmt.Errorf("ensure adverse_media case: %w", err)
		}
	}
	return scr, nil
}

// ensureAdverseMediaCase opens the user's adverse_media review case if none is
// open, mirroring EnsurePEPEDDCase (idempotent per user).
func (s *Store) ensureAdverseMediaCase(ctx context.Context, userID uuid.UUID, reason string, screeningID int64) (ManualReviewCase, bool, error) {
	var existing ManualReviewCase
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''), status, reason,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases
		WHERE user_id=$1 AND case_type='adverse_media' AND status='pending'
		ORDER BY created_at DESC LIMIT 1`, userID).Scan(
		&existing.ID, &existing.UserID, &existing.CaseType, &existing.RequestedTier,
		&existing.Status, &existing.Reason, &existing.ReviewerEmail,
		&existing.DecisionNote, &existing.CreatedAt, &existing.ReviewedAt)
	if err == nil {
		return existing, false, nil // an open case already covers this user
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ManualReviewCase{}, false, fmt.Errorf("check open adverse_media case: %w", err)
	}
	c, err := s.CreateManualReviewCase(ctx, ManualReviewCase{
		UserID:   userID,
		CaseType: "adverse_media",
		Reason:   reason,
	})
	if err != nil {
		return ManualReviewCase{}, false, fmt.Errorf("open adverse_media case: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.adverse_media_opened",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"case_id":             c.ID,
			"screening_result_id": screeningID,
			"reason":              reason,
		},
	}
	if _, err := s.AppendAuditLog(ctx, entry); err != nil {
		return c, true, fmt.Errorf("audit adverse_media open: %w", err)
	}
	return c, true, nil
}

const adverseMediaScreeningCols = `id,user_id,provider,decision,matched_hits::text,matched_names::text,legal_name,list_version,provider_ref,message,screened_at,rescreen_due`

// ListAdverseMediaScreenings returns recent adverse-media rows newest first.
func (s *Store) ListAdverseMediaScreenings(ctx context.Context, limit int) ([]AdverseMediaScreening, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+adverseMediaScreeningCols+` FROM adverse_media_screenings ORDER BY screened_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list adverse-media screenings: %w", err)
	}
	defer rows.Close()
	return collectAdverseMediaScreenings(rows)
}

// AdverseMediaScreeningsByUser returns the user's adverse-media rows newest
// first.
func (s *Store) AdverseMediaScreeningsByUser(ctx context.Context, userID uuid.UUID, limit int) ([]AdverseMediaScreening, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+adverseMediaScreeningCols+` FROM adverse_media_screenings WHERE user_id=$1 ORDER BY screened_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("adverse-media screenings by user: %w", err)
	}
	defer rows.Close()
	return collectAdverseMediaScreenings(rows)
}

func collectAdverseMediaScreenings(rows pgx.Rows) ([]AdverseMediaScreening, error) {
	var out []AdverseMediaScreening
	for rows.Next() {
		scr, err := scanAdverseMediaScreening(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, scr)
	}
	return out, rows.Err()
}

func scanAdverseMediaScreening(row rowScanner) (AdverseMediaScreening, error) {
	var scr AdverseMediaScreening
	var hitsText, namesText string
	err := row.Scan(&scr.ID, &scr.UserID, &scr.Provider, &scr.Decision, &hitsText, &namesText,
		&scr.LegalName, &scr.ListVersion, &scr.ProviderRef, &scr.Message, &scr.ScreenedAt, &scr.RescreenDue)
	if err != nil {
		return scr, err
	}
	scr.MatchedHits = []AdverseMediaHit{}
	if hitsText != "" {
		_ = json.Unmarshal([]byte(hitsText), &scr.MatchedHits)
	}
	scr.MatchedNames = []string{}
	if namesText != "" {
		_ = json.Unmarshal([]byte(namesText), &scr.MatchedNames)
	}
	return scr, nil
}
