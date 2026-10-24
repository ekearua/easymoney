package cac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestDojahVerifyCorporateSendsDocumentedRequest(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/api/v1/kyc/cac/basic" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "secret-key" {
			t.Errorf("Authorization = %q, want the raw secret key", got)
		}
		if r.Header.Get("Authorization") == "Bearer secret-key" {
			t.Error("Dojah rejects the Bearer prefix")
		}
		if got := r.Header.Get("AppId"); got != "app-id" {
			t.Errorf("AppId = %q", got)
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"entity":{"company_name":"Acme Limited","type_of_company":"COMPANY","status":"Active","rc_number":"1261103"}}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "COMPANY").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{
		RegistrationNumber: "RC1261103",
		BusinessName:       "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyCorporate: %v", err)
	}
	if gotQuery != "company_type=COMPANY&rc_number=RC1261103" {
		t.Fatalf("query = %q", gotQuery)
	}
	if got.Status != ports.VerifyVerified || !got.MatchName {
		t.Fatalf("result = %+v, want verified with a name match", got)
	}
	if got.RegisteredName != "Acme Limited" || got.ProviderRef != "DOJAH-CAC-1261103" {
		t.Fatalf("result = %+v", got)
	}
}

func TestDojahVerifyCorporateNameMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entity":{"company_name":"Other Holdings Ltd","status":"Active"}}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{
		RegistrationNumber: "RC1261103",
		BusinessName:       "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyCorporate: %v", err)
	}
	if got.Status != ports.VerifyVerified || got.MatchName {
		t.Fatalf("result = %+v, want verified with a name mismatch for reviewer review", got)
	}
}

func TestDojahVerifyCorporateInactiveCompanyIsMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entity":{"company_name":"Acme Ltd","status":"Not Active"}}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{
		RegistrationNumber: "RC1261103",
		BusinessName:       "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("VerifyCorporate: %v", err)
	}
	if got.Status != ports.VerifyMismatch || got.MatchName {
		t.Fatalf("result = %+v, want a mismatch for an inactive company", got)
	}
}

func TestDojahVerifyCorporateNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}))
	defer srv.Close()

	got, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC9999999"})
	if err != nil {
		t.Fatalf("VerifyCorporate: %v", err)
	}
	if got.Status != ports.VerifyNotFound {
		t.Fatalf("status = %q, want not_found", got.Status)
	}
}

func TestDojahVerifyCorporateRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entity":{"company_name":"Acme Ltd","status":"Suspended"}}`))
	}))
	defer srv.Close()

	if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC1261103"}); err == nil {
		t.Fatal("an unrecognized CAC status must be an error, never a pass")
	}
}

func TestDojahVerifyCorporateEmptyNumber(t *testing.T) {
	t.Parallel()
	if _, err := NewDojah("", "", "", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{}); err == nil {
		t.Fatal("an empty registration number must be an error")
	}
}

func TestDojahVerifyCorporateHTTPFailures(t *testing.T) {
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
			if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC1261103"}); err == nil {
				t.Fatalf("HTTP %d must be an error", status)
			}
		})
	}
}

func TestDojahVerifyCorporateBadJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC1261103"}); err == nil {
		t.Fatal("an undecodable response must be an error")
	}
}

func TestDojahVerifyCorporateMissingEntity(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = json.Marshal(map[string]any{"entity": nil})
	}))
	defer srv.Close()
	if _, err := NewDojah(srv.URL, "app-id", "secret-key", 0, "").VerifyCorporate(context.Background(), ports.CorporateRegistryRequest{RegistrationNumber: "RC1261103"}); err == nil {
		t.Fatal("a response without an entity must be an error")
	}
}
