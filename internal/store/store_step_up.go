package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Step-up scopes and actions (migration 093). A grant authorises one action for
// one subject until it expires; the settlement service checks the payout action
// for a merchant whose owner is high risk.
const (
	StepUpScopeMerchant        = "merchant"
	StepUpScopeAdmin           = "admin"
	StepUpActionMerchantPayout = "merchant_payout"
)

// RecordStepUpGrant stores a fresh grant authorising action for subjectID for
// ttl. factorKind names the second factor that satisfied the step-up.
func (s *Store) RecordStepUpGrant(ctx context.Context, scope string, subjectID uuid.UUID, action, factorKind string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO step_up_grants(scope, subject_id, action, factor_kind, expires_at)
		VALUES($1,$2,$3,$4,$5)`,
		scope, subjectID, action, factorKind, time.Now().UTC().Add(ttl))
	return err
}

// HasStepUpGrant reports whether an unexpired grant authorising action exists
// for the subject.
func (s *Store) HasStepUpGrant(ctx context.Context, scope string, subjectID uuid.UUID, action string, now time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM step_up_grants
			WHERE scope=$1 AND subject_id=$2 AND action=$3 AND expires_at > $4
		)`, scope, subjectID, action, now).Scan(&exists)
	return exists, err
}
