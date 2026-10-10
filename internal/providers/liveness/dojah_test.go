package liveness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func newDojahServer(t *testing.T, handler http.HandlerFunc) *Dojah {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewDojah(server.URL, "app-id-1", "secret-key", 2*time.Second)
}

func TestDojahLivenessRoundTrip(t *testing.T) {
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
			"status":     "passed",
			"confidence": 0.97,
			"reference":  "REF-LIVE-1",
			"message":    "live capture verified",
		})
	})
	result, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{
		UserID:      "usr-1",
		LegalName:   "Ada Obi",
		PhoneNumber: "+2348012345678",
		CaptureRef:  "selfie-ref-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/ml/liveness" {
		t.Fatalf("expected POST /api/v1/ml/liveness, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "secret-key" || gotAppID != "app-id-1" {
		t.Fatalf("expected raw auth + app id headers, got auth=%q app-id=%q", gotAuth, gotAppID)
	}
	if gotBody["user_id"] != "usr-1" || gotBody["selfie_ref"] != "selfie-ref-1" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if result.Status != ports.LivenessPassed {
		t.Fatalf("status = %q, want passed", result.Status)
	}
	if result.ProviderRef != "REF-LIVE-1" {
		t.Fatalf("ref = %q, want REF-LIVE-1", result.ProviderRef)
	}
	if result.Confidence != 0.97 {
		t.Fatalf("confidence = %v, want 0.97", result.Confidence)
	}
}

func TestDojahLivenessStatusMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		response     map[string]any
		want         string
		wantKnownRef bool
	}{
		{"status_passed", map[string]any{"status": "passed", "reference": "r1"}, ports.LivenessPassed, true},
		{"decision_live", map[string]any{"decision": "live"}, ports.LivenessPassed, false},
		{"status_failed", map[string]any{"status": "failed"}, ports.LivenessFailed, false},
		{"decision_spoof", map[string]any{"decision": "spoof"}, ports.LivenessFailed, false},
		{"status_review", map[string]any{"status": "review"}, ports.LivenessReview, false},
		{"status_undetermined", map[string]any{"status": "undetermined"}, ports.LivenessReview, false},
		{"boolean_live", map[string]any{"live": true}, ports.LivenessPassed, false},
		{"boolean_not_live", map[string]any{"live": false}, ports.LivenessFailed, false},
		{"boolean_passed", map[string]any{"passed": true}, ports.LivenessPassed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tc.response)
			})
			got, err := provider.VerifyLiveness(ctx, ports.LivenessRequest{CaptureRef: "selfie-ref-1"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (message %q)", got.Status, tc.want, got.Message)
			}
			if tc.wantKnownRef && strings.HasPrefix(got.ProviderRef, "DOJAH-LIVE-") {
				t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
			}
		})
	}
}

func TestDojahLivenessErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.VerifyLiveness(ctx, ports.LivenessRequest{CaptureRef: "selfie-ref-1"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.VerifyLiveness(ctx, ports.LivenessRequest{CaptureRef: "selfie-ref-1"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("unrecognized_status", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "maybe"})
		})
		_, err := provider.VerifyLiveness(ctx, ports.LivenessRequest{CaptureRef: "selfie-ref-1"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized status rejection, got %v", err)
		}
	})
}

func TestDojahLivenessMissingCapture(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second)
	if _, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{}); err == nil {
		t.Fatal("expected error for missing capture ref")
	}
}

func TestDojahLivenessGeneratedReference(t *testing.T) {
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "passed"})
	})
	got, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{CaptureRef: "selfie-ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ProviderRef, "DOJAH-LIVE-") {
		t.Fatalf("expected generated reference, got %q", got.ProviderRef)
	}
}
