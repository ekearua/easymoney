// Package kyc implements the Xego customer identity tier ladder (C10),
// aligned with the CBN three-tier KYC framework.
//
// Tiers run from L0 (unverified) to L3 (CBN Tier 3, enhanced due diligence).
// L1 is the CBN Tier 1 entry tier (self-declared basic details, no mandated
// electronic ID matching), L2 equates to CBN Tier 2 (a government-issued photo
// ID plus both a verified BVN and NIN), and L3 to CBN Tier 3 (structural proof
// of address plus enhanced due diligence). Advancement is strictly controlled:
// a profile can only move to the next adjacent tier, and only when the
// required evidence has been recorded. Downgrades are allowed to any lower
// tier (rescreen match, verification expiry, revocation) and are audited by
// the store layer.
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
)

// Evidence keys a transition must satisfy. Recorded in customer_verifications
// (verification_type) and screening_results (decision).
const (
	// EvChannelConfirmed means the WhatsApp/Telegram/Instagram/TikTok channel
	// and email are confirmed. Satisfies L0 -> L1 together with
	// EvTier1BasicDetails.
	EvChannelConfirmed = "channel_confirmed"
	// EvTier1BasicDetails means the CBN Tier 1 self-declared data is on file:
	// legal name, date of birth (>= 18), phone number, gender and residential
	// address. Satisfies L0 -> L1 together with EvChannelConfirmed.
	EvTier1BasicDetails = "tier1_basic_details"
	// EvGovPhotoID means a government-issued photo ID (international passport,
	// driver's licence, national ID card, or voter's card) is on file.
	// Satisfies L1 -> L2 together with both a verified BVN and NIN.
	EvGovPhotoID = "government_photo_id"
	// EvBVNVerified means the customer's Bank Verification Number was
	// electronically matched against the NIBSS database. Satisfies L1 -> L2
	// together with EvGovPhotoID and EvNINVerified.
	EvBVNVerified = "bvn_verified"
	// EvNINVerified means the customer's National Identification Number was
	// electronically matched against the NIMC database. Satisfies L1 -> L2
	// together with EvGovPhotoID and EvBVNVerified.
	EvNINVerified = "nin_verified"
	// EvAddressProof means a structural proof of residential address (utility
	// bill or digital geocoding verification) is on file. Satisfies L2 -> L3
	// together with EvEDDCompleted.
	EvAddressProof = "address_proof"
	// EvEDDCompleted means enhanced due diligence finished: liveness / facial
	// biometrics and structural verification of source of funds. Satisfies
	// L2 -> L3 together with EvAddressProof.
	EvEDDCompleted = "edd_completed"
)

// CBN tier correlation ceilings (kobo). These are the maximum single /
// cumulative daily / cumulative monthly transaction limits the CBN sets for
// each individual tier. The kyc_tier_limits defaults must never exceed them
// (they may be lower, i.e. more conservative). Tier 3 is uncapped by the CBN;
// the platform's own PAYMENT_MIN/MAX_KOBO bounds act as the absolute window.
const (
	// CBN Tier 1 (L1): 50,000 / 200,000 / 500,000 naira.
	CBNTier1MaxSingleKobo  = 5_000_000
	CBNTier1MaxDailyKobo   = 20_000_000
	CBNTier1MaxMonthlyKobo = 50_000_000
	// CBN Tier 2 (L2): 200,000 / 500,000 / 3,000,000 naira.
	CBNTier2MaxSingleKobo  = 20_000_000
	CBNTier2MaxDailyKobo   = 50_000_000
	CBNTier2MaxMonthlyKobo = 300_000_000
)

// CBNTier returns the CBN tier number ("1", "2", "3") for an internal tier, or
// "" when the tier has no CBN equivalent (the L0 pre-KYC state).
func CBNTier(t string) string {
	switch t {
	case TierL1:
		return "1"
	case TierL2:
		return "2"
	case TierL3:
		return "3"
	}
	return ""
}

// CBNTierLabel is the customer-facing CBN tier label ("CBN Tier 1"), or ""
// when the tier has no CBN equivalent.
func CBNTierLabel(t string) string {
	if n := CBNTier(t); n != "" {
		return "CBN Tier " + n
	}
	return ""
}

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

// ML/FT customer risk bands (C13). low/medium/high drive CDD vs EDD: low
// customers pass standard CDD, high customers require enhanced due diligence
// (L3) before higher-value activity.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// RiskBandThresholds map an aggregate score onto the CBN risk-based bands.
const (
	RiskBandMediumThreshold = 40.0
	RiskBandHighThreshold   = 70.0
)

// RiskEventInput is one scored ML/FT observation contributing to the customer
// risk score.
type RiskEventInput struct {
	EventType string
	Score     float64
}

// RiskResult is the outcome of scoring a customer.
type RiskResult struct {
	Score   float64
	Band    string
	Factors []string
}

// BandForScore maps an aggregate 0-100 risk score to a band.
func BandForScore(score float64) string {
	switch {
	case score >= RiskBandHighThreshold:
		return RiskHigh
	case score >= RiskBandMediumThreshold:
		return RiskMedium
	}
	return RiskLow
}

// ScoreRisk aggregates observed ML/FT risk events with profile context (tier,
// last sanctions/PEP decision) into a 0-100 score and low/medium/high band.
// Each event contributes its own 0-100 weight (capped at 100 per event);
// screening and tier adjustments are additive and the total is clamped to
// [0,100].
func ScoreRisk(events []RiskEventInput, tier, screening string) RiskResult {
	var score float64
	var factors []string
	for _, e := range events {
		contribution := e.Score
		if contribution > 100 {
			contribution = 100
		}
		if contribution < 0 {
			contribution = 0
		}
		if contribution == 0 {
			continue
		}
		score += contribution
		factors = append(factors, e.EventType)
	}
	switch screening {
	case ScreenPossible:
		score += 15
		factors = append(factors, "possible sanctions/PEP match")
	case ScreenStrong, ScreenBlocked:
		score += 40
		factors = append(factors, "strong sanctions/PEP match")
	}
	switch tier {
	case TierL0, TierL1:
		score += 10
		factors = append(factors, "identity not yet verified")
	case TierL3:
		if screening != ScreenClear {
			score -= 20
			factors = append(factors, "enhanced due diligence completed")
		}
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return RiskResult{Score: score, Band: BandForScore(score), Factors: factors}
}

// ValidTier reports whether t is one of the L0-L3 tiers.
func ValidTier(t string) bool {
	switch t {
	case TierL0, TierL1, TierL2, TierL3:
		return true
	}
	return false
}

// Order returns the numeric position of a tier (0-3). Unknown tiers return -1.
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
	}
	return -1
}

// CanAdvance reports whether the L0-L3 ladder permits moving from -> to.
// Advancement must be to exactly the next tier. The evidence list must contain
// everything the transition requires.
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
		return requireEvidence(to, evidence, EvChannelConfirmed, EvTier1BasicDetails)
	case TierL2:
		return requireEvidence(to, evidence, EvGovPhotoID, EvBVNVerified, EvNINVerified)
	case TierL3:
		return requireEvidence(to, evidence, EvAddressProof, EvEDDCompleted)
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
