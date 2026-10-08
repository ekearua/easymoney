package store

// Spec G11 (CBN_AML_COMPLIANCE_SPEC.md §3.7): duplicate-identity detection.
// One person -> one customer. A second profile presenting a BVN or NIN that is
// already verified on another profile is refused, and the collision opens a
// review case for a human. The identity itself is owned by the identity_keys
// table (migration 090), keyed by a non-reversible hash so the raw number is
// never duplicated into a second queryable column.
//
// The seam is RecordCustomerVerification: it claims the key inside the same
// transaction that writes the verification row, so a refused duplicate leaves
// no evidence behind and no tier advancement is possible.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// ErrIdentityInUse is returned when a BVN/NIN is already verified on a
// different profile. Callers surface a friendly re-ask rather than a 500.
var ErrIdentityInUse = errors.New("identity already linked to another profile")

// IdentityKeySupported reports whether a verification type participates in the
// duplicate-identity check. Only the two electronic identities (BVN, NIN) are
// identity keys; a photo ID or an email confirmation is not.
func IdentityKeySupported(verificationType string) bool {
	return verificationType == kyc.EvBVNVerified || verificationType == kyc.EvNINVerified
}

// IdentityKey derives the stored, non-reversible key for a verification's
// identity number. It must stay in step with the migration-090 backfill: the
// same SHA-256 over verification_type ":" digits. An empty or non-numeric
// input yields no key, and therefore no uniqueness claim.
func IdentityKey(verificationType, idNumber string) string {
	digits := identityDigits(idNumber)
	if digits == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(verificationType + ":" + digits))
	return hex.EncodeToString(sum[:])
}

// identityDigits keeps only decimal digits, matching the backfill's
// regexp_replace(..., '[^0-9]', ”, 'g').
func identityDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// identityNumberFromResult reads the id_number a verification recorded. The
// shape is shared by the chat and browser tracks ({"id_number": "..."}).
func identityNumberFromResult(result map[string]any) string {
	if result == nil {
		return ""
	}
	switch v := result["id_number"].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// IdentityKeyOwner is the overlap query: it answers which user already owns a
// BVN/NIN, or (uuid.Nil, false) when the identity is free. It powers the
// duplicate check and lets an investigator trace an overlap without the raw
// number.
func (s *Store) IdentityKeyOwner(ctx context.Context, verificationType, idNumber string) (uuid.UUID, bool, error) {
	key := IdentityKey(verificationType, idNumber)
	if key == "" {
		return uuid.Nil, false, nil
	}
	var owner uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT user_id FROM identity_keys
		WHERE verification_type=$1 AND identity_key=$2`, verificationType, key).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("identity key owner: %w", err)
	}
	return owner, true, nil
}

// EnsureIdentityConflictCase opens the review case a duplicate identity
// raises. It is idempotent per user: at most one open identity case, so a
// customer who retries the same number is not spammed into the queue. The
// returned bool reports whether a new case was opened.
func (s *Store) EnsureIdentityConflictCase(ctx context.Context, userID uuid.UUID, verificationType string) (ManualReviewCase, bool, error) {
	var existing ManualReviewCase
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, case_type, COALESCE(requested_tier,''), status, reason,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), created_at, reviewed_at
		FROM manual_review_cases
		WHERE user_id=$1 AND case_type='identity' AND status='pending'
		ORDER BY created_at DESC LIMIT 1`, userID).Scan(
		&existing.ID, &existing.UserID, &existing.CaseType, &existing.RequestedTier,
		&existing.Status, &existing.Reason, &existing.ReviewerEmail,
		&existing.DecisionNote, &existing.CreatedAt, &existing.ReviewedAt)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ManualReviewCase{}, false, fmt.Errorf("check open identity case: %w", err)
	}
	label := identityLabel(verificationType)
	c, err := s.CreateManualReviewCase(ctx, ManualReviewCase{
		UserID:   userID,
		CaseType: "identity",
		Reason:   "duplicate identity: this profile presented a " + label + " already verified on another profile",
	})
	if err != nil {
		return ManualReviewCase{}, false, fmt.Errorf("open identity case: %w", err)
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "kyc.identity_conflict",
		ResourceType: sql.NullString{String: "user", Valid: true},
		ResourceID:   sql.NullString{String: userID.String(), Valid: true},
		Details: map[string]any{
			"case_id":           c.ID,
			"verification_type": verificationType,
		},
	}
	if _, err := s.AppendAuditLog(ctx, entry); err != nil {
		return c, true, fmt.Errorf("audit identity conflict: %w", err)
	}
	return c, true, nil
}

// identityLabel is the customer-facing name of an identity key.
func identityLabel(verificationType string) string {
	if verificationType == kyc.EvNINVerified {
		return "NIN"
	}
	return "BVN"
}

// claimIdentityKey records ownership inside an existing transaction and reports
// whether the key belongs to this same user. A row already owned by someone
// else makes this return false, which the caller converts into
// ErrIdentityInUse; a same-user re-verification is a no-op that returns true.
func (s *Store) claimIdentityKey(ctx context.Context, tx pgx.Tx, userID uuid.UUID, verificationType, key string) (bool, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity_keys(verification_type, identity_key, user_id)
		VALUES($1,$2,$3)
		ON CONFLICT (verification_type, identity_key) DO NOTHING`,
		verificationType, key, userID); err != nil {
		return false, fmt.Errorf("claim identity key: %w", err)
	}
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT user_id FROM identity_keys
		WHERE verification_type=$1 AND identity_key=$2`, verificationType, key).Scan(&owner); err != nil {
		return false, fmt.Errorf("identity owner: %w", err)
	}
	return owner == userID, nil
}
