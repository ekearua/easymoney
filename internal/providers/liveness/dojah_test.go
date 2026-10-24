package liveness

import (
	"context"
	"encoding/base64"
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

func selfieBase64() string {
	return base64.StdEncoding.EncodeToString([]byte("selfie-bytes"))
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
		writeDojahJSON(t, w, map[string]any{
			"entity": map[string]any{
				"liveness": map[string]any{
					"liveness_check":       true,
					"liveness_probability": 97.5,
				},
				"face": map[string]any{"face_detected": true},
			},
		})
	})
	result, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{
		UserID:     "user-1",
		CaptureRef: selfieBase64(),
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
	if len(gotBody) != 1 || gotBody["image"] != selfieBase64() {
		t.Fatalf("expected only the base64 image field, got %v", gotBody)
	}
	if result.Status != ports.LivenessPassed {
		t.Fatalf("status = %q, want passed", result.Status)
	}
	if result.Confidence != 0.975 {
		t.Fatalf("confidence = %v, want 0.975", result.Confidence)
	}
	if !strings.HasPrefix(result.ProviderRef, "DOJAH-LIVE-") {
		t.Fatalf("expected generated reference, got %q", result.ProviderRef)
	}
}

func TestDojahLivenessStripsDataURIPrefix(t *testing.T) {
	var gotBody map[string]any
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeDojahJSON(t, w, map[string]any{"entity": map[string]any{
			"liveness": map[string]any{"liveness_check": true},
		}})
	})
	_, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{
		CaptureRef: "data:image/jpeg;base64," + selfieBase64(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["image"] != selfieBase64() {
		t.Fatalf("expected prefix stripped, got %v", gotBody["image"])
	}
}

func TestDojahLivenessFailedCheck(t *testing.T) {
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeDojahJSON(t, w, map[string]any{"entity": map[string]any{
			"liveness": map[string]any{"liveness_check": false, "liveness_probability": 12},
			"face":     map[string]any{"face_detected": false, "message": "no face found"},
		}})
	})
	got, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{CaptureRef: selfieBase64()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ports.LivenessFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Confidence != 0.12 {
		t.Fatalf("confidence = %v, want 0.12", got.Confidence)
	}
}

func TestDojahLivenessRejectsNonImageCapture(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second)
	for _, ref := range []string{"", "chat-confirm:8f14e45f", "browser-edd:user-1:camera"} {
		if _, err := provider.VerifyLiveness(context.Background(), ports.LivenessRequest{CaptureRef: ref}); err == nil {
			t.Fatalf("expected error for non-image capture %q", ref)
		}
	}
}

func TestDojahLivenessErrors(t *testing.T) {
	ctx := context.Background()
	request := ports.LivenessRequest{CaptureRef: selfieBase64()}
	cases := []struct {
		name     string
		status   int
		contains string
	}{
		{"unauthorized", http.StatusUnauthorized, "401"},
		{"wallet_empty", http.StatusPaymentRequired, "402"},
		{"upstream_failed", http.StatusFailedDependency, "424"},
		{"rate_limited", http.StatusTooManyRequests, "429"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			_, err := provider.VerifyLiveness(ctx, request)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected %s rejection, got %v", tc.contains, err)
			}
		})
	}
	t.Run("missing_entity", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeDojahJSON(t, w, map[string]any{})
		})
		_, err := provider.VerifyLiveness(ctx, request)
		if err == nil || !strings.Contains(err.Error(), "no liveness entity") {
			t.Fatalf("expected missing entity error, got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer server.Close()
		provider := NewDojah(server.URL, "app", "secret", 50*time.Millisecond)
		if _, err := provider.VerifyLiveness(ctx, request); err == nil {
			t.Fatal("expected timeout error")
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
