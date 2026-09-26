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
