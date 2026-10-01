package paymentscreening

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// TestSimulatedVerdicts pins the deterministic vendor vocabulary the G2 gate
// depends on: confirmed list hits block, fuzzy hits are ambiguous (never
// auto-cleared), everyone else clears, and a movement without a screenable
// counterparty name is ambiguous rather than silently cleared.
func TestSimulatedVerdicts(t *testing.T) {
	t.Parallel()
	sim := NewSimulated()
	cases := []struct {
		name     string
		counter  string
		decision string
	}{
		{"clear name", "Lagos Lunchbox Ltd", kyc.ScreenClear},
		{"case-insensitive block", "ACME SANCTIONED trading", kyc.ScreenBlocked},
		{"watchlisted block", "Watchlisted Corp", kyc.ScreenBlocked},
		{"fuzzy review", "Similar Ventures Ltd", kyc.ScreenStrong},
		// A blocklist term outranks resemblance: interdiction wins over hold.
		{"blocklist outranks resemblance", "Similar Sanctioned Ltd", kyc.ScreenBlocked},
		{"missing name is ambiguous", "   ", kyc.ScreenPossible},
	}
	for _, tc := range cases {
		dec, err := sim.ScreenPayment(context.Background(), ports.PaymentScreenRequest{
			CounterpartyName: tc.counter, AccountNumber: "0123456789", BankCode: "044",
			AmountKobo: 2_500_000, SubjectID: "11111111-1111-1111-1111-111111111111",
			Rail: "merchant_payout",
		})
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if dec.Decision != tc.decision {
			t.Fatalf("%s: decision %q, want %q", tc.name, dec.Decision, tc.decision)
		}
	}
	if sim.ProviderName() != "simulated" {
		t.Fatal("provider name must be recorded for screening rows")
	}
}
