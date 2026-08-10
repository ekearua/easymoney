package identity

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatorVerifyIdentity(t *testing.T) {
	s := NewSimulator()
	ctx := context.Background()

	cases := []struct {
		name    string
		idType  string
		number  string
		wantErr bool
		status  string
	}{
		{"valid nin", "NIN", "12345678901", false, "verified"},
		{"valid bvn", "bvn", "22222222222", false, "verified"},
		{"mismatch nin", "NIN", "81234567890", false, "mismatch"},
		{"not found bvn", "BVN", "92222222222", false, "not_found"},
		{"wrong length", "NIN", "12345", true, ""},
		{"letters in number", "NIN", "12345678abc", true, ""},
		{"unsupported type", "passport", "12345678901", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
				IDType: tc.idType, IDNumber: tc.number, LegalName: "Ada Obi", DateOfBirth: "1992-05-24",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Status != tc.status {
				t.Fatalf("expected status %q, got %q (%s)", tc.status, res.Status, res.Message)
			}
			if res.ProviderRef == "" {
				t.Fatal("expected a provider reference")
			}
		})
	}
}

func TestSimulatorMatchFlags(t *testing.T) {
	s := NewSimulator()
	ctx := context.Background()

	withDetails, err := s.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
		IDType: "NIN", IDNumber: "12345678901", LegalName: "Ada Obi", DateOfBirth: "1992-05-24",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !withDetails.MatchName || !withDetails.MatchDOB {
		t.Fatalf("expected name and dob match flags, got %+v", withDetails)
	}

	withoutDetails, err := s.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
		IDType: "NIN", IDNumber: "12345678901",
	})
	if err != nil {
		t.Fatal(err)
	}
	if withoutDetails.MatchName || withoutDetails.MatchDOB {
		t.Fatalf("expected no match flags without submitted details, got %+v", withoutDetails)
	}
}
