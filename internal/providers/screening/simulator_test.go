package screening

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatorScreen(t *testing.T) {
	s := NewSimulator()
	ctx := context.Background()

	cases := []struct {
		name   string
		legal  string
		status string
	}{
		{"clear", "Ada Obi", kyc.ScreenClear},
		{"sanctions match", "Harold Sanctioned", kyc.ScreenStrong},
		{"drug match", "Jo Drug Lord", kyc.ScreenStrong},
		{"pep token", "Senator Musa", kyc.ScreenPossible},
		{"pep word", "Pep Guardiola", kyc.ScreenPossible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Screen(ctx, ports.ScreeningRequest{LegalName: tc.legal})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.status {
				t.Fatalf("expected %q, got %q (%s)", tc.status, got.Decision, got.Message)
			}
			if got.ProviderRef == "" {
				t.Fatal("expected a provider reference")
			}
			if tc.status == kyc.ScreenClear && len(got.MatchedNames) != 0 {
				t.Fatalf("expected no matches for clear, got %v", got.MatchedNames)
			}
			if tc.status != kyc.ScreenClear && len(got.MatchedNames) == 0 {
				t.Fatalf("expected matched names for %q, got none", tc.status)
			}
		})
	}
}

func TestSimulatorScreenEmptyName(t *testing.T) {
	s := NewSimulator()
	if _, err := s.Screen(context.Background(), ports.ScreeningRequest{}); err == nil {
		t.Fatal("expected error for empty legal name")
	}
}
