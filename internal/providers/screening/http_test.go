package screening

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

func newHTTPServer(t *testing.T, handler http.HandlerFunc) *HTTP {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewHTTP(server.URL, "secret-key", 2*time.Second)
}

func TestHTTPScreenRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAPIKey string
	var gotBody map[string]any
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeJSON(t, w, map[string]any{
			"decision":      kyc.ScreenClear,
			"matched_names": []string{},
			"reference":     "REF-1",
			"message":       "no sanctions or PEP matches",
		})
	})
	decision, err := provider.Screen(context.Background(), ports.ScreeningRequest{
		LegalName:   "Ada Obi",
		DateOfBirth: "1990-01-02",
		PhoneNumber: "+2348012345678",
		Address:     "12 Marina, Lagos",
		CountryCode: "NG",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/screen" {
		t.Fatalf("expected POST /screen, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer secret-key" || gotAPIKey != "secret-key" {
		t.Fatalf("expected bearer + api key headers, got auth=%q api-key=%q", gotAuth, gotAPIKey)
	}
	if gotBody["legal_name"] != "Ada Obi" || gotBody["country_code"] != "NG" || gotBody["date_of_birth"] != "1990-01-02" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if decision.Decision != kyc.ScreenClear {
		t.Fatalf("decision = %q, want clear", decision.Decision)
	}
	if decision.ProviderRef != "REF-1" {
		t.Fatalf("provider ref = %q, want REF-1", decision.ProviderRef)
	}
}

func TestHTTPScreenDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name           string
		response       map[string]any
		want           string
		wantMatched    bool
		wantKnownRef   bool
	}{
		{
			"decision_clear", map[string]any{"decision": "clear", "reference": "r1"}, kyc.ScreenClear, false, true,
		},
		{
			"decision_possible", map[string]any{"decision": "possible", "matched_names": []string{"Senator Musa"}}, kyc.ScreenPossible, true, false,
		},
		{
			"decision_blocked", map[string]any{"decision": "blocked", "matched_names": []string{"Harold Sanctioned"}}, kyc.ScreenStrong, true, false,
		},
		{
			"status_match", map[string]any{"status": "match", "matched_names": []string{"Jo Drug Lord"}, "provider_ref": "rp"}, kyc.ScreenStrong, true, true,
		},
		{
			"status_no_hit", map[string]any{"status": "no_hit"}, kyc.ScreenClear, false, false,
		},
		{
			"matched_true", map[string]any{"matched": true, "reference": "r2"}, kyc.ScreenStrong, false, true,
		},
		{
			"matched_false", map[string]any{"matched": false, "report_id": "r3"}, kyc.ScreenClear, false, true,
		},
		{
			"status_unknown_ignored_with_decision", map[string]any{"status": "success", "decision": "possible"}, kyc.ScreenPossible, false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tc.response)
			})
			got, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
			if tc.wantMatched && len(got.MatchedNames) == 0 {
				t.Fatalf("expected matched names, got none")
			}
			if tc.wantKnownRef {
				if got.ProviderRef == "" {
					t.Fatalf("expected a known provider reference, got empty")
				}
				if strings.HasPrefix(got.ProviderRef, "HTTP-SCR-") {
					t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
				}
			}
		})
	}
}

func TestHTTPScreenFallsBackToGeneratedReference(t *testing.T) {
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"decision": "clear"})
	})
	got, err := provider.Screen(context.Background(), ports.ScreeningRequest{LegalName: "Ada Obi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "HTTP-SCR-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}

func TestHTTPScreenErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("server_error_with_message", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			writeJSON(t, w, map[string]any{"message": "upstream timeout"})
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "upstream timeout") {
			t.Fatalf("expected API error message, got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer server.Close()
		provider := NewHTTP(server.URL, "", 50*time.Millisecond)
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
	t.Run("unrecognized_decision", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"decision": "maybe"})
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized decision rejection, got %v", err)
		}
	})
}

func TestHTTPScreenEmptyName(t *testing.T) {
	provider := NewHTTP("http://example.test", "", time.Second)
	if _, err := provider.Screen(context.Background(), ports.ScreeningRequest{}); err == nil {
		t.Fatal("expected error for empty legal name")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}