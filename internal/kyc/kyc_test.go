package kyc

import "testing"

func TestOrder(t *testing.T) {
	cases := map[string]int{
		TierL0: 0,
		TierL1: 1,
		TierL2: 2,
		TierL3: 3,
		TierL4: 4,
		"L9":   -1,
		"":     -1,
	}
	for tier, want := range cases {
		if got := Order(tier); got != want {
			t.Errorf("Order(%q) = %d, want %d", tier, got, want)
		}
	}
}

func TestCanAdvance(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		evidence []string
		wantOK   bool
	}{
		{"L0 to L1 with channel", TierL0, TierL1, []string{EvChannelConfirmed}, true},
		{"L0 to L1 missing evidence", TierL0, TierL1, nil, false},
		{"L1 to L2 with identity", TierL1, TierL2, []string{EvIdentityOnFile}, true},
		{"L2 to L3 with NIN", TierL2, TierL3, []string{EvNINBVNVerified}, true},
		{"L3 to L4 with EDD", TierL3, TierL4, []string{EvEDDCompleted}, true},
		{"skip a tier", TierL1, TierL3, []string{EvNINBVNVerified}, false},
		{"downgrade not an advance", TierL2, TierL1, []string{EvIdentityOnFile}, false},
		{"unknown tier", TierL0, "L9", []string{EvChannelConfirmed}, false},
		{"same tier", TierL2, TierL2, []string{EvIdentityOnFile}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CanAdvance(tc.from, tc.to, tc.evidence)
			if tc.wantOK && err != nil {
				t.Fatalf("CanAdvance(%q,%q,%v) = %v, want nil", tc.from, tc.to, tc.evidence, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("CanAdvance(%q,%q,%v) = nil, want error", tc.from, tc.to, tc.evidence)
			}
		})
	}
}

func TestCanDowngrade(t *testing.T) {
	if err := CanDowngrade(TierL2, TierL0); err != nil {
		t.Errorf("L2->L0 should be a valid downgrade: %v", err)
	}
	if err := CanDowngrade(TierL2, TierL3); err == nil {
		t.Error("L2->L3 should not be a downgrade")
	}
	if err := CanDowngrade(TierL2, TierL2); err == nil {
		t.Error("same tier should not be a downgrade")
	}
}

func TestBlockedByScreening(t *testing.T) {
	if BlockedByScreening(ScreenClear) || BlockedByScreening(ScreenManuallyCleared) {
		t.Error("clear/manually_cleared must not block advancement")
	}
	for _, d := range []string{ScreenPossible, ScreenStrong, ScreenBlocked} {
		if !BlockedByScreening(d) {
			t.Errorf("%q must block advancement", d)
		}
	}
}

func TestBandForScore(t *testing.T) {
	cases := map[float64]string{
		0:   RiskLow,
		39:  RiskLow,
		40:  RiskMedium,
		69:  RiskMedium,
		70:  RiskHigh,
		100: RiskHigh,
	}
	for score, want := range cases {
		if got := BandForScore(score); got != want {
			t.Errorf("BandForScore(%v) = %q, want %q", score, got, want)
		}
	}
}

func TestScoreRisk(t *testing.T) {
	tests := []struct {
		name      string
		events    []RiskEventInput
		tier      string
		screening string
		wantScore float64
		wantBand  string
	}{
		{
			name:      "clean low-risk profile",
			events:    nil,
			tier:      TierL3,
			screening: ScreenClear,
			wantScore: 0,
			wantBand:  RiskLow,
		},
		{
			name:      "unverified adds risk",
			events:    nil,
			tier:      TierL0,
			screening: "",
			wantScore: 10,
			wantBand:  RiskLow,
		},
		{
			name: "structuring events drive medium",
			events: []RiskEventInput{
				{EventType: "structuring", Score: 30},
				{EventType: "velocity", Score: 15},
			},
			tier:      TierL3,
			screening: ScreenClear,
			wantScore: 45,
			wantBand:  RiskMedium,
		},
		{
			name:      "strong screening pushes high",
			events:    []RiskEventInput{{EventType: "round_amounts", Score: 35}},
			tier:      TierL2,
			screening: ScreenStrong,
			wantScore: 75,
			wantBand:  RiskHigh,
		},
		{
			name:      "event score clamped to 100",
			events:    []RiskEventInput{{EventType: "sanction", Score: 500}},
			tier:      TierL3,
			screening: ScreenClear,
			wantScore: 100,
			wantBand:  RiskHigh,
		},
		{
			name:      "EDD completes offsets earlier risk",
			events:    []RiskEventInput{{EventType: "velocity", Score: 25}},
			tier:      TierL4,
			screening: ScreenPossible,
			wantScore: 20,
			wantBand:  RiskLow,
		},
		{
			name: "total clamped at 100",
			events: []RiskEventInput{
				{EventType: "a", Score: 80},
				{EventType: "b", Score: 80},
			},
			tier:      TierL3,
			screening: ScreenStrong,
			wantScore: 100,
			wantBand:  RiskHigh,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreRisk(tc.events, tc.tier, tc.screening)
			if got.Score != tc.wantScore {
				t.Errorf("score = %v, want %v", got.Score, tc.wantScore)
			}
			if got.Band != tc.wantBand {
				t.Errorf("band = %q, want %q", got.Band, tc.wantBand)
			}
		})
	}
}
