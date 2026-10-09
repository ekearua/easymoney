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
	"whatsapp-payment-demo/internal/ports"
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
// AllowanceReservation describes one money movement to validate and record.
// RiskBand optionally carries the subject's current KYC risk band (low/
// medium/high): a high band caps the single-transaction ceiling at
// RiskHighSingleLimitKobo (M3 Trust Fabric step-up), so a profile under
// active risk cannot move large amounts regardless of tier. Zero means the
// caller has no band — callers that load the KYC profile anyway pass it;
// business allowances pass the merchant profile's band.
type AllowanceReservation struct {
	AccountType             string
	SubjectID               uuid.UUID
	Direction               string
	Tier                    string
	AmountKobo              int64
	Ref                     string
	RiskBand                string
	RiskHighSingleLimitKobo int64
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
// ErrAllowanceFrozen means the subject has an active allowance freeze
// raised from a confirmed network case; new reservations are refused in
// both directions until compliance lifts it.
var ErrAllowanceFrozen = errors.New("allowances are frozen pending compliance review")

// FreezeAllowance raises an active freeze on a subject (one per subject —
// a second freeze on an already-frozen subject is a no-op), recording the
// reviewer and the network case that motivated it. Idempotent.
func (s *Store) FreezeAllowance(ctx context.Context, accountType string, subjectID uuid.UUID, frozenBy string, caseID int64, reason string) error {
	if accountType != AccountIndividual && accountType != AccountBusiness {
		return fmt.Errorf("invalid allowance account type %q", accountType)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO allowance_freezes(account_type, subject_id, frozen_by, case_id, reason)
		VALUES($1, $2, $3, NULLIF($4, 0), $5)
		ON CONFLICT (account_type, subject_id) WHERE unfrozen_at IS NULL DO NOTHING`,
		accountType, subjectID, frozenBy, caseID, reason)
	return err
}

// UnfreezeAllowance lifts every active freeze on a subject, recording who
// lifted it. Idempotent; returns whether a freeze was actually lifted.
func (s *Store) UnfreezeAllowance(ctx context.Context, accountType string, subjectID uuid.UUID, unfrozenBy string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE allowance_freezes SET unfrozen_at=now(), unfrozen_by=$3
		WHERE account_type=$1 AND subject_id=$2 AND unfrozen_at IS NULL`,
		accountType, subjectID, unfrozenBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SubjectIsFrozen reports whether an active freeze blocks the subject.
func (s *Store) SubjectIsFrozen(ctx context.Context, accountType string, subjectID uuid.UUID) (bool, error) {
	var frozen bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM allowance_freezes WHERE account_type=$1 AND subject_id=$2 AND unfrozen_at IS NULL)`,
		accountType, subjectID).Scan(&frozen)
	return frozen, err
}

// NetworkCaseMembers resolves a case's member set to (account_type, id)
// pairs for freeze/unfreeze sweeps.
func (s *Store) NetworkCaseMembers(ctx context.Context, c NetworkCase) ([]struct {
	AccountType string
	SubjectID   uuid.UUID
}, error) {
	out := make([]struct {
		AccountType string
		SubjectID   uuid.UUID
	}, 0, len(c.Members))
	for _, m := range c.Members {
		if id, err := uuid.Parse(m); err == nil {
			out = append(out, struct {
				AccountType string
				SubjectID   uuid.UUID
			}{AccountIndividual, id})
		}
	}
	return out, nil
}

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

	// Trust Fabric network enforcement: an active freeze (raised when a
	// network case covering this subject was resolved as confirmed) blocks
	// new reservations in both directions, inside the same validated
	// transaction as the tier ceilings.
	var frozen bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM allowance_freezes WHERE account_type=$1 AND subject_id=$2 AND unfrozen_at IS NULL)`,
		r.AccountType, r.SubjectID).Scan(&frozen); err != nil {
		return fmt.Errorf("check allowance freeze: %w", err)
	}
	if frozen {
		return ErrAllowanceFrozen
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
	// M3 decision layer: the persisted risk band finally bites. A high-band
	// subject gets its single-transaction ceiling capped at the configured
	// step-up limit inside the same validated transaction, so the cap is
	// exactly as authoritative — and exactly as replay-safe — as the tier
	// ceilings themselves. A zero or negative cap disables the step-up.
	if r.RiskBand == kyc.RiskHigh && r.RiskHighSingleLimitKobo > 0 &&
		(limits.SingleLimitKobo == 0 || limits.SingleLimitKobo > r.RiskHighSingleLimitKobo) {
		limits.SingleLimitKobo = r.RiskHighSingleLimitKobo
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
		       COALESCE(last_screening_decision,''), last_screen_at, rescreen_due,
		       review_status, reviewed_by, reviewed_at,
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

// KYBDocuments is the structured set of corporate documents a merchant
// supplies with a self-service upgrade request (CAC, TIN, beneficial owners,
// business address, SCUML, DNFBP self-certification).
// KYB document review statuses.
const (
	// KYBDocumentVerified means a reviewer inspected the submitted document and
	// accepted it as evidence for its key.
	KYBDocumentVerified = "verified"
	// KYBDocumentRejected means a reviewer inspected it and refused it.
	KYBDocumentRejected = "rejected"
)

// KYBDocumentReview is one reviewer disposition on a single submitted piece of
// corporate evidence. Only a review makes a document evidence: a document on
// file is a claim, and "verified" has to name a person and a time.
type KYBDocumentReview struct {
	Status     string    `json:"status"`
	Reviewer   string    `json:"reviewer"`
	Note       string    `json:"note,omitempty"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

// KYBDocumentVerification is a third-party check result on one document (a
// corporate registry, a tax authority, geocoding, or the browser's reported
// position). It is advisory: it shows the operator what an outside source said
// so their disposition is informed, but it never advances a tier by itself.
type KYBDocumentVerification struct {
	Status      string    `json:"status"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref,omitempty"`
	MatchName   bool      `json:"match_name"`
	Message     string    `json:"message,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
}

// KYBDocumentAuthenticity is a provider verdict on whether one submitted
// document is genuine (spec G12). Unlike the advisory verifications map it is
// load-bearing: the corporate hard gate counts a reviewer-verified key only
// when its authenticity verdict is "genuine".
type KYBDocumentAuthenticity struct {
	Status      string    `json:"status"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref,omitempty"`
	Confidence  float64   `json:"confidence,omitempty"`
	FaceMatched bool      `json:"face_matched,omitempty"`
	Message     string    `json:"message,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
}

// KYBDocuments is a merchant's self-service corporate evidence: what they typed
// or uploaded, where they were when they submitted, the reviewer dispositions,
// and the third-party verification proposals. The *_document fields hold
// extracted text (OCR output or the typed value), never a raw uploaded file.
type KYBDocuments struct {
	MerchantID       uuid.UUID
	CACRegNumber     string
	TIN              string
	BeneficialOwners string
	BusinessAddress  string
	SCUMLNumber      string
	DNFBP            bool

	// CACDocument / TINDocument / AddressDocument are the readings of the
	// uploaded certificates and utility bill.
	CACDocument     string
	TINDocument     string
	AddressDocument string

	// Location is the position the merchant confirmed in the browser, with the
	// visitor's permission, at submission time.
	LocationLat        *float64
	LocationLng        *float64
	LocationAccuracyM  *float64
	LocationCapturedAt *time.Time
	LocationStatus     string
	LocationProvider   string

	Reviews       map[string]KYBDocumentReview
	Verifications map[string]KYBDocumentVerification
	Authenticity  map[string]KYBDocumentAuthenticity

	RecordedAt time.Time
	UpdatedAt  time.Time
}

// DocumentForEvidence returns the submitted document text that backs an
// evidence key, or "" when nothing was submitted for it. It is the single
// mapping from the corporate hard gate's keys onto the merchant's documents.
func (d KYBDocuments) DocumentForEvidence(key string) string {
	switch key {
	case kyc.EvCACVerified:
		return firstNonBlank(d.CACDocument, d.CACRegNumber)
	case kyc.EvTINVerified:
		return firstNonBlank(d.TINDocument, d.TIN)
	case kyc.EvUBOCleared:
		return d.BeneficialOwners
	case kyc.EvLocationVerified:
		return firstNonBlank(d.AddressDocument, d.BusinessAddress)
	case kyc.EvSCUMLSubmitted:
		return d.SCUMLNumber
	}
	return ""
}

// DeriveKYBEvidence returns the evidence keys the merchant's submitted
// documents justify. It is the honest input to an advancement request: a key
// appears only when a document was actually supplied, never because a tier
// happens to require it.
func DeriveKYBEvidence(d KYBDocuments) []string {
	out := make([]string, 0, 5)
	for _, key := range BusinessEvidenceRequiredFor(kyc.TierB1) {
		if strings.TrimSpace(d.DocumentForEvidence(key)) != "" {
			out = append(out, key)
		}
	}
	return out
}

// VerifiedKYBEvidence returns the evidence keys that are backed by a submitted
// document, confirmed by a reviewer, AND judged genuine by the authenticity
// provider. This is what the corporate hard gate accepts at B0 -> B1: a
// self-asserted key cannot appear here, and neither can a reviewer-verified
// document the provider refused or could not judge (spec G12).
func VerifiedKYBEvidence(d KYBDocuments) []string {
	submitted := DeriveKYBEvidence(d)
	out := make([]string, 0, len(submitted))
	for _, key := range submitted {
		review, reviewed := d.Reviews[key]
		auth, judged := d.Authenticity[key]
		if reviewed && review.Status == KYBDocumentVerified && judged && auth.Status == ports.AuthenticityGenuine {
			out = append(out, key)
		}
	}
	return out
}

// UpsertKYBDocuments records (or replaces) the structured corporate documents
// for a merchant. Callers record the documents together with the advisory
// advancement request they accompany.
//
// Replacing a document invalidates every prior reviewer disposition: a CAC
// certificate that has since been swapped out is not the certificate the
// reviewer verified, so the reviews and verification proposals reset with the
// change. An identical resubmission keeps them.
func (s *Store) UpsertKYBDocuments(ctx context.Context, merchantID uuid.UUID, documents KYBDocuments) error {
	documents.MerchantID = merchantID
	reviews, verifications := documents.Reviews, documents.Verifications
	authenticity := documents.Authenticity
	if existing, err := s.KYBDocumentsByMerchant(ctx, merchantID); err == nil {
		reviews, verifications = existing.Reviews, existing.Verifications
		authenticity = existing.Authenticity
		if documentsChanged(existing, documents) {
			reviews, verifications, authenticity = nil, nil, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load existing kyb documents: %w", err)
	}
	reviewsJSON, err := json.Marshal(orEmptyReviews(reviews))
	if err != nil {
		return fmt.Errorf("encode kyb document reviews: %w", err)
	}
	verificationsJSON, err := json.Marshal(orEmptyVerifications(verifications))
	if err != nil {
		return fmt.Errorf("encode kyb document verifications: %w", err)
	}
	authenticityJSON, err := json.Marshal(orEmptyAuthenticity(authenticity))
	if err != nil {
		return fmt.Errorf("encode kyb document authenticity: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO business_kyb_documents(merchant_id, cac_reg_number, tin,
			beneficial_owners, business_address, scuml_number, dnfbp,
			cac_document, tin_document, address_document, reviews, verifications, authenticity)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12::jsonb,$13::jsonb)
		ON CONFLICT (merchant_id) DO UPDATE
		SET cac_reg_number=EXCLUDED.cac_reg_number,
		    tin=EXCLUDED.tin,
		    beneficial_owners=EXCLUDED.beneficial_owners,
		    business_address=EXCLUDED.business_address,
		    scuml_number=EXCLUDED.scuml_number,
		    dnfbp=EXCLUDED.dnfbp,
		    cac_document=EXCLUDED.cac_document,
		    tin_document=EXCLUDED.tin_document,
		    address_document=EXCLUDED.address_document,
		    reviews=EXCLUDED.reviews,
		    verifications=EXCLUDED.verifications,
		    authenticity=EXCLUDED.authenticity,
		    updated_at=now()`,
		merchantID, documents.CACRegNumber, documents.TIN, documents.BeneficialOwners,
		documents.BusinessAddress, documents.SCUMLNumber, documents.DNFBP,
		documents.CACDocument, documents.TINDocument, documents.AddressDocument,
		string(reviewsJSON), string(verificationsJSON), string(authenticityJSON))
	return err
}

// RecordKYBLocation stores the coordinates the merchant confirmed in the
// browser (with the visitor's permission) and the outcome of comparing them
// with the declared operating address. It leaves reviewer dispositions alone:
// a re-capture is a new proposal to check, not a new document to verify.
func (s *Store) RecordKYBLocation(ctx context.Context, merchantID uuid.UUID, lat, lng, accuracyM float64, status, provider string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO business_kyb_documents(merchant_id) VALUES($1)
		ON CONFLICT (merchant_id) DO NOTHING`, merchantID); err != nil {
		return fmt.Errorf("ensure kyb documents for location: %w", err)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE business_kyb_documents
		SET location_lat=$2, location_lng=$3, location_accuracy_m=$4,
		    location_captured_at=now(), location_status=$5, location_provider=$6,
		    updated_at=now()
		WHERE merchant_id=$1`, merchantID, lat, lng, accuracyM, status, provider)
	return err
}

// ReviewKYBDocument records a reviewer's disposition on one submitted
// corporate document and audits it. It refuses to review a document the
// merchant never submitted, so a disposition always refers to something real.
func (s *Store) ReviewKYBDocument(ctx context.Context, merchantID uuid.UUID, key, status, reviewerEmail, note string) (KYBDocuments, error) {
	if status != KYBDocumentVerified && status != KYBDocumentRejected {
		return KYBDocuments{}, fmt.Errorf("invalid document review status %q", status)
	}
	d, err := s.KYBDocumentsByMerchant(ctx, merchantID)
	if err != nil {
		return KYBDocuments{}, fmt.Errorf("load kyb documents to review: %w", err)
	}
	if strings.TrimSpace(d.DocumentForEvidence(key)) == "" {
		return KYBDocuments{}, fmt.Errorf("no %q document is on file to review", key)
	}
	reviews := d.Reviews
	if reviews == nil {
		reviews = map[string]KYBDocumentReview{}
	}
	reviews[key] = KYBDocumentReview{
		Status:     status,
		Reviewer:   reviewerEmail,
		Note:       note,
		ReviewedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(reviews)
	if err != nil {
		return KYBDocuments{}, fmt.Errorf("encode document review: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return KYBDocuments{}, fmt.Errorf("begin document review: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE business_kyb_documents SET reviews=$2::jsonb, updated_at=now()
		WHERE merchant_id=$1`, merchantID, string(payload)); err != nil {
		return KYBDocuments{}, fmt.Errorf("record document review: %w", err)
	}
	entry := AuditLog{
		ActorType:    "admin",
		ActorEmail:   sql.NullString{String: reviewerEmail, Valid: reviewerEmail != ""},
		Action:       "kyb.document.reviewed",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		Details:      map[string]any{"evidence": key, "status": status, "note": note},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return KYBDocuments{}, fmt.Errorf("audit document review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KYBDocuments{}, fmt.Errorf("commit document review: %w", err)
	}
	return s.KYBDocumentsByMerchant(ctx, merchantID)
}

// SaveKYBVerification stores one third-party verification proposal for a
// document. Advisory only: it informs a reviewer's disposition and never
// advances a tier by itself.
// SaveKYBDocumentAuthenticity stores one provider authenticity verdict for a
// document. Unlike SaveKYBVerification this is a precondition, not advice: the
// corporate hard gate counts the key only when the stored status is genuine,
// so a tampered or inconclusive verdict blocks the key until it is re-run.
func (s *Store) SaveKYBDocumentAuthenticity(ctx context.Context, merchantID uuid.UUID, key string, v KYBDocumentAuthenticity) error {
	d, err := s.KYBDocumentsByMerchant(ctx, merchantID)
	if err != nil {
		return fmt.Errorf("load kyb documents for authenticity: %w", err)
	}
	authenticity := d.Authenticity
	if authenticity == nil {
		authenticity = map[string]KYBDocumentAuthenticity{}
	}
	if v.CheckedAt.IsZero() {
		v.CheckedAt = time.Now().UTC()
	}
	authenticity[key] = v
	payload, err := json.Marshal(authenticity)
	if err != nil {
		return fmt.Errorf("encode document authenticity: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE business_kyb_documents SET authenticity=$2::jsonb, updated_at=now()
		WHERE merchant_id=$1`, merchantID, string(payload))
	return err
}

func (s *Store) SaveKYBVerification(ctx context.Context, merchantID uuid.UUID, key string, v KYBDocumentVerification) error {
	d, err := s.KYBDocumentsByMerchant(ctx, merchantID)
	if err != nil {
		return fmt.Errorf("load kyb documents for verification: %w", err)
	}
	verifications := d.Verifications
	if verifications == nil {
		verifications = map[string]KYBDocumentVerification{}
	}
	if v.CheckedAt.IsZero() {
		v.CheckedAt = time.Now().UTC()
	}
	verifications[key] = v
	payload, err := json.Marshal(verifications)
	if err != nil {
		return fmt.Errorf("encode document verification: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE business_kyb_documents SET verifications=$2::jsonb, updated_at=now()
		WHERE merchant_id=$1`, merchantID, string(payload))
	return err
}

// KYBDocumentsByMerchant loads the merchant's latest recorded corporate
// documents. A missing record returns pgx.ErrNoRows.
func (s *Store) KYBDocumentsByMerchant(ctx context.Context, merchantID uuid.UUID) (KYBDocuments, error) {
	var d KYBDocuments
	var reviews, verifications, authenticity []byte
	err := s.pool.QueryRow(ctx, `
		SELECT merchant_id, cac_reg_number, tin, beneficial_owners,
		       business_address, scuml_number, dnfbp,
		       cac_document, tin_document, address_document,
		       location_lat, location_lng, location_accuracy_m,
		       location_captured_at, location_status, location_provider,
		       COALESCE(reviews::text,'{}'), COALESCE(verifications::text,'{}'),
		       COALESCE(authenticity::text,'{}'),
		       recorded_at, updated_at
		FROM business_kyb_documents WHERE merchant_id=$1`, merchantID).Scan(
		&d.MerchantID, &d.CACRegNumber, &d.TIN, &d.BeneficialOwners,
		&d.BusinessAddress, &d.SCUMLNumber, &d.DNFBP,
		&d.CACDocument, &d.TINDocument, &d.AddressDocument,
		&d.LocationLat, &d.LocationLng, &d.LocationAccuracyM,
		&d.LocationCapturedAt, &d.LocationStatus, &d.LocationProvider,
		&reviews, &verifications, &authenticity, &d.RecordedAt, &d.UpdatedAt)
	if err != nil {
		return KYBDocuments{}, err
	}
	_ = json.Unmarshal(reviews, &d.Reviews)
	_ = json.Unmarshal(verifications, &d.Verifications)
	_ = json.Unmarshal(authenticity, &d.Authenticity)
	return d, nil
}

// documentsChanged reports whether a resubmission altered any merchant-supplied
// document, so prior reviewer dispositions must be discarded.
func documentsChanged(old, next KYBDocuments) bool {
	return old.CACRegNumber != next.CACRegNumber ||
		old.TIN != next.TIN ||
		old.BeneficialOwners != next.BeneficialOwners ||
		old.BusinessAddress != next.BusinessAddress ||
		old.SCUMLNumber != next.SCUMLNumber ||
		old.CACDocument != next.CACDocument ||
		old.TINDocument != next.TINDocument ||
		old.AddressDocument != next.AddressDocument
}

func orEmptyReviews(m map[string]KYBDocumentReview) map[string]KYBDocumentReview {
	if m == nil {
		return map[string]KYBDocumentReview{}
	}
	return m
}

func orEmptyVerifications(m map[string]KYBDocumentVerification) map[string]KYBDocumentVerification {
	if m == nil {
		return map[string]KYBDocumentVerification{}
	}
	return m
}

func orEmptyAuthenticity(m map[string]KYBDocumentAuthenticity) map[string]KYBDocumentAuthenticity {
	if m == nil {
		return map[string]KYBDocumentAuthenticity{}
	}
	return m
}

// firstNonBlank returns the first trimmed non-empty value.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// BusinessEvidenceRequiredFor returns the complete evidence set a one-step
// business advancement to the given tier requires (the B1 corporate suite is
// five hard gates; B2/B3 each require one key). Empty when to is not the
// target of an adjacent step (B0 has no entry evidence).
func BusinessEvidenceRequiredFor(to string) []string {
	switch to {
	case kyc.TierB1:
		return []string{kyc.EvCACVerified, kyc.EvTINVerified, kyc.EvUBOCleared, kyc.EvLocationVerified, kyc.EvSCUMLSubmitted}
	case kyc.TierB2:
		return []string{kyc.EvBusinessBank}
	case kyc.TierB3:
		return []string{kyc.EvBusinessEDD}
	}
	return nil
}

// BusinessEvidenceFor returns a single headline evidence key for a one-step
// business advancement, for legacy callers that build a summary. Prefer
// BusinessEvidenceRequiredFor when recording the actual request.
func BusinessEvidenceFor(to string) string {
	required := BusinessEvidenceRequiredFor(to)
	if len(required) == 0 {
		return ""
	}
	return required[0]
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
	fromTier := profile.Tier
	if err := kyc.CanAdvanceBusiness(profile.Tier, to, evidence); err != nil {
		return KYBProfile{}, err
	}
	if kyc.BusinessOrder(to) >= 1 && kyc.BlockedByScreening(profile.LastScreeningDecision) {
		return KYBProfile{}, fmt.Errorf("advance to %s blocked by screening decision %q", to, profile.LastScreeningDecision)
	}
	// Corporate hard gate: B0 -> B1 needs every key backed by a document a
	// reviewer actually verified. An evidence key on its own is a claim, so a
	// bare upgrade request (no documents on file) can never reach B1, however
	// it was submitted and whoever approved it.
	if to == kyc.TierB1 {
		docs, err := s.KYBDocumentsByMerchant(ctx, merchantID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return KYBProfile{}, fmt.Errorf("load kyb documents for hard gate: %w", err)
		}
		verified := make(map[string]bool)
		for _, key := range VerifiedKYBEvidence(docs) {
			verified[key] = true
		}
		for _, key := range BusinessEvidenceRequiredFor(to) {
			if !verified[key] {
				return KYBProfile{}, fmt.Errorf("advance to %s requires a reviewer-verified %q document", to, key)
			}
		}
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
	fromTier := profile.Tier
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
		return KYBProfile{}, fmt.Errorf("audit kyb downgrade: %w", err)
	}
	// A merchant demoted to the unverified B0 baseline loses its verified
	// status: the business wallet is frozen (money-out blocked) within the same
	// transaction so the downgrade and freeze cannot diverge.
	if to == kyc.TierB0 {
		if err := freezeWalletTx(ctx, tx, WalletOwnerBusiness, merchantID); err != nil {
			return KYBProfile{}, fmt.Errorf("freeze business wallet on downgrade: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return KYBProfile{}, fmt.Errorf("commit kyb downgrade: %w", err)
	}
	return profile, nil
}

// ReviewKYBProfile approves or rejects a merchant's KYB position and records
// the reviewer, the review timestamp, and the reviewer's note in one audited
// transaction. It does not move the tier; that is AdvanceKYBTier's job. A
// rejection freezes the merchant's business wallet (money-out blocked) because
// an unverified position must not continue disbursing.
func (s *Store) ReviewKYBProfile(ctx context.Context, merchantID uuid.UUID, approve bool, reviewedBy *uuid.UUID, note string) error {
	status := "approved"
	if !approve {
		status = "rejected"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin kyb review: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE business_kyb_profiles
		SET review_status=$2, reviewed_by=$3, reviewed_at=now(), updated_at=now()
		WHERE merchant_id=$1`, merchantID, status, reviewedBy)
	if err != nil {
		return fmt.Errorf("update kyb review: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("kyb profile not found")
	}
	var reviewerID uuid.NullUUID
	if reviewedBy != nil {
		reviewerID = uuid.NullUUID{UUID: *reviewedBy, Valid: true}
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyb.reviewed",
		ResourceType: sql.NullString{String: "merchant", Valid: true},
		ResourceID:   sql.NullString{String: merchantID.String(), Valid: true},
		ActorID:      reviewerID,
		Details: map[string]any{
			"decision": status,
			"note":     note,
		},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return fmt.Errorf("audit kyb review: %w", err)
	}
	// W1: approving the merchant's KYB position integrates their business
	// wallet (created active), mirroring AdvanceKYBTier. Rejecting freezes the
	// wallet so an unverified merchant cannot make money-out movements.
	if approve {
		if _, err := s.ensureWalletTx(ctx, tx, WalletOwnerBusiness, merchantID, "", WalletStatusActive); err != nil {
			return fmt.Errorf("ensure business wallet: %w", err)
		}
	} else {
		if err := freezeWalletTx(ctx, tx, WalletOwnerBusiness, merchantID); err != nil {
			return fmt.Errorf("freeze business wallet on reject: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func allowanceLockKey(accountType string, subjectID uuid.UUID, direction string) string {
	return fmt.Sprintf("allow:%s:%s:%s", accountType, subjectID, direction)
}

// freezeWalletTx freezes the entity's wallet (money-out blocked) inside the
// caller's transaction. A missing wallet row is not an error: there is nothing
// to freeze yet.
func freezeWalletTx(ctx context.Context, tx pgx.Tx, ownerType string, ownerID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE wallet_accounts
		SET status='frozen', updated_at=now()
		WHERE owner_type=$1 AND owner_id=$2`, ownerType, ownerID); err != nil {
		return fmt.Errorf("freeze %s wallet: %w", ownerType, err)
	}
	return nil
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
