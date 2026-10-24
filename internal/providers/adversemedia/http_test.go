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
	return NewHTTP(server.URL, "app-id-1", "secret-key", 2*time.Second, 0.85, "lists-1")
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
			"entity": map[string]any{
				"entity_id":    "REF-1",
				"match_status": "Confirmed Match",
				"risk_level":   "High",
				"results": []map[string]any{{
					"source_type":    "ADVERSE_MEDIA",
					"media_category": "fraud",
					"articles": []map[string]any{{
						"timestamp": "2026-01-02T00:00:00Z",
						"headline":  "record flagged",
						"source":    "https://news.example/1",
					}},
				}},
			},
		})
	})
	decision, err := provider.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{
		LegalName:   "Ada Obi",
		PhoneNumber: "+2348012345678",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/aml/screening" {
		t.Fatalf("expected POST /api/v2/aml/screening, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "secret-key" || gotAppID != "app-id-1" {
		t.Fatalf("expected raw auth + app id headers, got auth=%q app-id=%q", gotAuth, gotAppID)
	}
	if gotBody["schema"] != "individual" {
		t.Fatalf("expected individual schema, got %v", gotBody)
	}
	properties, _ := gotBody["properties"].(map[string]any)
	if properties["names"] != "Ada Obi" {
		t.Fatalf("unexpected properties: %v", properties)
	}
	options, _ := gotBody["screening_options"].(map[string]any)
	if options["adverse_media_check"] != true || options["match_threshold"] != 0.85 {
		t.Fatalf("unexpected screening options: %v", options)
	}
	if options["pep_check"] != false || options["sanction"] != false {
		t.Fatalf("adverse-media lane must not request PEP or sanctions screening: %v", options)
	}
	if decision.Decision != kyc.ScreenBlocked {
		t.Fatalf("decision = %q, want blocked", decision.Decision)
	}
	if decision.ProviderRef != "REF-1" || decision.ListVersion != "lists-1" {
		t.Fatalf("ref/version = %q/%q, want REF-1/lists-1", decision.ProviderRef, decision.ListVersion)
	}
	if len(decision.MatchedHits) != 1 || decision.MatchedHits[0].Title != "record flagged" {
		t.Fatalf("unexpected hits: %+v", decision.MatchedHits)
	}
}

func TestHTTPAdverseDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		status       string
		want         string
		wantKnownRef bool
	}{
		{"no_match", "No Match", kyc.ScreenClear, false},
		{"partially_matched", "Partially Matched", kyc.ScreenPossible, false},
		{"potential_match", "Potential Match", kyc.ScreenPossible, false},
		{"confirmed_match", "Confirmed Match", kyc.ScreenBlocked, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"entity": map[string]any{
					"entity_id":    "r1",
					"match_status": tc.status,
				}})
			})
			got, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
			if tc.wantKnownRef && got.ProviderRef != "r1" {
				t.Fatalf("expected vendor reference r1, got %q", got.ProviderRef)
			}
		})
	}
}

func TestHTTPAdverseFallsBackToGeneratedReference(t *testing.T) {
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"entity": map[string]any{"match_status": "No Match"}})
	})
	got, err := provider.ScreenAdverseMedia(context.Background(), ports.AdverseMediaRequest{LegalName: "Ada Obi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "ADVERSE-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}

func TestHTTPAdverseDojahProviderName(t *testing.T) {
	provider := NewDojah("https://sandbox.dojah.io", "app", "secret", time.Second, 0.85, "")
	if provider.ProviderName() != "dojah" {
		t.Fatalf("provider = %q, want dojah", provider.ProviderName())
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
	t.Run("wallet_empty", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPaymentRequired)
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "402") {
			t.Fatalf("expected 402 rejection, got %v", err)
		}
	})
	t.Run("upstream_failed", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusFailedDependency)
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "424") {
			t.Fatalf("expected 424 rejection, got %v", err)
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
		provider := NewHTTP(server.URL, "app", "secret", 50*time.Millisecond, 0.85, "")
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
	t.Run("unrecognized_status", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"entity": map[string]any{"match_status": "maybe"}})
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized status rejection, got %v", err)
		}
	})
	t.Run("missing_entity", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{})
		})
		_, err := provider.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{LegalName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "no adverse-media entity") {
			t.Fatalf("expected missing entity error, got %v", err)
		}
	})
}

func TestHTTPAdverseEmptyName(t *testing.T) {
	provider := NewHTTP("http://example.test", "app", "secret", time.Second, 0.85, "")
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
