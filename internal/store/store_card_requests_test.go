package store

import (
	"strings"
	"testing"
)

// TestValidCardReadinessGate pins the gate vocabulary: exactly the five gates
// the store enforces, so the compliance console cannot attest a gate the
// readiness check never reads.
func TestValidCardReadinessGate(t *testing.T) {
	want := map[string]bool{
		CardGateIssuerContract:    true,
		CardGatePCIScope:          true,
		CardGateFundingSettlement: true,
		CardGateIssuerAPI:         true,
		CardGateFulfillment:       true,
	}
	if len(want) != len(cardReadinessGates) {
		t.Fatalf("readiness gate list has %d entries, test names %d", len(cardReadinessGates), len(want))
	}
	for _, gate := range cardReadinessGates {
		if !want[gate] {
			t.Fatalf("gate %q is enforced but not asserted here", gate)
		}
		if !ValidCardReadinessGate(gate) {
			t.Fatalf("ValidCardReadinessGate(%q) = false", gate)
		}
	}
	for _, bad := range []string{"", "issuer", "pci", "ISSUER_CONTRACT", "issuer_contract "} {
		if ValidCardReadinessGate(bad) {
			t.Fatalf("ValidCardReadinessGate(%q) = true, want false", bad)
		}
	}
}

// TestValidCardIDType keeps document types closed: only the documents an agent
// can actually inspect are accepted, and a pasted ID number is never mistaken
// for a type.
func TestValidCardIDType(t *testing.T) {
	for _, ok := range []string{"nin", "bvn", "drivers_licence", "voters_card", "passport", " NIN "} {
		if !ValidCardIDType(ok) {
			t.Fatalf("ValidCardIDType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "id", "national_id", "nin 12345678901", "12345678901"} {
		if ValidCardIDType(bad) {
			t.Fatalf("ValidCardIDType(%q) = true, want false", bad)
		}
	}
}

// TestCardPickupCodeDigestIsSaltedAndDeterministic mirrors the remittance code
// invariant: the verifier recomputes the digest from the stored salt and the
// presented code, two salts never share a digest, and the plaintext is not
// recoverable from the stored value.
func TestCardPickupCodeDigestIsSaltedAndDeterministic(t *testing.T) {
	code, err := NewRemittanceCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != PickupCodeLength {
		t.Fatalf("pickup code %q has length %d, want %d", code, len(code), PickupCodeLength)
	}
	saltA, err := NewRemittanceSalt()
	if err != nil {
		t.Fatal(err)
	}
	saltB, err := NewRemittanceSalt()
	if err != nil {
		t.Fatal(err)
	}
	digestA := RemittanceCodeHash(saltA, code)
	if digestA != RemittanceCodeHash(saltA, code) {
		t.Fatal("the same salt and code must produce the same digest")
	}
	if digestA == RemittanceCodeHash(saltB, code) {
		t.Fatal("different salts must not share a digest")
	}
	if strings.Contains(digestA, code) {
		t.Fatal("the stored digest must not contain the plaintext code")
	}
}
