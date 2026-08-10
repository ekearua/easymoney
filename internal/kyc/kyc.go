// Package kyc implements the Xego customer identity tier ladder (C10).
//
// Tiers run from L0 (unverified) to L4 (enhanced due diligence). Advancement
// is strictly controlled: a profile can only move to the next adjacent tier,
// and only when the required evidence has been recorded. Downgrades are
// allowed to any lower tier (rescreen match, verification expiry, revocation)
// and are audited by the store layer.
package kyc

import (
	"errors"
	"fmt"
)

// Tiers, lowest to highest.
const (
	TierL0 = "L0"
	TierL1 = "L1"
	TierL2 = "L2"
	TierL3 = "L3"
	TierL4 = "L4"
)

// Evidence keys a transition must satisfy. Recorded in customer_verifications
// (verification_type) and screening_results (decision).
const (
	// EvChannelConfirmed means the WhatsApp/Telegram channel and email are
	// confirmed. Satisfies L0 -> L1.
	EvChannelConfirmed = "channel_confirmed"
	// EvIdentityOnFile means a legal identity profile has been submitted.
	// Satisfies L1 -> L2 together with a non-blocked screening decision.
	EvIdentityOnFile = "identity_on_file"
	// EvNINBVNVerified means identity was verified against an official
	// NIN/BVN provider. Satisfies L2 -> L3.
	EvNINBVNVerified = "nin_or_bvn_verified"
	// EvEDDCompleted means enhanced due diligence finished. Satisfies
	// L3 -> L4.
	EvEDDCompleted = "edd_completed"
)

// Screening decisions (screening_results.decision).
const (
	ScreenClear           = "clear"
	ScreenPossible        = "possible"
	ScreenStrong          = "strong"
	ScreenBlocked         = "blocked"
	ScreenManuallyCleared = "manually_cleared"
)

// Review case statuses (manual_review_cases.status).
const (
	CasePending  = "pending"
	CaseApproved = "approved"
	CaseRejected = "rejected"
)

// ValidTier reports whether t is one of the L0-L4 tiers.
func ValidTier(t string) bool {
	switch t {
	case TierL0, TierL1, TierL2, TierL3, TierL4:
		return true
	}
	return false
}

// Order returns the numeric position of a tier (0-4). Unknown tiers return -1.
func Order(t string) int {
	switch t {
	case TierL0:
		return 0
	case TierL1:
		return 1
	case TierL2:
		return 2
	case TierL3:
		return 3
	case TierL4:
		return 4
	}
	return -1
}

// CanAdvance reports whether the L0-L4 ladder permits moving from -> to.
// Advancement must be to exactly the next tier. The evidence list must
// contain everything the transition requires.
func CanAdvance(from, to string, evidence []string) error {
	if !ValidTier(from) || !ValidTier(to) {
		return fmt.Errorf("unknown tier: %q -> %q", from, to)
	}
	fromOrder, toOrder := Order(from), Order(to)
	if toOrder != fromOrder+1 {
		return fmt.Errorf("transition %s -> %s is not an adjacent advancement", from, to)
	}
	switch to {
	case TierL1:
		return requireEvidence(to, evidence, EvChannelConfirmed)
	case TierL2:
		return requireEvidence(to, evidence, EvIdentityOnFile)
	case TierL3:
		return requireEvidence(to, evidence, EvNINBVNVerified)
	case TierL4:
		return requireEvidence(to, evidence, EvEDDCompleted)
	}
	return nil
}

// CanDowngrade reports whether moving from -> to is an allowed downgrade
// (any lower tier). It never requires evidence, but callers should supply a
// reason that is persisted to the audit trail.
func CanDowngrade(from, to string) error {
	if !ValidTier(from) || !ValidTier(to) {
		return fmt.Errorf("unknown tier: %q -> %q", from, to)
	}
	if Order(to) >= Order(from) {
		return fmt.Errorf("transition %s -> %s is not a downgrade", from, to)
	}
	return nil
}

// BlockedByScreening reports whether a screening decision should prevent the
// profile from advancing. Only "clear" and "manually_cleared" pass.
func BlockedByScreening(decision string) bool {
	switch decision {
	case ScreenClear, ScreenManuallyCleared:
		return false
	}
	return true
}

func requireEvidence(to string, evidence []string, keys ...string) error {
	have := make(map[string]bool, len(evidence))
	for _, e := range evidence {
		have[e] = true
	}
	for _, key := range keys {
		if !have[key] {
			return fmt.Errorf("tier %s requires evidence %q", to, key)
		}
	}
	return nil
}

// ErrNoTransition is returned by ResolveStart when the profile has no stored
// tier.
var ErrNoTransition = errors.New("kyc: no tier stored")
