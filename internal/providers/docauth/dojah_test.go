package docauth

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

func documentBase64() string {
	return base64.StdEncoding.EncodeToString([]byte("document-image-bytes"))
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
		writeDojahJSON(t, w, map[string]any{
			"entity": map[string]any{
				"status": map[string]any{
					"overall_status": 1,
					"reason":         "VALID",
					"document_type":  "CAC",
				},
			},
		})
	})
	result, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		EvidenceKey: "cac_verified",
		Document:    documentBase64(),
		LegalName:   "Acme Ltd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/document/analysis" {
		t.Fatalf("expected POST /api/v1/document/analysis, got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "secret-key" || gotAppID != "app-id-1" {
		t.Fatalf("expected raw auth + app id headers, got auth=%q app-id=%q", gotAuth, gotAppID)
	}
	if gotBody["input_type"] != "base64" || gotBody["imagefrontside"] != documentBase64() {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if result.Status != ports.AuthenticityGenuine {
		t.Fatalf("status = %q, want genuine", result.Status)
	}
	if !strings.HasPrefix(result.ProviderRef, "DOJAH-") {
		t.Fatalf("expected generated reference, got %q", result.ProviderRef)
	}
}

func TestDojahDocumentAcceptsURL(t *testing.T) {
	var gotBody map[string]any
	provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeDojahJSON(t, w, map[string]any{"entity": map[string]any{
			"status": map[string]any{"overall_status": 0, "reason": "NOT_VALID"},
		}})
	})
	result, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		Document: "https://files.example/doc.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["input_type"] != "url" || gotBody["imagefrontside"] != "https://files.example/doc.jpg" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if result.Status != ports.AuthenticityTampered {
		t.Fatalf("status = %q, want tampered", result.Status)
	}
}

func TestDojahDocumentStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"valid", 1, ports.AuthenticityGenuine},
		{"invalid", 0, ports.AuthenticityTampered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeDojahJSON(t, w, map[string]any{"entity": map[string]any{
					"status": map[string]any{"overall_status": tc.status},
				}})
			})
			got, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{Document: documentBase64()})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestDojahDocumentRejectsTextOnlyEvidence(t *testing.T) {
	provider := NewDojah("http://example.test", "app", "secret", time.Second)
	_, err := provider.VerifyDocumentAuthenticity(context.Background(), ports.DocumentAuthenticityRequest{
		Document: "RC1234567 LAGOS LUNCHBOX LTD",
	})
	if err == nil || !strings.Contains(err.Error(), "requires the document image") {
		t.Fatalf("expected image-required error, got %v", err)
	}
}

func TestDojahDocumentErrors(t *testing.T) {
	ctx := context.Background()
	request := ports.DocumentAuthenticityRequest{Document: documentBase64()}
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
			_, err := provider.VerifyDocumentAuthenticity(ctx, request)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected %s rejection, got %v", tc.contains, err)
			}
		})
	}
	t.Run("missing_entity", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeDojahJSON(t, w, map[string]any{})
		})
		_, err := provider.VerifyDocumentAuthenticity(ctx, request)
		if err == nil || !strings.Contains(err.Error(), "no document-analysis entity") {
			t.Fatalf("expected missing entity error, got %v", err)
		}
	})
	t.Run("unrecognized_status", func(t *testing.T) {
		provider := newDojahServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeDojahJSON(t, w, map[string]any{"entity": map[string]any{
				"status": map[string]any{"overall_status": 7},
			}})
		})
		_, err := provider.VerifyDocumentAuthenticity(ctx, request)
		if err == nil || !strings.Contains(err.Error(), "unrecognized document authenticity status") {
			t.Fatalf("expected unrecognized status error, got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer server.Close()
		provider := NewDojah(server.URL, "app", "secret", 50*time.Millisecond)
		if _, err := provider.VerifyDocumentAuthenticity(ctx, request); err == nil {
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
