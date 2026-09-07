package store

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

// Account types used for allowance enforcement.
const (
	AccountIndividual = "individual"
	AccountBusiness   = "business"
)

// TierLimitRow is one editable (account type, tier, direction) ceiling row.
type TierLimitRow struct {
	ID               int64
	AccountType      string
	Tier             string
	Direction        string
	SingleLimitKobo  int64
	DailyLimitKobo   int64
	MonthlyLimitKobo int64
	UpdatedBy        *uuid.UUID
	UpdatedAt        time.Time
}

// KYBProfile is a merchant's position on the B0-B3 business identity ladder.
type KYBProfile struct {
	MerchantID             uuid.UUID
	Tier                   string
	TierUpdatedAt          time.Time
	Evidence               []string
	LastScreeningDecision  string
	LastScreenAt           *time.Time
	RescreenDue            *time.Time
	ReviewStatus           string
	ReviewedBy             *uuid.UUID
	ReviewedAt             *time.Time
	AdvancementRequest     *KYBAdvancementRequest
	AdvancementRequestedAt *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// KYBAdvancementRequest is the self-service record a merchant holds of which
// evidence supports their next-tier upgrade. The requested tier is always the
// next tier up the ladder (adjacent-only). It is advisory: the tier still only
// moves when an admin approves via AdvanceKYBTier.
type KYBAdvancementRequest struct {
	RequestedTier string   `json:"requested_tier"`
	Evidence      []string `json:"evidence"`
	Note          string   `json:"note,omitempty"`
}

// AllowanceReservation describes a money-in/out movement against one subject.
type AllowanceReservation struct {
	AccountType string
	SubjectID   uuid.UUID
	Direction   string
	Tier        string
	AmountKobo  int64
	Ref         string
}

// ReserveAllowance atomically validates a movement against the subject's tier
// ceilings and records it for the daily/monthly rollups. It is idempotent:
// re-reserving the same Ref is a no-op, so replaying a payment or payout never
// double-counts. Reservations for the same subject and direction are
// serialized with an advisory lock so concurrent attempts cannot each pass
// validation. On a breach it returns an error wrapping kyc.ErrAllowance (with
// a *kyc.LimitError payload); the caller must treat any such error as a
// rejection that stops the transaction.
//
// Callers satisfy the "same transaction as creation" contract by reserving
// before the payment/payout row is created: a rejected reservation prevents
// creation, and a crash after a successful reservation can only over-count
// (the conservative direction), never under-count.
func (s *Store) ReserveAllowance(ctx context.Context, r AllowanceReservation) error {
	if r.AccountType != AccountIndividual && r.AccountType != AccountBusiness {
		return fmt.Errorf("invalid allowance account type %q", r.AccountType)
	}
	if !kyc.ValidDirection(r.Direction) {
		return fmt.Errorf("invalid allowance direction %q", r.Direction)
	}
	if r.Tier == "" {
		r.Tier = kyc.TierL0
		if r.AccountType == AccountBusiness {
			r.Tier = kyc.TierB0
		}
	}
	if r.AmountKobo <= 0 {
		return errors.New("allowance amount must be positive")
	}
	if len(r.Ref) == 0 {
		return errors.New("allowance reservation requires a reference")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin allowance reserve: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// pg_advisory_xact_lock takes a bigint; the key string is hashed so the
	// advisory lock still serializes per (subject, direction) without the
	// text->bigint cast error that plain text arguments raise on Postgres.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		allowanceLockKey(r.AccountType, r.SubjectID, r.Direction)); err != nil {
		return fmt.Errorf("lock allowance subject: %w", err)
	}

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM allowance_usage WHERE transaction_ref=$1)`, r.Ref).
		Scan(&exists); err != nil {
		return fmt.Errorf("lookup allowance reservation: %w", err)
	}
	if exists {
		return tx.Commit(ctx)
	}

	now := time.Now()
	dayStart, monthStart := kyc.DayWindowStart(now), kyc.MonthWindowStart(now)
	usedDay, err := sumAllowanceUsageTx(ctx, tx, r.AccountType, r.SubjectID, r.Direction, dayStart)
	if err != nil {
		return err
	}
	usedMonth, err := sumAllowanceUsageTx(ctx, tx, r.AccountType, r.SubjectID, r.Direction, monthStart)
	if err != nil {
		return err
	}
	limits, err := tierLimitsTx(ctx, tx, r.AccountType, r.Tier, r.Direction)
	if err != nil {
		return err
	}
	if err := kyc.Validate(limits, r.AmountKobo, usedDay, usedMonth); err != nil {
		return fmt.Errorf("allowance rejected: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO allowance_usage(account_type, subject_id, direction, tier_at_time, amount_kobo, transaction_ref)
		VALUES($1,$2,$3,$4,$5,$6)`,
		r.AccountType, r.SubjectID, r.Direction, r.Tier, r.AmountKobo, r.Ref); err != nil {
		return fmt.Errorf("record allowance usage: %w", err)
	}
	return tx.Commit(ctx)
}

