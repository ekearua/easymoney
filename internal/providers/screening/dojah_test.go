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

func newDojahServer(t *testing.T, handler http.HandlerFunc) *Dojah {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewDojah(server.URL, "app-id-1", "secret-key", 2*time.Second, 0.85)
}

func TestDojahScreenRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAppID string
	var gotBody map[string]any
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAppID = r.Header.Get("AppId")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"decision":      kyc.ScreenStrong,
			"matched_names": []string{"Ada Sanctioned"},
			"reference":     "REF-DOJUAH-1",
			"message":       "sanctions match",
		})
	})
	decision, err := provider.Screen(context.Background(), ports.ScreeningRequest{
		LegalName:   "Ada Obi",
		DateOfBirth: "1990-01-02",
		PhoneNumber: "+2348012345678",
		CountryCode: "NG",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/aml/v2/screening" {
		t.Fatalf("expected POST /api/v1/aml/v2/screening, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "secret-key" || gotAppID != "app-id-1" {
		t.Fatalf("expected raw auth + app id headers, got auth=%q app-id=%q", gotAuth, gotAppID)
	}
	if gotBody["legal_name"] != "Ada Obi" || gotBody["country_code"] != "NG" || gotBody["match_threshold"] != 0.85 {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if decision.Decision != kyc.ScreenStrong {
		t.Fatalf("decision = %q, want strong", decision.Decision)
	}
	if decision.ProviderRef != "REF-DOJUAH-1" {
		t.Fatalf("ref = %q, want REF-DOJUAH-1", decision.ProviderRef)
	}
	if len(decision.MatchedNames) != 1 || decision.MatchedNames[0] != "Ada Sanctioned" {
		t.Fatalf("unexpected matched names: %+v", decision.MatchedNames)
	}
}

func TestDojahScreenDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		response     map[string]any
		want         string
		wantKnownRef bool
	}{
		{"decision_clear", map[string]any{"decision": "clear", "reference": "r1"}, kyc.ScreenClear, true},
		{"decision_possible", map[string]any{"decision": "possible"}, kyc.ScreenPossible, false},
		{"status_hit", map[string]any{"status": "hit"}, kyc.ScreenStrong, false},
		{"status_no_match", map[string]any{"status": "no_match"}, kyc.ScreenClear, false},
		{"sanctioned_is_strong", map[string]any{"decision": "sanctioned"}, kyc.ScreenStrong, false},
		{"boolean_matched", map[string]any{"matched": true}, kyc.ScreenStrong, false},
		{"boolean_not_matched", map[string]any{"matched": false}, kyc.ScreenClear, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tc.response)
			})
			got, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
			if tc.wantKnownRef && strings.HasPrefix(got.ProviderRef, "DOJAH-SCR-") {
				t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
			}
		})
	}
}

func TestDojahScreenFallsBackToGeneratedReference(t *testing.T) {
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"decision": "clear"})
	})
	got, err := provider.Screen(context.Background(), ports.ScreeningRequest{LegalName: "Ada Obi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "DOJAH-SCR-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}

func TestDojahScreenErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("server_error_with_message", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"message": "upstream timeout"})
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
		provider := NewDojah(server.URL, "app", "secret", 50*time.Millisecond, 0.85)
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
	t.Run("unrecognized_decision", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"decision": "maybe"})
		})
		_, err := provider.Screen(ctx, ports.ScreeningRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized decision rejection, got %v", err)
		}
	})
}

func TestDojahScreenEmptyName(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second, 0.85)
	if _, err := provider.Screen(context.Background(), ports.ScreeningRequest{}); err == nil {
		t.Fatal("expected error for empty legal name")
	}
}
