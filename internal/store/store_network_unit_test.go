package store

// Unit tests for the Trust Fabric network and feedback-loop pure logic:
// ring detection over link edges, account masking, and rule tuning
// suggestions from false-positive stats. The SQL paths behind these rows
// are covered by the gated store integration tests.

import (
	"strings"
	"testing"
	"time"
)

func TestDetectRingsGroupsSharedFacts(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	edges := []IdentityLinkRow{
		// Ring 1: A-B share a device, B-C share a settlement account →
		// one three-member component transitively.
		{LinkKind: "device", LinkValue: "fp-1", SubjectA: "u-A", SubjectB: "u-B", NameA: "Ada", NameB: "Bode", LastSharedAt: base, Sightings: 2},
		{LinkKind: "settlement_account", LinkValue: "0123456789", SubjectA: "u-B", SubjectB: "u-C", NameB: "Chidi", LastSharedAt: base.Add(time.Hour), Sightings: 2},
		// Ring 2: separate two-member device pair.
		{LinkKind: "device", LinkValue: "fp-2", SubjectA: "u-D", SubjectB: "u-E", LastSharedAt: base, Sightings: 2},
		// Self-pair noise: a row whose two sides are the same subject must
		// not create a ring.
		{LinkKind: "device", LinkValue: "fp-3", SubjectA: "u-F", SubjectB: "u-F", LastSharedAt: base, Sightings: 2},
	}
	rings := DetectRings(edges, 2)
	if len(rings) != 2 {
		t.Fatalf("got %d rings, want 2 (transitive trio + pair); %+v", len(rings), rings)
	}
	if len(rings[0].Members) != 3 {
		t.Errorf("largest ring = %v, want the transitive trio", rings[0].Members)
	}
	if strings.Join(rings[0].Kinds, ",") != "device,settlement_account" {
		t.Errorf("kinds = %v, want device+settlement_account", rings[0].Kinds)
	}
	if rings[0].Names["u-A"] != "Ada" || rings[0].Names["u-C"] != "Chidi" {
		t.Errorf("names = %v, want resolved members", rings[0].Names)
	}
	if len(rings[1].Members) != 2 {
		t.Errorf("second ring = %v, want the pair", rings[1].Members)
	}
	// minMembers=3 filters the pair out.
	if got := DetectRings(edges, 3); len(got) != 1 || len(got[0].Members) != 3 {
		t.Errorf("minMembers=3 → %d rings, want only the trio", len(got))
	}
}

func TestScoreRingOrdersByStrengthAndSignal(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-24 * time.Hour)
	stale := now.Add(-365 * 24 * time.Hour)
	// Fresh account edge beats a fresh device edge of the same shape.
	acct := ScoreRing([]IdentityLink{{Kind: "recipient_account", Sightings: 2, LastSeen: fresh}}, 0, 0, now)
	dev := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: fresh}}, 0, 0, now)
	if acct <= dev {
		t.Errorf("account edge score %v must exceed device edge %v", acct, dev)
	}
	// Recency decays: the year-old edge scores well below yesterday's.
	oldDev := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: stale}}, 0, 0, now)
	if oldDev >= dev {
		t.Errorf("stale edge score %v must be below fresh %v", oldDev, dev)
	}
	// Sightings add weight with diminishing returns.
	once := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: fresh}}, 0, 0, now)
	nine := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 10, LastSeen: fresh}}, 0, 0, now)
	if nine <= once || nine > once*3 {
		t.Errorf("sightings 10 → %v vs 2 → %v: more, but sublinear", nine, once)
	}
	// Confirmed alerts multiply hardest; open alerts bump modestly.
	base := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: fresh}}, 0, 0, now)
	withOpen := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: fresh}}, 0, 2, now)
	withConfirmed := ScoreRing([]IdentityLink{{Kind: "device", Sightings: 2, LastSeen: fresh}}, 2, 0, now)
	if withOpen <= base || withOpen >= base*2 {
		t.Errorf("open-alert bump = %v from %v, want a modest multiplier", withOpen, base)
	}
	if withConfirmed < base*2 {
		t.Errorf("confirmed multiplier = %v from %v, want ≥2×", withConfirmed, base)
	}
}

func TestMaskAccountShowsLastFourOnly(t *testing.T) {
	t.Parallel()
	if got := MaskAccount("0123456789"); got != "••••6789" {
		t.Errorf("MaskAccount long = %q", got)
	}
	if got := MaskAccount("123"); got != "••••" {
		t.Errorf("MaskAccount short = %q, want fully masked", got)
	}
	row := IdentityLinkRow{LinkKind: "settlement_account", LinkValue: "0123456789"}
	if row.MaskedValue() != "••••6789" {
		t.Errorf("MaskedValue settlement = %q", row.MaskedValue())
	}
	fp := IdentityLinkRow{LinkKind: "device", LinkValue: "abcdef0123456789"}
	if fp.MaskedValue() != fp.LinkValue {
		t.Errorf("MaskedValue device should stay opaque, got %q", fp.MaskedValue())
	}
}
