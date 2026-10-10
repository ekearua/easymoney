package adversemedia

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// TestSimulatedVerdicts pins the deterministic adverse-media vocabulary the
// G9 fold depends on: a decisive media hit blocks, a resemblance is ambiguous
// (never auto-cleared), everyone else clears, and a missing name is ambiguous
// rather than silently cleared.
func TestSimulatedVerdicts(t *testing.T) {
	t.Parallel()
	sim := NewSimulated()
	cases := []struct {
		name     string
		legal    string
		decision string
		hits     int
	}{
		{"clear name", "Lagos Lunchbox Ltd", kyc.ScreenClear, 0},
		{"case-insensitive block", "ACME Adverse Trading", kyc.ScreenBlocked, 1},
		{"resemblance review", "MediaWatch Ventures Ltd", kyc.ScreenStrong, 1},
		{"block outranks resemblance", "Adverse MediaWatch Ltd", kyc.ScreenBlocked, 1},
		{"missing name is ambiguous", "   ", kyc.ScreenPossible, 0},
	}
	for _, tc := range cases {
		dec, err := sim.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{LegalName: tc.legal})
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if dec.Decision != tc.decision {
			t.Fatalf("%s: decision %q, want %q", tc.name, dec.Decision, tc.decision)
		}
		if len(dec.MatchedHits) != tc.hits {
			t.Fatalf("%s: %d hits, want %d", tc.name, len(dec.MatchedHits), tc.hits)
		}
	}
	if sim.ProviderName() != "simulated" {
		t.Fatal("provider name must be recorded for screening rows")
	}
}
