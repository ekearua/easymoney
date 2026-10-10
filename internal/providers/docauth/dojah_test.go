package docauth

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

func TestDojahDocumentRoundTrip(t *testing.T) {
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
			"status":       "genuine",
			"confidence":   0.99,
			"face_matched": true,
			"reference":    "REF-DOC-1",
			"message":      "no signs of alteration",
		})
	})
	result, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		EvidenceKey:     "cac_verified",
		Document:        "CAC CERTIFICATE OF INCORPORATION",
		LegalName:       "Acme Ltd",
		FaceDocumentRef: "selfie-ref-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/document/analysis" {
		t.Fatalf("expected POST /document/analysis, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "secret-key" || gotAppID != "app-id-1" {
		t.Fatalf("expected raw auth + app id headers, got auth=%q app-id=%q", gotAuth, gotAppID)
	}
	if gotBody["document"] != "CAC CERTIFICATE OF INCORPORATION" || gotBody["face_document_ref"] != "selfie-ref-1" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if result.Status != ports.AuthenticityGenuine {
		t.Fatalf("status = %q, want genuine", result.Status)
	}
	if result.ProviderRef != "REF-DOC-1" {
		t.Fatalf("ref = %q, want REF-DOC-1", result.ProviderRef)
	}
	if !result.FaceMatched || result.Confidence != 0.99 {
		t.Fatalf("face_matched=%v confidence=%v, want true/0.99", result.FaceMatched, result.Confidence)
	}
}

func TestDojahDocumentStatusMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		response     map[string]any
		want         string
		wantKnownRef bool
	}{
		{"status_genuine", map[string]any{"status": "genuine", "reference": "r1"}, ports.AuthenticityGenuine, true},
		{"decision_authentic", map[string]any{"decision": "authentic"}, ports.AuthenticityGenuine, false},
		{"status_tampered", map[string]any{"status": "tampered"}, ports.AuthenticityTampered, false},
		{"decision_fraudulent", map[string]any{"decision": "fraudulent"}, ports.AuthenticityTampered, false},
		{"status_inconclusive", map[string]any{"status": "inconclusive"}, ports.AuthenticityInconclusive, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tc.response)
			})
			got, err := provider.VerifyDocumentAuthenticity(ctx, ports.DocumentAuthenticityRequest{Document: "CAC CERTIFICATE OF INCORPORATION"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (message %q)", got.Status, tc.want, got.Message)
			}
			if tc.wantKnownRef && strings.HasPrefix(got.ProviderRef, "DOJAH-") {
				t.Fatalf("expected a vendor reference, got generated %q", got.ProviderRef)
			}
		})
	}
}

func TestDojahDocumentErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, err := provider.VerifyDocumentAuthenticity(ctx, ports.DocumentAuthenticityRequest{Document: "CAC CERT"})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := provider.VerifyDocumentAuthenticity(ctx, ports.DocumentAuthenticityRequest{Document: "CAC CERT"})
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("unrecognized_status", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "maybe"})
		})
		_, err := provider.VerifyDocumentAuthenticity(ctx, ports.DocumentAuthenticityRequest{Document: "CAC CERT"})
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("expected unrecognized status rejection, got %v", err)
		}
	})
}

func TestDojahDocumentMissingReading(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second)
	if _, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{}); err == nil {
		t.Fatal("expected error for empty document reading")
	}
}