// ReleaseAllowance removes a previously reserved movement (failed, expired,
// abandoned, or refunded payment; declined payout). Idempotent.
func (s *Store) ReleaseAllowance(ctx context.Context, ref string) error {
	if len(ref) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM allowance_usage WHERE transaction_ref=$1`, ref)
	return err
}

// AllowanceUsage returns the sum of movements recorded for a subject and
// direction since the given instant.
func (s *Store) AllowanceUsage(ctx context.Context, accountType string, subjectID uuid.UUID, direction string, since time.Time) (int64, error) {
	return sumAllowanceUsageTx(ctx, s.pool, accountType, subjectID, direction, since)
}

// TierLimits resolves a (account type, tier, direction) ceiling, in kobo.
func (s *Store) TierLimits(ctx context.Context, accountType, tier, direction string) (kyc.Limits, error) {
	return tierLimitsTx(ctx, s.pool, accountType, tier, direction)
}

// ListTierLimits returns every editable ceiling row for the admin console.
func (s *Store) ListTierLimits(ctx context.Context) ([]TierLimitRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, account_type, tier, direction, single_limit_kobo, daily_limit_kobo,
		       monthly_limit_kobo, updated_by, updated_at
		FROM kyc_tier_limits ORDER BY account_type, tier, direction`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var limits []TierLimitRow
	for rows.Next() {
		var l TierLimitRow
		if err := rows.Scan(&l.ID, &l.AccountType, &l.Tier, &l.Direction,
			&l.SingleLimitKobo, &l.DailyLimitKobo, &l.MonthlyLimitKobo,
			&l.UpdatedBy, &l.UpdatedAt); err != nil {
			return nil, err
		}
		limits = append(limits, l)
	}
	return limits, rows.Err()
}

// UpdateTierLimit rewrites a ceiling row and records the admin operator who
// changed it. Single must not exceed daily, daily must not exceed monthly, and
// all three must be non-negative.
func (s *Store) UpdateTierLimit(ctx context.Context, id int64, single, daily, monthly int64, updatedBy *uuid.UUID) (TierLimitRow, error) {
	if single < 0 || daily < 0 || monthly < 0 {
		return TierLimitRow{}, errors.New("tier limit values must be non-negative")
	}
	if daily > 0 && single > daily {
		return TierLimitRow{}, errors.New("single limit cannot exceed the daily limit")
	}
	if monthly > 0 && daily > monthly {
		return TierLimitRow{}, errors.New("daily limit cannot exceed the monthly limit")
	}
	var l TierLimitRow
	err := s.pool.QueryRow(ctx, `
		UPDATE kyc_tier_limits
		SET single_limit_kobo=$2, daily_limit_kobo=$3, monthly_limit_kobo=$4,
		    updated_by=$5, updated_at=now()
		WHERE id=$1
		RETURNING id, account_type, tier, direction, single_limit_kobo, daily_limit_kobo,
		          monthly_limit_kobo, updated_by, updated_at`,
		id, single, daily, monthly, updatedBy).Scan(
		&l.ID, &l.AccountType, &l.Tier, &l.Direction, &l.SingleLimitKobo,
		&l.DailyLimitKobo, &l.MonthlyLimitKobo, &l.UpdatedBy, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TierLimitRow{}, errors.New("tier limit not found")
	}
	return l, err
}

