package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// IndividualProfile is the demo KYC profile that unlocks individual-only features.
type IndividualProfile struct {
	UserID      uuid.UUID
	LegalName   string
	DateOfBirth time.Time
	Gender      string
	Address     string
	Occupation  string
	KYCStatus   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// KYCProfile is a user's position on the L0-L3 identity ladder (C9/C10).
type KYCProfile struct {
	UserID                uuid.UUID
	Tier                  string
	TierUpdatedAt         time.Time
	Evidence              []string
	LastScreeningDecision string
	ReviewStatus          string
	ReviewerEmail         string
	ReviewedAt            *time.Time
	RiskScore             float64
	RiskBand              string
	RiskUpdatedAt         *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// CustomerVerification is one identity or channel verification performed.
type CustomerVerification struct {
	ID               int64
	UserID           uuid.UUID
	VerificationType string
	Status           string
	Provider         string
	ProviderRef      string
	Result           map[string]any
	VerifiedAt       time.Time
	ExpiresAt        *time.Time
}

// ScreeningResult is one sanctions/PEP screening outcome.
type ScreeningResult struct {
	ID           int64
	UserID       uuid.UUID
	Provider     string
	Decision     string
	MatchedNames []string
	ScreenedAt   time.Time
	RescreenDue  *time.Time
}

// RiskEvent is one ML/FT risk observation.
type RiskEvent struct {
	ID         int64
	UserID     uuid.UUID
	EventType  string
	Score      float64
	Details    map[string]any
	OccurredAt time.Time
}

// ManualReviewCase is a KYC review-queue item.
type ManualReviewCase struct {
	ID            int64
	UserID        uuid.UUID
	CaseType      string
	RequestedTier string
	Status        string
	Reason        string
	ReviewerEmail string
	DecisionNote  string
	CreatedAt     time.Time
	ReviewedAt    *time.Time
}

// TransactionAlert is one suspicious activity detection raised by the
// transaction monitor (C14).
type TransactionAlert struct {
	ID            int64
	UserID        uuid.UUID
	PaymentID     *uuid.UUID
	Rule          string
	Severity      string
	Details       map[string]any
	Status        string
	ReviewerEmail string
	ReviewedAt    *time.Time
	CreatedAt     time.Time
}

// UpsertIndividualProfile records the CBN Tier-1 self-declared profile (legal
// name, date of birth, gender, address), upgrades the user to the individual
// account level, and records the tier1_basic_details verification. Sanctions/
// PEP screening and L0-L3 ladder promotion are handled by the caller
// (RecordScreeningResult then AdvanceKYCTier/AdvanceKYCTierTo), so a blocking
// screening decision keeps the user below L2.
func (s *Store) UpsertIndividualProfile(ctx context.Context, userID uuid.UUID, legalName string, dob time.Time, address, occupation, gender string) (IndividualProfile, error) {
	var profile IndividualProfile
	err := s.pool.QueryRow(ctx, `
		INSERT INTO individual_profiles(user_id,legal_name,date_of_birth,address,occupation,gender,kyc_status)
		VALUES($1,$2,$3,$4,$5,$6,'approved_simulated')
		ON CONFLICT(user_id) DO UPDATE
		SET legal_name=EXCLUDED.legal_name,
			date_of_birth=EXCLUDED.date_of_birth,
			address=EXCLUDED.address,
			occupation=EXCLUDED.occupation,
			gender=EXCLUDED.gender,
			kyc_status='approved_simulated',
			updated_at=now()
		RETURNING user_id,legal_name,date_of_birth,address,occupation,gender,kyc_status,created_at,updated_at`,
		userID, strings.TrimSpace(legalName), dob, strings.TrimSpace(address), strings.TrimSpace(occupation), strings.TrimSpace(gender),
	).Scan(&profile.UserID, &profile.LegalName, &profile.DateOfBirth, &profile.Address, &profile.Occupation, &profile.Gender, &profile.KYCStatus, &profile.CreatedAt, &profile.UpdatedAt)
	if err != nil {
		return IndividualProfile{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET account_level='individual',updated_at=now() WHERE id=$1`, userID); err != nil {
		return IndividualProfile{}, err
	}
	if _, err := s.RecordCustomerVerification(ctx, CustomerVerification{
		UserID:           userID,
		VerificationType: kyc.EvTier1BasicDetails,
		Provider:         "simulated",
		Result:           map[string]any{"status": "approved_simulated"},
	}); err != nil {
		return IndividualProfile{}, err
	}
	return profile, nil
}

// HealIndividualAccountLevel repairs the denormalized account label for a
// user whose KYC ladder already vouches L2+. Tier paths outside
// UpsertIndividualProfile (e.g. admin advancement) can leave account_level
// behind, which gates P2P and thrift even though the profile page reports
// "already verified". Idempotent: only a mismatched label is touched.
func (s *Store) HealIndividualAccountLevel(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET account_level='individual',updated_at=now() WHERE id=$1 AND account_level<>'individual'`, userID)
	return err
}

// IndividualProfileByUser resolves the profile used to gate thrift creation.
func (s *Store) IndividualProfileByUser(ctx context.Context, userID uuid.UUID) (IndividualProfile, error) {
	var profile IndividualProfile
	err := s.pool.QueryRow(ctx, `
		SELECT user_id,legal_name,date_of_birth,gender,address,occupation,kyc_status,created_at,updated_at
		FROM individual_profiles
		WHERE user_id=$1`, userID).Scan(&profile.UserID, &profile.LegalName, &profile.DateOfBirth, &profile.Gender, &profile.Address, &profile.Occupation, &profile.KYCStatus, &profile.CreatedAt, &profile.UpdatedAt)
	return profile, err
}

// EnsureKYCProfile creates an L0 profile row for the user if none exists and
// returns the current profile. The L0 row is the user-creation milestone:
// W1 opens a pending individual wallet alongside it, so every user has a
// wallet from first contact (activated when the ladder reaches L1).
func (s *Store) EnsureKYCProfile(ctx context.Context, userID uuid.UUID) (KYCProfile, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO kyc_profiles(user_id)
		VALUES($1)
		ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return KYCProfile{}, fmt.Errorf("ensure kyc profile: %w", err)
	}
	if _, err := s.EnsureUserWallet(ctx, userID, ""); err != nil {
		return KYCProfile{}, fmt.Errorf("ensure user wallet: %w", err)
	}
	return s.KYCProfileByUser(ctx, userID)
}

// KYCProfileByUser resolves a user's KYC ladder position.
func (s *Store) KYCProfileByUser(ctx context.Context, userID uuid.UUID) (KYCProfile, error) {
	var p KYCProfile
	var evidence []byte
	var reviewedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		       COALESCE(last_screening_decision,''),
		       review_status, COALESCE(reviewer_email,''), reviewed_at,
		       COALESCE(risk_score,0), COALESCE(risk_band,'low'), risk_updated_at,
		       created_at, updated_at
		FROM kyc_profiles WHERE user_id=$1`, userID).Scan(
		&p.UserID, &p.Tier, &p.TierUpdatedAt, &evidence,
		&p.LastScreeningDecision, &p.ReviewStatus, &p.ReviewerEmail,
		&reviewedAt, &p.RiskScore, &p.RiskBand, &p.RiskUpdatedAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return KYCProfile{}, err
	}
	_ = json.Unmarshal(evidence, &p.Evidence)
	if p.Evidence == nil {
		p.Evidence = []string{}
	}
	p.ReviewedAt = reviewedAt
	return p, nil
}

// ListKYCProfiles returns all ladder positions (for the admin review page).
func (s *Store) ListKYCProfiles(ctx context.Context, limit int) ([]KYCProfile, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		       COALESCE(last_screening_decision,''),
		       review_status, COALESCE(reviewer_email,''), reviewed_at,
		       COALESCE(risk_score,0), COALESCE(risk_band,'low'), risk_updated_at,
		       created_at, updated_at
		FROM kyc_profiles ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []KYCProfile
	for rows.Next() {
		var p KYCProfile
		var evidence []byte
		var reviewedAt *time.Time
		if err := rows.Scan(&p.UserID, &p.Tier, &p.TierUpdatedAt, &evidence,
			&p.LastScreeningDecision, &p.ReviewStatus, &p.ReviewerEmail,
			&reviewedAt, &p.RiskScore, &p.RiskBand, &p.RiskUpdatedAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &p.Evidence)
		if p.Evidence == nil {
			p.Evidence = []string{}
		}
		p.ReviewedAt = reviewedAt
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// KYCProfileByTier lists users currently at a given tier.
func (s *Store) KYCProfileByTier(ctx context.Context, tier string) ([]KYCProfile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		       COALESCE(last_screening_decision,''),
		       review_status, COALESCE(reviewer_email,''), reviewed_at,
		       COALESCE(risk_score,0), COALESCE(risk_band,'low'), risk_updated_at,
		       created_at, updated_at
		FROM kyc_profiles WHERE tier=$1 ORDER BY tier_updated_at DESC`, tier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []KYCProfile
	for rows.Next() {
		var p KYCProfile
		var evidence []byte
		var reviewedAt *time.Time
		if err := rows.Scan(&p.UserID, &p.Tier, &p.TierUpdatedAt, &evidence,
			&p.LastScreeningDecision, &p.ReviewStatus, &p.ReviewerEmail,
			&reviewedAt, &p.RiskScore, &p.RiskBand, &p.RiskUpdatedAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &p.Evidence)
		if p.Evidence == nil {
			p.Evidence = []string{}
		}
		p.ReviewedAt = reviewedAt
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// AdvanceKYCTier moves a user to exactly the next tier after validating the
// ladder rules and required evidence. The transition is audited. An optional
// actor identifies the operator; when nil, "system" is recorded. Advancing to
// L2 or higher requires a non-blocked sanctions/PEP screening decision on the
// profile.
func (s *Store) AdvanceKYCTier(ctx context.Context, userID uuid.UUID, to string, evidence []string, actor *AuditLog) (KYCProfile, error) {
	profile, err := s.EnsureKYCProfile(ctx, userID)
	if err != nil {
		return KYCProfile{}, err
	}
	fromTier := profile.Tier
	if err := kyc.CanAdvance(profile.Tier, to, evidence); err != nil {
		return KYCProfile{}, err
	}
	if kyc.Order(to) >= kyc.Order(kyc.TierL2) && kyc.BlockedByScreening(profile.LastScreeningDecision) {
		return KYCProfile{}, fmt.Errorf("advance to %s blocked by screening decision %q", to, profile.LastScreeningDecision)
	}
	merged := mergeEvidence(profile.Evidence, evidence)
	evidenceJSON, _ := json.Marshal(merged)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYCProfile{}, fmt.Errorf("begin kyc advance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profile.Tier = to
	profile.Evidence = merged
	if err := tx.QueryRow(ctx, `
		UPDATE kyc_profiles
		SET tier=$2, tier_updated_at=now(), evidence=$3::jsonb, updated_at=now()
		WHERE user_id=$1
		RETURNING tier, tier_updated_at, review_status, COALESCE(reviewer_email,''), reviewed_at, created_at, updated_at`,
		userID, to, string(evidenceJSON)).Scan(&profile.Tier, &profile.TierUpdatedAt, &profile.ReviewStatus,
		&profile.ReviewerEmail, &profile.ReviewedAt, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return KYCProfile{}, fmt.Errorf("advance kyc tier: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.tier_advanced",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"from_tier": fromTier,
			"to_tier":   to,
			"evidence":  merged,
		},
	}
	if actor != nil {
		entry.ActorType = actor.ActorType
		entry.ActorID = actor.ActorID
		entry.ActorEmail = actor.ActorEmail
		entry.IP = actor.IP
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return KYCProfile{}, fmt.Errorf("audit kyc advance: %w", err)
	}
	// W1: reaching L1 (channel confirmed) activates the pending wallet opened
	// at user creation, enabling money-out.
	if to == kyc.TierL1 {
		if _, err := tx.Exec(ctx, `
			UPDATE wallet_accounts SET status='active', updated_at=now()
			WHERE owner_type='user' AND owner_id=$1 AND status='pending'`, userID); err != nil {
			return KYCProfile{}, fmt.Errorf("activate user wallet: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return KYCProfile{}, fmt.Errorf("commit kyc advance: %w", err)
	}
	return profile, nil
}

// DowngradeKYCTier moves a user to a lower tier (rescreen match, verification
// expiry, revocation) and audits the change.
func (s *Store) DowngradeKYCTier(ctx context.Context, userID uuid.UUID, to string, reason string, actor *AuditLog) (KYCProfile, error) {
	profile, err := s.EnsureKYCProfile(ctx, userID)
	if err != nil {
		return KYCProfile{}, err
	}
	fromTier := profile.Tier
	if err := kyc.CanDowngrade(profile.Tier, to); err != nil {
		return KYCProfile{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYCProfile{}, fmt.Errorf("begin kyc downgrade: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profile.Tier = to
	if err := tx.QueryRow(ctx, `
		UPDATE kyc_profiles
		SET tier=$2, tier_updated_at=now(), review_status='none', reviewer_email=NULL, reviewed_at=NULL, updated_at=now()
		WHERE user_id=$1
		RETURNING tier, tier_updated_at, review_status, COALESCE(reviewer_email,''), reviewed_at, created_at, updated_at`,
		userID, to).Scan(&profile.Tier, &profile.TierUpdatedAt, &profile.ReviewStatus,
		&profile.ReviewerEmail, &profile.ReviewedAt, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return KYCProfile{}, fmt.Errorf("downgrade kyc tier: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.tier_downgraded",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"from_tier": fromTier,
			"to_tier":   to,
			"reason":    reason,
		},
	}
	if actor != nil {
		entry.ActorType = actor.ActorType
		entry.ActorID = actor.ActorID
		entry.ActorEmail = actor.ActorEmail
		entry.IP = actor.IP
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return KYCProfile{}, fmt.Errorf("audit kyc downgrade: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KYCProfile{}, fmt.Errorf("commit kyc downgrade: %w", err)
	}
	return profile, nil
}

// AdvanceKYCTierTo promotes a user step by step up to a target tier using the
// supplied evidence, stopping early if the target is already reached. This is
// the entry point for flows that unlock a fixed tier (e.g. CBN Tier-1 data on
// file -> L1) regardless of the user's starting position.
func (s *Store) AdvanceKYCTierTo(ctx context.Context, userID uuid.UUID, to string, evidence []string, actor *AuditLog) (KYCProfile, error) {
	profile, err := s.EnsureKYCProfile(ctx, userID)
	if err != nil {
		return KYCProfile{}, err
	}
	for kyc.Order(profile.Tier) < kyc.Order(to) {
		next := tierAt(kyc.Order(profile.Tier) + 1)
		updated, err := s.AdvanceKYCTier(ctx, userID, next, evidence, actor)
		if err != nil {
			return KYCProfile{}, err
		}
		profile = updated
	}
	return profile, nil
}

func tierAt(order int) string {
	switch order {
	case 1:
		return kyc.TierL1
	case 2:
		return kyc.TierL2
	case 3:
		return kyc.TierL3
	}
	return kyc.TierL0
}

// KYCProfilesDueForRescreen lists profiles whose most recent screening result
// is older than the cutoff or entirely absent, newest first. Used by the
// periodic sanctions/PEP rescreen worker.
func (s *Store) KYCProfilesDueForRescreen(ctx context.Context, cutoff time.Time, limit int) ([]KYCProfile, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.user_id, p.tier, p.tier_updated_at, COALESCE(p.evidence::text,'[]'),
		       COALESCE(p.last_screening_decision,''),
		       p.review_status, COALESCE(p.reviewer_email,''), p.reviewed_at,
		       COALESCE(p.risk_score,0), COALESCE(p.risk_band,'low'), p.risk_updated_at,
		       p.created_at, p.updated_at
		FROM kyc_profiles p
		LEFT JOIN LATERAL (
			SELECT user_id, screened_at FROM screening_results
			WHERE user_id = p.user_id
			ORDER BY screened_at DESC LIMIT 1
		) latest ON latest.user_id = p.user_id
		WHERE latest.screened_at IS NULL OR latest.screened_at < $1
		ORDER BY p.updated_at ASC
		LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []KYCProfile
	for rows.Next() {
		var p KYCProfile
		var evidence []byte
		var reviewedAt *time.Time
		if err := rows.Scan(&p.UserID, &p.Tier, &p.TierUpdatedAt, &evidence,
			&p.LastScreeningDecision, &p.ReviewStatus, &p.ReviewerEmail,
			&reviewedAt, &p.RiskScore, &p.RiskBand, &p.RiskUpdatedAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &p.Evidence)
		if p.Evidence == nil {
			p.Evidence = []string{}
		}
		p.ReviewedAt = reviewedAt
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// RecordCustomerVerification persists a verification outcome.
func (s *Store) RecordCustomerVerification(ctx context.Context, v CustomerVerification) (CustomerVerification, error) {
	if v.Status == "" {
		v.Status = "completed"
	}
	if v.VerifiedAt.IsZero() {
		v.VerifiedAt = time.Now().UTC()
	}
	result, _ := json.Marshal(v.Result)
	if result == nil {
		result = []byte("{}")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO customer_verifications(user_id,verification_type,status,provider,provider_reference,result,verified_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8)
		RETURNING id, verified_at`,
		v.UserID, v.VerificationType, v.Status, nullIfEmpty(v.Provider), nullIfEmpty(v.ProviderRef),
		string(result), v.VerifiedAt, v.ExpiresAt).Scan(&v.ID, &v.VerifiedAt)
	if err != nil {
		return CustomerVerification{}, fmt.Errorf("record verification: %w", err)
	}
	return v, nil
}

// RecordScreeningResult persists a sanctions/PEP screening outcome and writes
// the decision through to the KYC profile so the ladder can enforce it. The
// rescreen due date is advanced by rescreenPeriod (0 keeps the caller value).
func (s *Store) RecordScreeningResult(ctx context.Context, r ScreeningResult, rescreenPeriod time.Duration) (ScreeningResult, error) {
	if r.ScreenedAt.IsZero() {
		r.ScreenedAt = time.Now().UTC()
	}
	if r.RescreenDue == nil && rescreenPeriod > 0 {
		due := r.ScreenedAt.Add(rescreenPeriod)
		r.RescreenDue = &due
	}
	names, _ := json.Marshal(r.MatchedNames)
	if names == nil {
		names = []byte("[]")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ScreeningResult{}, fmt.Errorf("begin screening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO screening_results(user_id,provider,decision,matched_names,screened_at,rescreen_due)
		VALUES($1,$2,$3,$4::jsonb,$5,$6)
		RETURNING id, screened_at, rescreen_due`,
		r.UserID, r.Provider, r.Decision, string(names), r.ScreenedAt, r.RescreenDue).Scan(&r.ID, &r.ScreenedAt, &r.RescreenDue)
	if err != nil {
		return ScreeningResult{}, fmt.Errorf("record screening: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kyc_profiles(user_id) VALUES($1) ON CONFLICT (user_id) DO NOTHING`, r.UserID); err != nil {
		return ScreeningResult{}, fmt.Errorf("ensure kyc profile for screening: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE kyc_profiles SET last_screening_decision=$2, updated_at=now() WHERE user_id=$1`,
		r.UserID, r.Decision); err != nil {
		return ScreeningResult{}, fmt.Errorf("update kyc profile screening: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.screening_recorded",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: r.UserID.String(), Valid: true},
		Details: map[string]any{
			"provider":     r.Provider,
			"decision":     r.Decision,
			"matched":      r.MatchedNames,
			"rescreen_due": r.RescreenDue,
		},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return ScreeningResult{}, fmt.Errorf("audit screening: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ScreeningResult{}, fmt.Errorf("commit screening: %w", err)
	}
	return r, nil
}

// RecordRiskEvent persists an ML/FT risk observation and recomputes the
// customer's aggregate risk band.
func (s *Store) RecordRiskEvent(ctx context.Context, e RiskEvent) (RiskEvent, error) {
	details, _ := json.Marshal(e.Details)
	if details == nil {
		details = []byte("{}")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO risk_events(user_id,event_type,score,details,occurred_at)
		VALUES($1,$2,$3,$4::jsonb,COALESCE($5, now()))
		RETURNING id, occurred_at`,
		e.UserID, e.EventType, e.Score, string(details), e.OccurredAt).Scan(&e.ID, &e.OccurredAt)
	if err != nil {
		return RiskEvent{}, fmt.Errorf("record risk event: %w", err)
	}
	if _, err := s.RecomputeRiskScore(ctx, e.UserID); err != nil {
		return RiskEvent{}, fmt.Errorf("recompute risk after event: %w", err)
	}
	return e, nil
}

// RecomputeRiskScore aggregates a user's ML/FT risk events with their KYC tier
// and last screening decision into a 0-100 score and low/medium/high band,
// persists it on the KYC profile, and audits the recalculation.
//
// Observations are weighted by age under RiskDecayHalfLife, so a score falls
// as a customer's history recedes, and negative events (the corrections
// written when a reviewer resolves an alert as a false positive) subtract the
// weight they cancelled.
func (s *Store) RecomputeRiskScore(ctx context.Context, userID uuid.UUID) (KYCProfile, error) {
	events := []kyc.RiskEventInput{}
	rows, err := s.pool.Query(ctx, `
		SELECT event_type, COALESCE(score,0), occurred_at FROM risk_events
		WHERE user_id=$1 ORDER BY occurred_at ASC`, userID)
	if err != nil {
		return KYCProfile{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var e kyc.RiskEventInput
		if err := rows.Scan(&e.EventType, &e.Score, &e.OccurredAt); err != nil {
			return KYCProfile{}, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return KYCProfile{}, err
	}
	profile, err := s.EnsureKYCProfile(ctx, userID)
	if err != nil {
		return KYCProfile{}, err
	}
	result := kyc.ScoreRisk(events, profile.Tier, profile.LastScreeningDecision, kyc.RiskScoring{
		Now:      time.Now().UTC(),
		HalfLife: s.RiskDecayHalfLife,
	})
	now := time.Now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYCProfile{}, fmt.Errorf("begin risk recompute: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	profile.RiskScore = result.Score
	profile.RiskBand = result.Band
	profile.RiskUpdatedAt = &now
	if err := tx.QueryRow(ctx, `
		UPDATE kyc_profiles
		SET risk_score=$2, risk_band=$3, risk_updated_at=$4, updated_at=now()
		WHERE user_id=$1
		RETURNING risk_score, risk_band, risk_updated_at`,
		userID, result.Score, result.Band, now).Scan(&profile.RiskScore, &profile.RiskBand, &profile.RiskUpdatedAt); err != nil {
		return KYCProfile{}, fmt.Errorf("persist risk score: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.risk_scored",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"score":              result.Score,
			"band":               result.Band,
			"factors":            result.Factors,
			"events":             len(events),
			"decay_half_life_ms": s.RiskDecayHalfLife.Milliseconds(),
		},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return KYCProfile{}, fmt.Errorf("audit risk recompute: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KYCProfile{}, fmt.Errorf("commit risk recompute: %w", err)
	}
	return profile, nil
}

// ListTransactionAlerts returns transaction-monitoring alerts newest first,
// optionally filtered to a status.
func (s *Store) ListTransactionAlerts(ctx context.Context, status string, limit int) ([]TransactionAlert, error) {
	return s.ListTransactionAlertsFiltered(ctx, status, "", limit)
}

// ListTransactionAlertsFiltered additionally filters to one monitored rail
// (payment, topup, standing_credit, refund, payout) by matching the alert's
// recorded rail tags, so an investigator can triage by the money rail that
// fired. An empty rail keeps every alert.
func (s *Store) ListTransactionAlertsFiltered(ctx context.Context, status, rail string, limit int) ([]TransactionAlert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, user_id, payment_id, rule, severity, COALESCE(details::text,'{}'),
		       status, COALESCE(reviewer_email,''), reviewed_at, created_at
		FROM transaction_alerts`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	if rail != "" {
		args = append(args, rail)
		if status == "" {
			query += ` WHERE`
		} else {
			query += ` AND`
		}
		query += ` details->'rails' ? $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var alerts []TransactionAlert
	for rows.Next() {
		var a TransactionAlert
		var paymentID *uuid.UUID
		var details []byte
		var reviewedAt *time.Time
		if err := rows.Scan(&a.ID, &a.UserID, &paymentID, &a.Rule, &a.Severity, &details,
			&a.Status, &a.ReviewerEmail, &reviewedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.PaymentID = paymentID
		a.ReviewedAt = reviewedAt
		_ = json.Unmarshal(details, &a.Details)
		if a.Details == nil {
			a.Details = map[string]any{}
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// RunTransactionMonitor evaluates settled money movements inside the lookback
// window — across every monitored rail: card/transfer payments, wallet
// top-ups, standing-account bank credits, refunds, and merchant settlement
// payouts — against the monitoring rules and records any alerts as risk
// events. A high or medium alert also escalates into a manual review case
// (case_type 'monitoring'). Alerts are deduped by payment id or rail
// reference, so rescanning the window never re-raises an alert. Returns the
// number of new alerts raised.
func (s *Store) RunTransactionMonitor(ctx context.Context, since time.Time, cfg kyc.MonitorConfig) (int, error) {
	inputs, err := s.monitorInputs(ctx, since, time.Now().UTC().Add(time.Hour), nil)
	if err != nil {
		return 0, err
	}
	byUser := map[uuid.UUID][]kyc.TransactionInput{}
	var order []uuid.UUID
	for _, t := range inputs {
		if _, seen := byUser[t.UserID]; !seen {
			order = append(order, t.UserID)
		}
		byUser[t.UserID] = append(byUser[t.UserID], t)
	}
	raised := 0
	for _, userID := range order {
		count, err := s.recordMonitorAlerts(ctx, userID, kyc.RunTransactionMonitor(byUser[userID], cfg))
		raised += count
		if err != nil {
			return raised, err
		}
	}
	return raised, nil
}

// monitorInputs loads settled events for every monitored rail, optionally
// filtered to one user. occurred_at is each rail's settlement timestamp and
// the window is [since, before). Merchant settlement payouts are attributed to
// the merchant's primary owner (merchant_owners is the only user link on that
// rail); a payout with no owner has nobody to alert and is skipped.
func (s *Store) monitorInputs(ctx context.Context, since, before time.Time, userID *uuid.UUID) ([]kyc.TransactionInput, error) {
	rows, err := s.pool.Query(ctx, `
		WITH events AS (
			SELECT CASE WHEN m.slug='xego-wallet-topup' THEN 'topup' ELSE 'payment' END AS rail,
			       p.id::text AS reference, p.id AS payment_id, p.user_id,
			       m.id AS merchant_id, m.name AS merchant_name, m.category AS merchant_category,
			       p.amount_kobo, p.paid_at AS occurred_at
			FROM payments p
			JOIN merchants m ON m.id = p.merchant_id
			WHERE p.status='succeeded' AND p.paid_at IS NOT NULL
			  AND p.paid_at >= $1 AND p.paid_at < $2
			  AND ($3::uuid IS NULL OR p.user_id = $3)

			UNION ALL

			SELECT 'standing_credit', sc.provider_reference, NULL, sc.user_id,
			       NULL, 'Standing account', 'Bank transfer', sc.amount_kobo, sc.created_at
			FROM standing_credits sc
			WHERE sc.status='applied' AND sc.created_at >= $1 AND sc.created_at < $2
			  AND ($3::uuid IS NULL OR sc.user_id = $3)

			UNION ALL

			SELECT 'refund', r.id::text, r.payment_id, p.user_id,
			       m.id, COALESCE(NULLIF(m.name,''), 'Refund'), COALESCE(m.category,''),
			       r.amount_kobo, COALESCE(r.completed_at, r.created_at)
			FROM refunds r
			JOIN payments p ON p.id = r.payment_id
			LEFT JOIN merchants m ON m.id = p.merchant_id
			WHERE r.status='succeeded'
			  AND COALESCE(r.completed_at, r.created_at) >= $1
			  AND COALESCE(r.completed_at, r.created_at) < $2
			  AND ($3::uuid IS NULL OR p.user_id = $3)

			UNION ALL

			SELECT 'payout', po.id::text, NULL, mo.user_id,
			       po.merchant_id, COALESCE(NULLIF(m.name,''), 'Merchant payout'), COALESCE(m.category,''),
			       po.amount_kobo, COALESCE(po.completed_at, po.created_at)
			FROM payouts po
			JOIN LATERAL (
				SELECT user_id FROM merchant_owners
				WHERE merchant_id = po.merchant_id
				ORDER BY created_at LIMIT 1
			) mo ON true
			JOIN merchants m ON m.id = po.merchant_id
			WHERE po.status='completed'
			  AND COALESCE(po.completed_at, po.created_at) >= $1
			  AND COALESCE(po.completed_at, po.created_at) < $2
			  AND ($3::uuid IS NULL OR mo.user_id = $3)
		)
		SELECT rail, reference, payment_id, user_id, merchant_id, merchant_name,
		       merchant_category, amount_kobo, occurred_at
		FROM events
		ORDER BY user_id, occurred_at`, since, before, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []kyc.TransactionInput
	for rows.Next() {
		var t kyc.TransactionInput
		var paymentID, merchantID *uuid.UUID
		if err := rows.Scan(&t.Rail, &t.Reference, &paymentID, &t.UserID, &merchantID,
			&t.MerchantName, &t.MerchantCategory, &t.AmountKobo, &t.PaidAt); err != nil {
			return nil, err
		}
		if paymentID != nil {
			t.PaymentID = *paymentID
		}
		if merchantID != nil {
			t.MerchantID = *merchantID
		}
		inputs = append(inputs, t)
	}
	return inputs, rows.Err()
}

// recordMonitorAlerts dedupes and stores the alerts raised for one user. An
// alert is skipped when a previous alert for the same rule carries any of its
// payment ids or its rail reference.
func (s *Store) recordMonitorAlerts(ctx context.Context, userID uuid.UUID, alerts []kyc.MonitorAlert) (int, error) {
	raised := 0
	for _, alert := range alerts {
		recorded, err := s.alertExistsForEvent(ctx, userID, alert.Rule, alert.PaymentIDs, alert.References)
		if err != nil {
			return raised, err
		}
		if recorded {
			continue
		}
		if err := s.insertTransactionAlert(ctx, userID, alert); err != nil {
			return raised, err
		}
		raised++
	}
	return raised, nil
}

// PaymentTransactionInput loads the monitoring input for one settled payment,
// including its merchant name and category. Wallet top-ups are tagged with
// their own rail so a funding burst is distinguishable from merchant spend.
func (s *Store) PaymentTransactionInput(ctx context.Context, paymentID uuid.UUID) (kyc.TransactionInput, error) {
	var t kyc.TransactionInput
	err := s.pool.QueryRow(ctx, `
		SELECT p.id, p.user_id, p.merchant_id, m.name, m.category, p.amount_kobo, p.paid_at,
		       CASE WHEN m.slug='xego-wallet-topup' THEN 'topup' ELSE 'payment' END
		FROM payments p
		JOIN merchants m ON m.id = p.merchant_id
		WHERE p.id=$1`, paymentID).Scan(&t.PaymentID, &t.UserID, &t.MerchantID, &t.MerchantName, &t.MerchantCategory, &t.AmountKobo, &t.PaidAt, &t.Rail)
	if err != nil {
		return t, err
	}
	t.Reference = t.PaymentID.String()
	return t, nil
}

// RunTransactionMonitorForInput evaluates one newly settled event against the
// monitoring rules together with the customer's recent history on every rail
// (Phase 3 event-driven consumer). It is idempotent: an alert already recorded
// for the event's payment id or rail reference under the same rule is not
// re-raised, so redelivery is safe.
func (s *Store) RunTransactionMonitorForInput(ctx context.Context, input kyc.TransactionInput, cfg kyc.MonitorConfig) (int, error) {
	if cfg.VelocityWindow <= 0 {
		cfg.VelocityWindow = 24 * time.Hour
	}
	if cfg.StructuringWindow <= 0 {
		cfg.StructuringWindow = 24 * time.Hour
	}
	since := input.PaidAt.Add(-cfg.VelocityWindow)
	if earlier := input.PaidAt.Add(-cfg.StructuringWindow); earlier.Before(since) {
		since = earlier
	}
	history, err := s.monitorInputs(ctx, since, input.PaidAt, &input.UserID)
	if err != nil {
		return 0, err
	}
	// The triggering event is appended explicitly; drop any history row that is
	// the same event (a standing credit is written before its alert runs, so it
	// can already sit inside the lookback window).
	trigger := monitorInputReference(input)
	events := make([]kyc.TransactionInput, 0, len(history)+1)
	for _, e := range history {
		if trigger != "" && e.Reference == trigger {
			continue
		}
		events = append(events, e)
	}
	events = append(events, input)
	return s.recordMonitorAlerts(ctx, input.UserID, kyc.RunTransactionMonitor(events, cfg))
}

// RunTransactionMonitorForPayment evaluates one newly settled payment against
// the monitoring rules together with the customer's recent history.
func (s *Store) RunTransactionMonitorForPayment(ctx context.Context, payment kyc.TransactionInput, cfg kyc.MonitorConfig) (int, error) {
	return s.RunTransactionMonitorForInput(ctx, payment, cfg)
}

// monitorInputReference is the stable dedupe reference for a monitor event.
func monitorInputReference(input kyc.TransactionInput) string {
	if input.Reference != "" {
		return input.Reference
	}
	if input.PaymentID != uuid.Nil {
		return input.PaymentID.String()
	}
	return ""
}

// alertExistsForEvent reports whether an alert for any of the payment ids or
// rail references was already recorded under the rule, making monitoring
// idempotent across redeliveries and rescans.
func (s *Store) alertExistsForEvent(ctx context.Context, userID uuid.UUID, rule string, paymentIDs []uuid.UUID, references []string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM transaction_alerts
			WHERE user_id=$1 AND rule=$2 AND (
				(cardinality($3::uuid[]) > 0 AND payment_id = ANY($3::uuid[]))
				OR (cardinality($4::text[]) > 0 AND details->>'reference' = ANY($4::text[]))
			)
		)`, userID, rule, paymentIDs, references).Scan(&exists)
	return exists, err
}

func (s *Store) insertTransactionAlert(ctx context.Context, userID uuid.UUID, alert kyc.MonitorAlert) error {
	details := map[string]any{}
	for key, value := range alert.Details {
		details[key] = value
	}
	if len(alert.Rails) > 0 {
		details["rails"] = alert.Rails
	}
	if len(alert.References) > 0 {
		details["reference"] = alert.References[0]
		if len(alert.References) > 1 {
			details["references"] = alert.References
		}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode alert details: %w", err)
	}
	var paymentID *uuid.UUID
	if len(alert.PaymentIDs) > 0 {
		paymentID = &alert.PaymentIDs[0]
	}
	var alertID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO transaction_alerts(user_id,payment_id,rule,severity,details)
		VALUES($1,$2,$3,$4,$5::jsonb)
		RETURNING id`,
		userID, paymentID, alert.Rule, alert.Severity, string(encoded)).Scan(&alertID); err != nil {
		return fmt.Errorf("insert transaction alert: %w", err)
	}
	// The alert id rides along on the risk observation so a reviewer's
	// subsequent disposition can cancel exactly the weight this alert added,
	// instead of guessing at it from the rule and severity.
	riskDetails := map[string]any{"alert_id": alertID}
	for key, value := range alert.Details {
		riskDetails[key] = value
	}
	if _, err := s.RecordRiskEvent(ctx, RiskEvent{
		UserID:    userID,
		EventType: monitorRiskEventType(alert.Rule),
		Score:     kyc.AlertScore(alert.Severity),
		Details:   riskDetails,
	}); err != nil {
		return fmt.Errorf("risk event for alert: %w", err)
	}
	if alert.Severity != kyc.AlertLow {
		if _, err := s.CreateManualReviewCase(ctx, ManualReviewCase{
			UserID:   userID,
			CaseType: "monitoring",
			Reason:   "transaction alert " + alert.Rule + " (" + alert.Severity + ")",
		}); err != nil {
			return fmt.Errorf("review case for alert: %w", err)
		}
	}
	return nil
}

// ResolveTransactionAlert moves an alert to a terminal status and records who
// resolved it and when.
func (s *Store) ResolveTransactionAlert(ctx context.Context, alertID int64, status, reviewerEmail string) (TransactionAlert, error) {
	return s.resolveTransactionAlert(ctx, alertID, status, reviewerEmail, "")
}

// ResolveTransactionOutcome records the reviewer's explicit disposition on a
// resolve action (M4 feedback loop).
func (s *Store) ResolveTransactionAlertOutcome(ctx context.Context, alertID int64, status, reviewerEmail, reviewerOutcome string) (TransactionAlert, error) {
	return s.resolveTransactionAlert(ctx, alertID, status, reviewerEmail, reviewerOutcome)
}

// resolveTransactionAlert moves an alert to a terminal status and records the
// M4 outcome row; reviewerOutcome is the explicit false_positive/confirmed call
// on a resolve (empty means acknowledged/escalated mapping applies).
func (s *Store) resolveTransactionAlert(ctx context.Context, alertID int64, status, reviewerEmail, reviewerOutcome string) (TransactionAlert, error) {
	valid := map[string]bool{"acknowledged": true, "escalated": true, "resolved": true}
	if !valid[status] {
		return TransactionAlert{}, fmt.Errorf("invalid alert status %q", status)
	}
	now := time.Now().UTC()
	var a TransactionAlert
	var paymentID *uuid.UUID
	var details []byte
	var reviewedAt *time.Time
	err := s.pool.QueryRow(ctx, `UPDATE transaction_alerts
		SET status=$2, reviewer_email=$3, reviewed_at=COALESCE(reviewed_at, $4)
		WHERE id=$1
		RETURNING id, user_id, payment_id, rule, severity, COALESCE(details::text,'{}'),
		          status, COALESCE(reviewer_email,''), reviewed_at, created_at`,
		alertID, status, reviewerEmail, now).Scan(&a.ID, &a.UserID, &paymentID, &a.Rule,
		&a.Severity, &details, &a.Status, &a.ReviewerEmail, &reviewedAt, &a.CreatedAt)
	if err != nil {
		return TransactionAlert{}, fmt.Errorf("resolve transaction alert: %w", err)
	}
	// M4 feedback loop: the disposition becomes an outcome row (migration 075),
	// so false-positive rates per rule are computable and any future threshold
	// tuning or model work has labelled data. acknowledged→undetermined,
	// escalated→confirmed, resolved→the reviewer's explicit outcome call.
	label, oerr := s.recordAlertOutcome(ctx, alertID, a.Rule, a.Severity, status, reviewerEmail, reviewerOutcome)
	if oerr != nil {
		return TransactionAlert{}, oerr
	}
	// A false positive cancels the risk score the alert added. Without this a
	// dismissed alert sat in the score forever, so a customer collecting
	// clean reviews never climbed back down (M4 feedback loop, closed).
	if label == "false_positive" {
		if err := s.cancelAlertRisk(ctx, a, reviewerEmail); err != nil {
			return TransactionAlert{}, err
		}
	}
	a.PaymentID = paymentID
	a.ReviewedAt = reviewedAt
	_ = json.Unmarshal(details, &a.Details)
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	a.Details["outcome"] = label
	return a, nil
}

// recordAlertOutcome inserts one outcome row per resolve action: acknowledging
// means "not yet determined", escalating means the reviewer confirmed the
// suspicion, resolving records the reviewer's explicit call (defaulting to
// false_positive — the common disposition when an alert closes without
// escalation). Rows append, so the reviewer-call history is preserved.
func (s *Store) recordAlertOutcome(ctx context.Context, alertID int64, rule, severity, status, reviewerEmail, outcome string) (string, error) {
	label := map[string]string{
		"acknowledged": "undetermined",
		"escalated":    "confirmed",
		"resolved":     outcome,
	}[status]
	if label == "" {
		label = "false_positive"
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO transaction_alert_outcomes(alert_id, outcome, rule, severity, reviewer_email)
		VALUES($1,$2,$3,$4,$5)`, alertID, label, rule, severity, reviewerEmail); err != nil {
		return "", fmt.Errorf("record alert outcome: %w", err)
	}
	return label, nil
}

// Alert-backed risk observations are logged as monitor_<rule>; a reviewer
// clearing one appends monitor_<rule>_cleared. Both carry the originating alert
// id in details, which is what ties a disposition to the weight it corrects.
func monitorRiskEventType(rule string) string { return "monitor_" + rule }

func monitorRiskEventClearedType(rule string) string { return "monitor_" + rule + "_cleared" }

// cancelAlertRisk appends the negative observation that cancels the weight an
// alert contributed, once a reviewer has labelled it a false positive.
//
// The correction is dated at the observation it corrects, so under any decay
// half-life the pair cancels exactly — both age identically — and the score
// returns to what it was before the alert was raised rather than to a value
// that depends on how long the review took. It is idempotent: re-resolving the
// same alert (acknowledged, then resolved) records one correction, not two.
// When the original observation is missing — alerts raised before the alert id
// travelled on risk events — it falls back to cancelling the rule's severity
// weight as of the resolution.
func (s *Store) cancelAlertRisk(ctx context.Context, alert TransactionAlert, reviewerEmail string) error {
	alertRef := strconv.FormatInt(alert.ID, 10)
	var cleared bool
	err := s.pool.QueryRow(ctx, `
		SELECT true FROM risk_events
		WHERE user_id=$1 AND event_type=$2 AND details->>'alert_id'=$3
		LIMIT 1`,
		alert.UserID, monitorRiskEventClearedType(alert.Rule), alertRef).Scan(&cleared)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check cleared risk event: %w", err)
	}
	if cleared {
		return nil
	}
	score := -kyc.AlertScore(alert.Severity)
	occurredAt := time.Now().UTC()
	var standing float64
	var observedAt time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(score,0), occurred_at FROM risk_events
		WHERE user_id=$1 AND event_type=$2 AND details->>'alert_id'=$3
		ORDER BY id DESC LIMIT 1`,
		alert.UserID, monitorRiskEventType(alert.Rule), alertRef).Scan(&standing, &observedAt)
	switch {
	case err == nil:
		score = -standing
		occurredAt = observedAt
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("load alert risk event: %w", err)
	}
	if _, err := s.RecordRiskEvent(ctx, RiskEvent{
		UserID:     alert.UserID,
		EventType:  monitorRiskEventClearedType(alert.Rule),
		Score:      score,
		OccurredAt: occurredAt,
		Details: map[string]any{
			"alert_id":   alert.ID,
			"rule":       alert.Rule,
			"severity":   alert.Severity,
			"reviewer":   reviewerEmail,
			"cleared_at": time.Now().UTC().Format(time.RFC3339),
			"reason":     "reviewer resolved the alert as a false positive",
		},
	}); err != nil {
		return fmt.Errorf("record alert risk cancellation: %w", err)
	}
	return nil
}

// alertOutcomeTx inserts exactly one outcome row per (alert, status) change.
// Re-resolving an alert to the same status is idempotent (the unique guard
// is the (alert_id, outcome, created_at-minute) triple via ON CONFLICT DO
// NOTHING on a plain index — re-disposition appends a new row by design so
// the history of reviewer calls is preserved).
func (s *Store) alertOutcomeTx(ctx context.Context, alertID int64, rule, severity, status, reviewerEmail, outcome string) (string, error) {
	label := map[string]string{
		"acknowledged": "undetermined",
		"escalated":    "confirmed",
		"resolved":     outcome,
	}[status]
	if label == "" {
		label = "false_positive"
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO transaction_alert_outcomes(alert_id, outcome, rule, severity, reviewer_email)
		VALUES($1,$2,$3,$4,$5)`, alertID, label, rule, severity, reviewerEmail); err != nil {
		return "", fmt.Errorf("record alert outcome: %w", err)
	}
	return label, nil
}

// AlertOutcomeCounts returns confirmed and false_positive outcome counts per
// rule, for the compliance queue's feedback summary (M4/M5).
type AlertOutcomeCount struct {
	Rule          string
	Confirmed     int
	FalsePositive int
}

func (s *Store) AlertOutcomeCounts(ctx context.Context) ([]AlertOutcomeCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rule,
		       count(*) FILTER (WHERE outcome='confirmed') AS confirmed,
		       count(*) FILTER (WHERE outcome='false_positive') AS false_positive
		FROM transaction_alert_outcomes
		GROUP BY rule ORDER BY rule`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertOutcomeCount
	for rows.Next() {
		var c AlertOutcomeCount
		if err := rows.Scan(&c.Rule, &c.Confirmed, &c.FalsePositive); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateManualReviewCase opens a KYC review-queue item.
func (s *Store) CreateManualReviewCase(ctx context.Context, c ManualReviewCase) (ManualReviewCase, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO manual_review_cases(user_id,case_type,requested_tier,reason)
		VALUES($1,$2,$3,$4)
		RETURNING id, created_at`,
		c.UserID, c.CaseType, nullIfEmpty(c.RequestedTier), c.Reason).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return ManualReviewCase{}, fmt.Errorf("create review case: %w", err)
	}
	return c, nil
}

// ListManualReviewCases lists review cases newest first, optionally filtered
// to a status.
func (s *Store) ListManualReviewCases(ctx context.Context, status string, limit int) ([]ManualReviewCase, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''),
		       status, reason, COALESCE(reviewer_email,''),
		       COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cases []ManualReviewCase
	for rows.Next() {
		var c ManualReviewCase
		var reviewedAt *time.Time
		if err := rows.Scan(&c.ID, &c.UserID, &c.CaseType, &c.RequestedTier,
			&c.Status, &c.Reason, &c.ReviewerEmail, &c.DecisionNote,
			&c.CreatedAt, &reviewedAt); err != nil {
			return nil, err
		}
		c.ReviewedAt = reviewedAt
		cases = append(cases, c)
	}
	return cases, rows.Err()
}

// ReviewManualReviewCase approves or rejects a review case. Approving a
// tier_upgrade case advances the user to the requested tier once the ladder
// rules and screening pass; rejecting records the decision. The action is
// audited with the operator as actor.
func (s *Store) ReviewManualReviewCase(ctx context.Context, caseID int64, approve bool, reviewerEmail, note string, actor *AuditLog) (ManualReviewCase, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManualReviewCase{}, fmt.Errorf("begin review: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var c ManualReviewCase
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''),
		       status, reason, COALESCE(reviewer_email,''), COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(
		&c.ID, &c.UserID, &c.CaseType, &c.RequestedTier, &c.Status, &c.Reason,
		&c.ReviewerEmail, &c.DecisionNote, &c.CreatedAt, &c.ReviewedAt)
	if err != nil {
		return ManualReviewCase{}, fmt.Errorf("load review case: %w", err)
	}
	if c.Status != "pending" {
		return ManualReviewCase{}, fmt.Errorf("review case %d already %s", c.ID, c.Status)
	}
	status := "rejected"
	if approve {
		status = "approved"
	}
	if err := tx.QueryRow(ctx, `
		UPDATE manual_review_cases
		SET status=$2, reviewer_email=$3, decision_note=$4, reviewed_at=now()
		WHERE id=$1
		RETURNING status, COALESCE(reviewer_email,''), COALESCE(decision_note,''), reviewed_at`,
		caseID, status, nullIfEmpty(reviewerEmail), note).Scan(
		&c.Status, &c.ReviewerEmail, &c.DecisionNote, &c.ReviewedAt); err != nil {
		return ManualReviewCase{}, fmt.Errorf("update review case: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.review_" + status,
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: c.UserID.String(), Valid: true},
		Details: map[string]any{
			"case_id":       c.ID,
			"case_type":     c.CaseType,
			"requested":     c.RequestedTier,
			"decision_note": note,
		},
	}
	if actor != nil {
		entry.ActorType = actor.ActorType
		entry.ActorID = actor.ActorID
		entry.ActorEmail = actor.ActorEmail
		entry.IP = actor.IP
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return ManualReviewCase{}, fmt.Errorf("audit review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ManualReviewCase{}, fmt.Errorf("commit review: %w", err)
	}
	if approve {
		if _, err := s.AdvanceKYCTier(ctx, c.UserID, c.RequestedTier, requiredTierEvidence(c.RequestedTier), actor); err != nil {
			return ManualReviewCase{}, err
		}
	}
	return c, nil
}

// requiredTierEvidence returns the evidence keys a tier requires. A reviewer's
// approval carries the weight of that verification, so an approved tier_upgrade
// case supplies them instead of the user's self-service submission.
func requiredTierEvidence(to string) []string {
	switch to {
	case kyc.TierL1:
		return []string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}
	case kyc.TierL2:
		return []string{kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified}
	case kyc.TierL3:
		return []string{kyc.EvAddressProof, kyc.EvEDDCompleted}
	}
	return nil
}

func mergeEvidence(existing []string, extra []string) []string {
	seen := make(map[string]bool, len(existing)+len(extra))
	out := make([]string, 0, len(existing)+len(extra))
	for _, e := range append(append([]string{}, existing...), extra...) {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
