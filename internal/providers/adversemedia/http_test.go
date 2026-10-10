package adversemedia

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
	return NewHTTP(server.URL, "app-id-1", "secret-key", 2*time.Second, 0.85)
}

func TestHTTPAdverseRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAppID string
	var gotBody map[string]any
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAppID = r.Header.Get("AppId")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeJSON(t, w, map[string]any{
			"decision": kyc.ScreenStrong,
			"hits": []map[string]any{{
				"source": "OpenSanctions", "title": "record flagged",
				"published_at": "2026-01-02", "reference": "HIT-1", "list_version": "v9",
			}},
			"reference":    "REF-1",
			"list_version": "v9",
			"message":      "adverse-media hit",
		})
	})
	decision, err := provider.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{
		LegalName:   "Ada Obi",
		PhoneNumber: "+2348012345678",
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
	if gotBody["legal_name"] != "Ada Obi" || gotBody["phone"] != "+2348012345678" || gotBody["match_threshold"] != 0.85 {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if decision.Decision != kyc.ScreenStrong {
		t.Fatalf("decision = %q, want strong", decision.Decision)
	}
	if decision.ProviderRef != "REF-1" || decision.ListVersion != "v9" {
		t.Fatalf("ref/version = %q/%q, want REF-1/v9", decision.ProviderRef, decision.ListVersion)
	}
	if len(decision.MatchedHits) != 1 || decision.MatchedHits[0].ProviderRef != "HIT-1" {
		t.Fatalf("unexpected hits: %+v", decision.MatchedHits)
	}
}

func TestHTTPAdverseDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		response     map[string]any
		want         string
		wantKnownRef bool
	}{
		{"decision_clear", map[string]any{"decision": "clear", "reference": "r1"}, kyc.ScreenClear, true},
		{"decision_possible", map[string]any{"decision": "possible"}, kyc.ScreenPossible, false},
		{"alerts_is_possible", map[string]any{"decision": "alerts"}, kyc.ScreenPossible, false},
		{"decision_blocked", map[string]any{"decision": "blocked"}, kyc.ScreenBlocked, false},
		{"status_hit", map[string]any{"status": "hit", "provider_ref": "rp"}, kyc.ScreenStrong, true},
		{"status_no_adverse_media", map[string]any{"status": "no_adverse_media"}, kyc.ScreenClear, false},
		{"sanctioned_is_blocked", map[string]any{"decision": "sanctioned"}, kyc.ScreenBlocked, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tc.response)
			})
			got, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
			if tc.wantKnownRef && strings.HasPrefix(got.ProviderRef, "HTTP-AMS-") {
				t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
			}
		})
	}
}

func TestHTTPAdverseFallsBackToGeneratedReference(t *testing.T) {
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"decision": "clear"})
	})
	got, err := provider.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{LegalName: "Ada Obi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "HTTP-AMS-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}

func TestHTTPAdverseErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("server_error_with_message", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			writeJSON(t, w, map[string]any{"message": "upstream timeout"})
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "upstream timeout") {
			t.Fatalf("expected API error message, got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer server.Close()
		provider := NewHTTP(server.URL, "app", "secret", 50*time.Millisecond, 0.85)
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
	t.Run("unrecognized_decision", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"decision": "maybe"})
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized decision rejection, got %v", err)
		}
	})
}

func TestHTTPAdverseEmptyName(t *testing.T) {
	provider := NewHTTP("http://example.test", "app", "secret", time.Second, 0.85)
	if _, err := provider.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{}); err == nil {
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
