package service

import (
	"testing"

	"whatsapp-payment-demo/internal/kyc"
)

// TestFoldAdverseMedia pins the fold the onboarding/rescreen gates enforce:
// the stricter of the sanctions/PEP decision and the adverse-media decision
// wins, and an empty/unknown side falls back to the other.
func TestFoldAdverseMedia(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		adverse string
		want    string
	}{
		{"clear+clear", kyc.ScreenClear, kyc.ScreenClear, kyc.ScreenClear},
		{"clear+possible", kyc.ScreenClear, kyc.ScreenPossible, kyc.ScreenPossible},
		{"possible+strong", kyc.ScreenPossible, kyc.ScreenStrong, kyc.ScreenStrong},
		{"strong+possible keeps base", kyc.ScreenStrong, kyc.ScreenPossible, kyc.ScreenStrong},
		{"blocked+clear", kyc.ScreenBlocked, kyc.ScreenClear, kyc.ScreenBlocked},
		{"clear+blocked", kyc.ScreenClear, kyc.ScreenBlocked, kyc.ScreenBlocked},
		{"blocked+anything", kyc.ScreenBlocked, kyc.ScreenPossible, kyc.ScreenBlocked},
		{"any+blocked", kyc.ScreenStrong, kyc.ScreenBlocked, kyc.ScreenBlocked},
		{"empty base uses adverse", "", kyc.ScreenStrong, kyc.ScreenStrong},
		{"empty adverse uses base", kyc.ScreenPossible, "", kyc.ScreenPossible},
		{"unknown adverse uses base", kyc.ScreenClear, "banana", kyc.ScreenClear},
		{"unknown base uses adverse", "banana", kyc.ScreenStrong, kyc.ScreenStrong},
		{"both unknown", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FoldAdverseMedia(tc.base, tc.adverse); got != tc.want {
				t.Fatalf("FoldAdverseMedia(%q, %q) = %q, want %q", tc.base, tc.adverse, got, tc.want)
			}
		})
	}
}
