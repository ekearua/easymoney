package cac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatedVerifyCorporateContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		number   string
		business string
		want     string
	}{
		{"plain digits verify", "1234567", "Acme Ltd", ports.VerifyVerified},
		{"RC prefix verifies", "RC1234567", "Acme Ltd", ports.VerifyVerified},
		{"leading zero is not on the register", "0123456", "Acme Ltd", ports.VerifyNotFound},
		{"name mismatch", "RC1234567", "Acme Name Mismatch Ltd", ports.VerifyMismatch},
		{"too short is not found", "12345", "Acme Ltd", ports.VerifyNotFound},
	}
	s := NewSimulated()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{
				RegistrationNumber: tc.number,
				BusinessName:       tc.business,
			})
			if err != nil {
				t.Fatalf("VerifyCorporate: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestSimulatedVerifyCorporateNeedsNumber(t *testing.T) {
	t.Parallel()
	if _, err := NewSimulated().VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{}); err == nil {
		t.Fatal("an empty registration number must be an error, not a pass")
	}
}

func TestHTTPVerifyCorporateMapsStatus(t *testing.T) {
	t.Parallel()
	var gotBody httpCorporateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/corporate-verification" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"verified","registered_name":"Acme Ltd","registration_date":"2019-03-14","reference":"V-1"}`))
	}))
	defer srv.Close()

	got, err := NewHTTP(srv.URL, "key", 0).VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{
		RegistrationNumber: "RC1234567",
		BusinessName:       "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyCorporate: %v", err)
	}
	if gotBody.RegistrationNumber != "RC1234567" || gotBody.BusinessName != "Acme Ltd" {
		t.Fatalf("request body = %+v", gotBody)
	}
	if got.Status != ports.VerifyVerified || !got.MatchName || got.RegisteredName != "Acme Ltd" || got.ProviderRef != "V-1" {
		t.Fatalf("result = %+v", got)
	}
}

func TestHTTPVerifyCorporateRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"maybe"}`))
	}))
	defer srv.Close()

	if _, err := NewHTTP(srv.URL, "", 0).VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC1234567"}); err == nil {
		t.Fatal("an unrecognized status must be rejected, never silently treated as clear")
	}
}
