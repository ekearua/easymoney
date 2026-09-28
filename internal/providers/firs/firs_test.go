package firs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatedVerifyTaxIDContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		tin      string
		business string
		want     string
	}{
		{"ten digits verify", "1234567890", "Acme Ltd", ports.VerifyVerified},
		{"hyphenated TIN verifies", "123-456-7890", "Acme Ltd", ports.VerifyVerified},
		{"leading zero is not on the register", "0123456789", "Acme Ltd", ports.VerifyNotFound},
		{"name mismatch", "1234567890", "Acme Name Mismatch Ltd", ports.VerifyMismatch},
		{"too short is not found", "12345", "Acme Ltd", ports.VerifyNotFound},
	}
	s := NewSimulated()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: tc.tin, BusinessName: tc.business})
			if err != nil {
				t.Fatalf("VerifyTaxID: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestSimulatedVerifyTaxIDNeedsTIN(t *testing.T) {
	t.Parallel()
	if _, err := NewSimulated().VerifyTaxID(context.Background(), ports.TaxIDRequest{}); err == nil {
		t.Fatal("an empty TIN must be an error, not a pass")
	}
}

func TestHTTPVerifyTaxIDSendsDigitsOnly(t *testing.T) {
	t.Parallel()
	var gotBody httpTINRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tin-verification" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"verified","registered_name":"Acme Ltd","reference":"T-1"}`))
	}))
	defer srv.Close()

	got, err := NewHTTP(srv.URL, "key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{
		TIN:          "123-456-7890",
		BusinessName: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyTaxID: %v", err)
	}
	if gotBody.TIN != "1234567890" {
		t.Fatalf("request TIN = %q, want the digits only", gotBody.TIN)
	}
	if got.Status != ports.VerifyVerified || !got.MatchName || got.ProviderRef != "T-1" {
		t.Fatalf("result = %+v", got)
	}
}

func TestHTTPVerifyTaxIDHonoursNameMatchesFallback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name_matches":false}`))
	}))
	defer srv.Close()

	got, err := NewHTTP(srv.URL, "", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: "1234567890"})
	if err != nil {
		t.Fatalf("VerifyTaxID: %v", err)
	}
	if got.Status != ports.VerifyMismatch || got.MatchName {
		t.Fatalf("result = %+v, want a mismatch from name_matches=false", got)
	}
}
