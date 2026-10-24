package firs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestDojahVerifyTaxIDSendsDocumentedRequest(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/api/v1/kyc/tin" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "secret-key" {
			t.Errorf("Authorization = %q, want the raw secret key", got)
		}
		if got := r.Header.Get("AppId"); got != "app-id" {
			t.Errorf("AppId = %q", got)
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"entity":{"taxpayer_name":"Acme Limited","cac_reg_number":"1261103","tax_office":"Ikeja"}}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{
		TIN:          "18609323-0001",
		BusinessName: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyTaxID: %v", err)
	}
	if gotQuery != "tin=186093230001" {
		t.Fatalf("query = %q, want the digits-only TIN", gotQuery)
	}
	if got.Status != ports.VerifyVerified || !got.MatchName {
		t.Fatalf("result = %+v, want verified with a name match", got)
	}
	if got.RegisteredName != "Acme Limited" || got.ProviderRef != "DOJAH-TIN-186093230001" {
		t.Fatalf("result = %+v", got)
	}
}

func TestDojahVerifyTaxIDNameMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entity":{"taxpayer_name":"Other Holdings Ltd"}}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{
		TIN:          "186093230001",
		BusinessName: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyTaxID: %v", err)
	}
	if got.Status != ports.VerifyVerified || got.MatchName {
		t.Fatalf("result = %+v, want verified with a name mismatch for reviewer review", got)
	}
}

func TestDojahVerifyTaxIDNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: "186093230001"})
	if err != nil {
		t.Fatalf("VerifyTaxID: %v", err)
	}
	if got.Status != ports.VerifyNotFound {
		t.Fatalf("status = %q, want not_found", got.Status)
	}
}

func TestDojahVerifyTaxIDEmptyTIN(t *testing.T) {
	t.Parallel()
	if _, err := NewDojah("", "", "", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{}); err == nil {
		t.Fatal("an empty TIN must be an error")
	}
}

func TestDojahVerifyTaxIDHTTPFailures(t *testing.T) {
	t.Parallel()
	cases := []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusFailedDependency, http.StatusInternalServerError}
	for _, status := range cases {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()
			if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: "186093230001"}); err == nil {
				t.Fatalf("HTTP %d must be an error", status)
			}
		})
	}
}

func TestDojahVerifyTaxIDBadJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: "186093230001"}); err == nil {
		t.Fatal("an undecodable response must be an error")
	}
}

func TestDojahVerifyTaxIDMissingEntity(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entity":null}`))
	}))
	defer srv.Close()
	if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0).VerifyTaxID(context.Background(), ports.TaxIDRequest{TIN: "186093230001"}); err == nil {
		t.Fatal("a response without an entity must be an error")
	}
}
