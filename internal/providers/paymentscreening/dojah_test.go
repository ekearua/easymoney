package paymentscreening

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
	return NewDojah(server.URL, "app-id-1", "secret-key", 2*time.Second, 0.85, "lists-2026-10")
}

func TestDojahPaymentRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAppID string
	var gotBody map[string]any
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAppID = r.Header.Get("AppId")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeDojahJSON(t, w, map[string]any{
			"entity": map[string]any{
				"entity_id":    "REF-PSCR-1",
				"match_status": "Confirmed Match",
				"results": []map[string]any{{
					"source_type": "Sanction List",
					"name":        []string{"Ada Sanctioned"},
					"match":       true,
				}},
			},
		})
	})
	decision, err := provider.ScreenPayment(context.Background(), ports.PaymentScreenRequest{
		CounterpartyName: "Ada Obi",
		AccountNumber:    "0123456789",
		Rail:             "merchant_payout",
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
	if gotBody["schema"] != "individual" || gotBody["unique_reference"] != "merchant_payout:0123456789" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	properties, _ := gotBody["properties"].(map[string]any)
	if properties["names"] != "Ada Obi" {
		t.Fatalf("unexpected properties: %v", properties)
	}
	options, _ := gotBody["screening_options"].(map[string]any)
	if options["pep_check"] != true || options["sanction"] != true || options["match_threshold"] != 0.85 {
		t.Fatalf("unexpected screening options: %v", options)
	}
	if decision.Decision != kyc.ScreenBlocked {
		t.Fatalf("decision = %q, want blocked", decision.Decision)
	}
	if decision.ProviderRef != "REF-PSCR-1" || decision.ListVersion != "lists-2026-10" {
		t.Fatalf("ref/version = %q/%q", decision.ProviderRef, decision.ListVersion)
	}
	if len(decision.MatchedNames) != 1 || decision.MatchedNames[0] != "Ada Sanctioned" {
		t.Fatalf("matched names = %v", decision.MatchedNames)
	}
}

func TestDojahPaymentDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{"no_match", "No Match", kyc.ScreenClear},
		{"partially_matched", "Partially Matched", kyc.ScreenPossible},
		{"potential_match", "Potential Match", kyc.ScreenPossible},
		{"confirmed_match", "Confirmed Match", kyc.ScreenBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeDojahJSON(t, w, map[string]any{"entity": map[string]any{"match_status": tc.status}})
			})
			got, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
		})
	}
}

func TestDojahPaymentFallsBackToGeneratedReference(t *testing.T) {
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeDojahJSON(t, w, map[string]any{"entity": map[string]any{"match_status": "No Match"}})
	})
	got, err := provider.ScreenPayment(context.Background(), ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "DOJAH-PSCR-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}

func TestDojahPaymentErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("empty_name", func(t *testing.T) {
		provider := NewDojah("http://example.test", "app", "secret", time.Second, 0.85, "")
		if _, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{}); err == nil {
			t.Fatal("expected error for empty counterparty name")
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("wallet_empty", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusPaymentRequired)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "402") {
			t.Fatalf("expected 402 rejection, got %v", err)
		}
	})
	t.Run("upstream_failed", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusFailedDependency)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "424") {
			t.Fatalf("expected 424 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("unrecognized_status", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeDojahJSON(t, w, map[string]any{"entity": map[string]any{"match_status": "maybe"}})
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized status rejection, got %v", err)
		}
	})
	t.Run("missing_entity", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeDojahJSON(t, w, map[string]any{})
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Ada Obi"})
		if err == nil || !strings.Contains(err.Error(), "no payment-screening entity") {
			t.Fatalf("expected missing entity error, got %v", err)
		}
	})
}

func writeDojahJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