// ---------------------------------------------------------------------------
// Business KYB ladder
// ---------------------------------------------------------------------------

// EnsureKYBProfile creates the baseline B0 profile row for the merchant if
// none exists and returns the current profile.
func (s *Store) EnsureKYBProfile(ctx context.Context, merchantID uuid.UUID) (KYBProfile, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO business_kyb_profiles(merchant_id)
		VALUES($1)
		ON CONFLICT (merchant_id) DO NOTHING`, merchantID); err != nil {
		return KYBProfile{}, fmt.Errorf("ensure kyb profile: %w", err)
	}
	return s.KYBProfileByMerchant(ctx, merchantID)
}

// KYBProfileByMerchant resolves a merchant's KYB ladder position.
func (s *Store) KYBProfileByMerchant(ctx context.Context, merchantID uuid.UUID) (KYBProfile, error) {
	var p KYBProfile
	var evidence []byte
	var advancement []byte
	err := s.pool.QueryRow(ctx, `
		SELECT merchant_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		       COALESCE(last_screening_decision,''), last_screen_at, rescreen_due,
		       review_status, reviewed_by, reviewed_at,
		       COALESCE(advancement_request::text,''), advancement_requested_at,
		       created_at, updated_at
		FROM business_kyb_profiles WHERE merchant_id=$1`, merchantID).Scan(
		&p.MerchantID, &p.Tier, &p.TierUpdatedAt, &evidence,
		&p.LastScreeningDecision, &p.LastScreenAt, &p.RescreenDue,
		&p.ReviewStatus, &p.ReviewedBy, &p.ReviewedAt,
		&advancement, &p.AdvancementRequestedAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return KYBProfile{}, err
	}
	_ = json.Unmarshal(evidence, &p.Evidence)
	if p.Evidence == nil {
		p.Evidence = []string{}
	}
	if len(advancement) > 0 && strings.TrimSpace(string(advancement)) != "" {
		var req KYBAdvancementRequest
		if err := json.Unmarshal(advancement, &req); err == nil {
			p.AdvancementRequest = &req
		}
	}
	return p, nil
}

// ListKYBProfiles returns all merchant KYB positions for the admin page,
// newest changes first.
func (s *Store) ListKYBProfiles(ctx context.Context, limit int) ([]KYBProfile, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT merchant_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		       COALESCE(last_screening_decision,''), review_status, reviewed_by, reviewed_at,
		       COALESCE(advancement_request::text,''), advancement_requested_at,
		       created_at, updated_at
		FROM business_kyb_profiles ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []KYBProfile
	for rows.Next() {
		var p KYBProfile
		var evidence []byte
		var advancement []byte
		if err := rows.Scan(&p.MerchantID, &p.Tier, &p.TierUpdatedAt, &evidence,
			&p.LastScreeningDecision, &p.LastScreenAt, &p.RescreenDue,
			&p.ReviewStatus, &p.ReviewedBy, &p.ReviewedAt,
			&advancement, &p.AdvancementRequestedAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &p.Evidence)
		if p.Evidence == nil {
			p.Evidence = []string{}
		}
		if len(advancement) > 0 && strings.TrimSpace(string(advancement)) != "" {
			var req KYBAdvancementRequest
			if err := json.Unmarshal(advancement, &req); err == nil {
				p.AdvancementRequest = &req
			}
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// BusinessEvidenceFor returns the evidence key a one-step business advancement
// to the given tier requires. Empty when to is not the target of an adjacent
// step (B0 has no entry evidence).
func BusinessEvidenceFor(to string) string {
	switch to {
	case kyc.TierB1:
		return kyc.EvBusinessDocs
	case kyc.TierB2:
		return kyc.EvBusinessBank
	case kyc.TierB3:
		return kyc.EvBusinessEDD
	}
	return ""
}

// NextBusinessTier returns the next tier above the current one on the B0-B3
// ladder, or "" when the merchant is already at the top (B3).
func NextBusinessTier(current string) string {
	switch current {
	case kyc.TierB0:
		return kyc.TierB1
	case kyc.TierB1:
		return kyc.TierB2
	case kyc.TierB2:
		return kyc.TierB3
	}
	return ""
}

// RequestKYBAdvancement records a merchant's self-service upgrade request for
// the next tier up. Merchant-submitted evidence is stored as an advisory
// request only - the tier still only moves when an admin approves it via
// AdvanceKYBTier. Passing nil evidence clears/rejects an existing request.
// It is idempotent: a duplicate request for the same next tier replaces the
// record and refreshes the requested_at timestamp.
func (s *Store) RequestKYBAdvancement(ctx context.Context, merchantID uuid.UUID, evidence []string, note string) (KYBProfile, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO business_kyb_profiles(merchant_id) VALUES($1)
		ON CONFLICT (merchant_id) DO NOTHING`, merchantID); err != nil {
		return KYBProfile{}, fmt.Errorf("ensure kyb profile for advancement request: %w", err)
	}
	current, err := s.KYBProfileByMerchant(ctx, merchantID)
	if err != nil {
		return KYBProfile{}, fmt.Errorf("load kyb profile for advancement request: %w", err)
	}
	next := NextBusinessTier(current.Tier)
	if next == "" {
		return KYBProfile{}, errors.New("kyb: merchant is already at the highest tier (B3)")
	}
	if evidence == nil {
		if _, err := s.pool.Exec(ctx, `
			UPDATE business_kyb_profiles
			SET advancement_request = NULL, advancement_requested_at = NULL,
			    review_status = 'none', updated_at = now()
			WHERE merchant_id = $1`, merchantID); err != nil {
			return KYBProfile{}, fmt.Errorf("clear kyb advancement request: %w", err)
		}
		return s.KYBProfileByMerchant(ctx, merchantID)
	}
	sanitized := make([]string, 0, len(evidence))
	seen := map[string]bool{}
	for _, e := range evidence {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		sanitized = append(sanitized, e)
	}
	payload, err := json.Marshal(KYBAdvancementRequest{
		RequestedTier: next,
		Evidence:      sanitized,
		Note:          note,
	})
	if err != nil {
		return KYBProfile{}, fmt.Errorf("marshal kyb advancement request: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE business_kyb_profiles
		SET advancement_request = $2::jsonb,
		    advancement_requested_at = now(),
		    review_status = 'pending',
		    updated_at = now()
		WHERE merchant_id = $1`, merchantID, string(payload)); err != nil {
		return KYBProfile{}, fmt.Errorf("record kyb advancement request: %w", err)
	}
	return s.KYBProfileByMerchant(ctx, merchantID)
}

// RecordKYBScreening persists a business sanctions/PEP outcome and writes the
// decision through to the KYB profile so the ladder can gate advancement. The
// rescreen due date is advanced by rescreenPeriod (0 keeps the caller value).
func (s *Store) RecordKYBScreening(ctx context.Context, merchantID uuid.UUID, decision string, matchedNames []string, rescreenPeriod time.Duration) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO business_kyb_profiles(merchant_id) VALUES($1)
		ON CONFLICT (merchant_id) DO NOTHING`, merchantID); err != nil {
		return fmt.Errorf("ensure kyb profile for screening: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin kyb screening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now()
	var due *time.Time
	if rescreenPeriod > 0 {
		d := now.Add(rescreenPeriod)
		due = &d
	}
	if _, err := tx.Exec(ctx, `
		UPDATE business_kyb_profiles
		SET last_screening_decision=$2, last_screen_at=COALESCE(last_screen_at,$3),
		    rescreen_due=$4, updated_at=now()
		WHERE merchant_id=$1`, merchantID, decision, now, due); err != nil {
		return fmt.Errorf("update kyb profile screening: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyb.screening_recorded",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		Details: map[string]any{
			"provider":     "simulated",
			"decision":     decision,
			"matched":      matchedNames,
			"rescreen_due": due,
		},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return fmt.Errorf("audit kyb screening: %w", err)
	}
	return tx.Commit(ctx)
}

// BusinessKYBProfile lists a merchant's KYB profile with its name, for worker
// and notification use.
type BusinessKYBProfile struct {
	KYBProfile
	MerchantName string
}

// BusinessKYBProfilesDueForRescreen lists businesses whose most recent
// screening result is older than the cutoff or entirely absent, newest first.
func (s *Store) BusinessKYBProfilesDueForRescreen(ctx context.Context, cutoff time.Time, limit int) ([]BusinessKYBProfile, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.merchant_id, p.tier, p.tier_updated_at, COALESCE(p.evidence::text,'[]'),
		       COALESCE(p.last_screening_decision,''), p.last_screen_at, p.rescreen_due,
		       p.review_status, p.reviewed_by, p.reviewed_at, p.created_at, p.updated_at,
		       COALESCE(m.name,'')
		FROM business_kyb_profiles p
		LEFT JOIN merchants m ON m.id = p.merchant_id
		WHERE p.rescreen_due IS NULL OR p.rescreen_due < now()
		ORDER BY COALESCE(p.last_screen_at, p.updated_at) ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []BusinessKYBProfile
	for rows.Next() {
		var p BusinessKYBProfile
		var evidence []byte
		if err := rows.Scan(&p.MerchantID, &p.Tier, &p.TierUpdatedAt, &evidence,
			&p.LastScreeningDecision, &p.LastScreenAt, &p.RescreenDue,
			&p.ReviewStatus, &p.ReviewedBy, &p.ReviewedAt, &p.CreatedAt, &p.UpdatedAt,
			&p.MerchantName); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &p.Evidence)
		if p.Evidence == nil {
			p.Evidence = []string{}
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// AdvanceKYBTier moves a merchant to exactly the next business tier after
// validating the ladder rules and required evidence. Advancing beyond the B0
// baseline requires a non-blocked screening decision. The transition is
// audited with the merchant as the resource.
func (s *Store) AdvanceKYBTier(ctx context.Context, merchantID uuid.UUID, to string, evidence []string, actor *AuditLog) (KYBProfile, error) {
	profile, err := s.EnsureKYBProfile(ctx, merchantID)
	if err != nil {
		return KYBProfile{}, err
	}
	if err := kyc.CanAdvanceBusiness(profile.Tier, to, evidence); err != nil {
		return KYBProfile{}, err
	}
	if kyc.BusinessOrder(to) >= 1 && kyc.BlockedByScreening(profile.LastScreeningDecision) {
		return KYBProfile{}, fmt.Errorf("advance to %s blocked by screening decision %q", to, profile.LastScreeningDecision)
	}
	merged := mergeEvidence(profile.Evidence, evidence)
	evidenceJSON, _ := json.Marshal(merged)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYBProfile{}, fmt.Errorf("begin kyb advance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var storedEvidence []byte
	if err := tx.QueryRow(ctx, `
		UPDATE business_kyb_profiles
		SET tier=$2, tier_updated_at=now(), evidence=$3::jsonb,
		    advancement_request=NULL, advancement_requested_at=NULL,
		    review_status='approved', updated_at=now()
		WHERE merchant_id=$1
		RETURNING merchant_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		          COALESCE(last_screening_decision,''), review_status, reviewed_by, reviewed_at,
		          created_at, updated_at`,
		merchantID, to, string(evidenceJSON)).Scan(
		&profile.MerchantID, &profile.Tier, &profile.TierUpdatedAt, &storedEvidence,
		&profile.LastScreeningDecision, &profile.ReviewStatus, &profile.ReviewedBy,
		&profile.ReviewedAt, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return KYBProfile{}, fmt.Errorf("advance kyb tier: %w", err)
	}
	profile.Evidence = merged
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyb.tier_advanced",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		Details: map[string]any{
			"from_tier": profile.Tier,
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
		return KYBProfile{}, fmt.Errorf("audit kyb advance: %w", err)
	}
	// W1: KYB verification (any tier advancement past the B0 baseline) is the
	// business-account milestone — the merchant's business wallet is created
	// (or returned) active in the same transaction.
	if _, err := s.ensureWalletTx(ctx, tx, WalletOwnerBusiness, merchantID, "", WalletStatusActive); err != nil {
		return KYBProfile{}, fmt.Errorf("ensure business wallet: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KYBProfile{}, fmt.Errorf("commit kyb advance: %w", err)
	}
	// Re-read the profile so the returned struct reflects the post-update row:
	// the UPDATE clears advancement_request and marks the tier approved, but
	// the RETURNING above does not carry those columns, so without a re-read
	// callers would see a stale pending request.
	return s.KYBProfileByMerchant(ctx, merchantID)
}

// DowngradeKYBTier moves a merchant to a lower business tier and audits the
// change.
func (s *Store) DowngradeKYBTier(ctx context.Context, merchantID uuid.UUID, to, reason string, actor *AuditLog) (KYBProfile, error) {
	profile, err := s.EnsureKYBProfile(ctx, merchantID)
	if err != nil {
		return KYBProfile{}, err
	}
	if err := kyc.CanDowngradeBusiness(profile.Tier, to); err != nil {
		return KYBProfile{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYBProfile{}, fmt.Errorf("begin kyb downgrade: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var storedEvidence []byte
	if err := tx.QueryRow(ctx, `
		UPDATE business_kyb_profiles
		SET tier=$2, tier_updated_at=now(), review_status='none', reviewed_by=NULL, reviewed_at=NULL,
		    advancement_request=NULL, advancement_requested_at=NULL, updated_at=now()
		WHERE merchant_id=$1
		RETURNING merchant_id, tier, tier_updated_at, COALESCE(evidence::text,'[]'),
		          COALESCE(last_screening_decision,''), review_status, reviewed_by, reviewed_at,
		          created_at, updated_at`,
		merchantID, to).Scan(
		&profile.MerchantID, &profile.Tier, &profile.TierUpdatedAt, &storedEvidence,
		&profile.LastScreeningDecision, &profile.ReviewStatus, &profile.ReviewedBy,
		&profile.ReviewedAt, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return KYBProfile{}, fmt.Errorf("downgrade kyb tier: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyb.tier_downgraded",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		Details: map[string]any{
			"from_tier": profile.Tier,
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
		return KYBProfile{}, fmt.Errorf("audit kyb downgrade: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KYBProfile{}, fmt.Errorf("commit kyb downgrade: %w", err)
	}
	return profile, nil
}

// ReviewKYBProfile approves or rejects a merchant's KYB position and records
// the reviewer. It does not move the tier; that is AdvanceKYBTier's job.
func (s *Store) ReviewKYBProfile(ctx context.Context, merchantID uuid.UUID, approve bool, reviewedBy *uuid.UUID, note string) error {
	status := "approved"
	if !approve {
		status = "rejected"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE business_kyb_profiles
		SET review_status=$2, reviewed_by=$3, updated_at=now()
		WHERE merchant_id=$1`, merchantID, status, reviewedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("kyb profile not found")
	}
	// W1: approving the merchant's KYB position integrates their business
	// wallet (created active), mirroring AdvanceKYBTier.
	if approve {
		if _, err := s.EnsureBusinessWallet(ctx, merchantID, ""); err != nil {
			return fmt.Errorf("ensure business wallet: %w", err)
		}
	}
	_ = note
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func allowanceLockKey(accountType string, subjectID uuid.UUID, direction string) string {
	return fmt.Sprintf("allow:%s:%s:%s", accountType, subjectID, direction)
}

type summable interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func sumAllowanceUsageTx(ctx context.Context, q summable, accountType string, subjectID uuid.UUID, direction string, since time.Time) (int64, error) {
	var total int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_kobo),0) FROM allowance_usage
		WHERE account_type=$1 AND subject_id=$2 AND direction=$3 AND recorded_at >= $4`,
		accountType, subjectID, direction, since).Scan(&total)
	return total, err
}

func tierLimitsTx(ctx context.Context, q summable, accountType, tier, direction string) (kyc.Limits, error) {
	var l kyc.Limits
	err := q.QueryRow(ctx, `
		SELECT single_limit_kobo, daily_limit_kobo, monthly_limit_kobo
		FROM kyc_tier_limits WHERE account_type=$1 AND tier=$2 AND direction=$3`,
		accountType, tier, direction).Scan(&l.SingleLimitKobo, &l.DailyLimitKobo, &l.MonthlyLimitKobo)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, fmt.Errorf("no tier limits configured for %s/%s/%s", accountType, tier, direction)
	}
	return l, err
}
