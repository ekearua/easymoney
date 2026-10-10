package kyc

import (
	"strings"
	"testing"
)

// TestScoreRiskGeographyAndVolumeInputs pins the G14 additive adjustments:
// geography only raises the score for a FATF high-risk / monitored country of
// residence, the volume band only raises it for high/very_high, an undeclared
// input contributes nothing, a custom geography weight is honored, and the
// total still clamps at 100.
func TestScoreRiskGeographyAndVolumeInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		events        []RiskEventInput
		opts          RiskScoring
		wantScore     float64
		wantFactor    string
		wantNoFactors bool
	}{
		{
			name:          "undeclared country and band add nothing",
			opts:          RiskScoring{},
			wantScore:     0,
			wantNoFactors: true,
		},
		{
			name:       "high-risk country adds the default geography weight",
			opts:       RiskScoring{CountryOfResidence: "IR"},
			wantScore:  RiskGeographyWeight,
			wantFactor: "country of residence on the FATF high-risk and monitored jurisdictions list",
		},
		{
			name:       "country matching is case-insensitive and trimmed",
			opts:       RiskScoring{CountryOfResidence: " kp "},
			wantScore:  RiskGeographyWeight,
			wantFactor: "country of residence on the FATF high-risk and monitored jurisdictions list",
		},
		{
			name:          "a low-risk country adds nothing",
			opts:          RiskScoring{CountryOfResidence: "NG"},
			wantScore:     0,
			wantNoFactors: true,
		},
		{
			name:          "low volume band adds nothing",
			opts:          RiskScoring{ExpectedVolumeBand: VolumeBandLow},
			wantScore:     0,
			wantNoFactors: true,
		},
		{
			name:          "medium volume band adds nothing",
			opts:          RiskScoring{ExpectedVolumeBand: VolumeBandMedium},
			wantScore:     0,
			wantNoFactors: true,
		},
		{
			name:       "high volume band adds the volume weight",
			opts:       RiskScoring{ExpectedVolumeBand: VolumeBandHigh},
			wantScore:  RiskVolumeWeight,
			wantFactor: "expected activity volume band calls for enhanced attention",
		},
		{
			name:       "very_high volume band adds the volume weight",
			opts:       RiskScoring{ExpectedVolumeBand: VolumeBandVeryHigh},
			wantScore:  RiskVolumeWeight,
			wantFactor: "expected activity volume band calls for enhanced attention",
		},
		{
			name:       "a custom geography weight is honored",
			opts:       RiskScoring{CountryOfResidence: "SY", GeographyWeight: 5},
			wantScore:  5,
			wantFactor: "country of residence on the FATF high-risk and monitored jurisdictions list",
		},
		{
			name:       "country and band stack",
			opts:       RiskScoring{CountryOfResidence: "VE", ExpectedVolumeBand: VolumeBandHigh},
			wantScore:  RiskGeographyWeight + RiskVolumeWeight,
			wantFactor: "expected activity volume band calls for enhanced attention",
		},
		{
			name:       "adjustments never push the score past the clamp",
			events:     []RiskEventInput{{EventType: "sanction", Score: 95}},
			opts:       RiskScoring{CountryOfResidence: "MM", ExpectedVolumeBand: VolumeBandVeryHigh},
			wantScore:  100,
			wantFactor: "country of residence on the FATF high-risk and monitored jurisdictions list",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreRisk(tc.events, TierL2, ScreenClear, tc.opts)
			if got.Score != tc.wantScore {
				t.Fatalf("score = %v, want %v (factors %v)", got.Score, tc.wantScore, got.Factors)
			}
			if tc.wantNoFactors {
				for _, f := range got.Factors {
					if f == "country of residence on the FATF high-risk and monitored jurisdictions list" ||
						f == "expected activity volume band calls for enhanced attention" {
						t.Fatalf("unexpected factor %q in %v", f, got.Factors)
					}
				}
				return
			}
			if !strings.Contains(strings.Join(got.Factors, "\n"), tc.wantFactor) {
				t.Fatalf("factors = %v, want to contain %q", got.Factors, tc.wantFactor)
			}
		})
	}
}

// TestJurisdictionHighRisk pins the documented FATF high-risk / monitored ISO
// set and the normalisation the score relies on.
func TestJurisdictionHighRisk(t *testing.T) {
	t.Parallel()
	highRisk := []string{"AF", "CU", "IR", "KP", "LY", "MM", "SD", "SY", "VE", "YE"}
	for _, iso := range highRisk {
		if !JurisdictionHighRisk(iso) {
			t.Errorf("JurisdictionHighRisk(%q) = false, want true", iso)
		}
		if !JurisdictionHighRisk(strings.ToLower(iso)) {
			t.Errorf("JurisdictionHighRisk(%q) = false, want true for lower case", strings.ToLower(iso))
		}
	}
	for _, iso := range []string{"", "NG", "US", "GB", "ZZ", "I", "IRN"} {
		if JurisdictionHighRisk(iso) {
			t.Errorf("JurisdictionHighRisk(%q) = true, want false", iso)
		}
	}
}
