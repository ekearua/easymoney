package kyc

import "math"

// Trust Fabric network signals (M3), fed back into the ML/FT score.
//
// The network layer correlates identities that share a device, a settlement
// account, or a channel identifier. Until now those findings only appeared on
// /admin/network, so an identity could sit in a confirmed ring and still score
// as though it acted alone. These are the two findings worth scoring: a shared
// browser (weak, factual) and membership of a ring with confirmed evidence
// (a judgement about who the identity moves with).
//
// Both are deliberately modest next to a direct observation — the strongest
// band must stay reachable only through what an identity itself did.

const (
	// RiskEventSharedDevice is the observation recorded for every identity on
	// a browser fingerprint used by more than one.
	RiskEventSharedDevice = "network_shared_device"
	// RiskEventRingMember is the observation recorded for each member of a
	// ring whose case carries at least one confirmed alert.
	RiskEventRingMember = "network_ring_member"
)

// SharedDeviceRisk weights a shared browser by how many *other* identities use
// it. One co-user is weak on its own — families, cyber cafés and shared office
// machines all share browsers — so it starts low and grows with the crowd,
// capped below the weight of a single confirmed rule hit.
func SharedDeviceRisk(coUsers int) float64 {
	if coUsers < 1 {
		return 0
	}
	return math.Min(15+10*float64(coUsers-1), 45)
}

// RingMembershipRisk weights belonging to a ring that has confirmed alerts
// among its members. The strength is the confirmed evidence; each additional
// confirmation and each additional member raises it, capped so that membership
// alone can never reach the top of the scale — an identity that shares a
// cyber-café browser with a flagged stranger is a lead to review, not a
// verdict.
func RingMembershipRisk(confirmedHits, memberCount int) float64 {
	if confirmedHits < 1 {
		return 0
	}
	score := 30 + 10*float64(confirmedHits-1)
	if memberCount > 2 {
		score += 10 * float64(memberCount-2)
	}
	return math.Min(score, 70)
}
