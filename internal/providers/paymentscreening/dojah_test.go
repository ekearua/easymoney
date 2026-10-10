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
	return NewDojah(server.URL, "app-id-1", "secret-key", 2*time.Second, 0.85)
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"decision":      kyc.ScreenBlocked,
			"matched_names": []string{"Blocked Co"},
			"reference":     "REF-P-1",
			"message":       "counterparty interdiction",
		})
	})
	decision, err := provider.ScreenPayment(context.Background(), ports.PaymentScreenRequest{
		CounterpartyName: "Blocked Co",
		AccountNumber:    "0123456789",
		BankCode:         "044",
		AmountKobo:       500000,
		SubjectID:        "usr-1",
		Rail:             "merchant_payout",
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
	if gotBody["legal_name"] != "Blocked Co" || gotBody["amount"] != float64(500000) || gotBody["match_threshold"] != 0.85 {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if decision.Decision != kyc.ScreenBlocked {
		t.Fatalf("decision = %q, want blocked", decision.Decision)
	}
	if decision.ProviderRef != "REF-P-1" {
		t.Fatalf("ref = %q, want REF-P-1", decision.ProviderRef)
	}
}

func TestDojahPaymentDecisionMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		response     map[string]any
		want         string
		wantKnownRef bool
	}{
		{"decision_clear", map[string]any{"decision": "clear", "reference": "r1"}, kyc.ScreenClear, true},
		{"decision_possible", map[string]any{"decision": "possible"}, kyc.ScreenPossible, false},
		{"status_pep", map[string]any{"status": "pep"}, kyc.ScreenPossible, false},
		{"decision_blocked", map[string]any{"decision": "blocked"}, kyc.ScreenBlocked, false},
		{"status_sanctioned", map[string]any{"status": "sanctioned"}, kyc.ScreenBlocked, false},
		{"boolean_matched", map[string]any{"matched": true}, kyc.ScreenStrong, false},
		{"boolean_not_matched", map[string]any{"matched": false}, kyc.ScreenClear, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tc.response)
			})
			got, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Well Known Co"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (message %q)", got.Decision, tc.want, got.Message)
			}
			if tc.wantKnownRef && strings.HasPrefix(got.ProviderRef, "DOJAH-PSCR-") {
				t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
			}
		})
	}
}

func TestDojahPaymentErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Well Known Co"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Well Known Co"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("unrecognized_decision", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"decision": "maybe"})
		})
		_, err := provider.ScreenPayment(ctx, ports.PaymentScreenRequest{CounterpartyName: "Well Known Co"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized decision rejection, got %v", err)
		}
	})
}

func TestDojahPaymentEmptyName(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second, 0.85)
	if _, err := provider.ScreenPayment(context.Background(), ports.PaymentScreenRequest{}); err == nil {
		t.Fatal("expected error for empty counterparty name")
	}
}

func TestDojahPaymentGeneratedReference(t *testing.T) {
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"decision": "clear"})
	})
	got, err := provider.ScreenPayment(context.Background(), ports.PaymentScreenRequest{CounterpartyName: "Well Known Co"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "DOJAH-PSCR-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}
