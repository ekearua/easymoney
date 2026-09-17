package sms

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

func newHTTPServer(t *testing.T, handler http.HandlerFunc) *HTTP {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewHTTP(server.URL, "secret-key", "Xego", 2*time.Second)
}

func TestHTTPSendRoundTrip(t *testing.T) {
	var gotMethod, gotAuth, gotAPIKey string
	var gotBody map[string]any
	provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"message_id":"SMS-1"}`)
	})
	id, err := provider.Send(context.Background(), "+2348012345678", "Your Xego request is fulfilled.")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotAuth != "Bearer secret-key" || gotAPIKey != "secret-key" {
		t.Fatalf("expected bearer + api key headers, got auth=%q api-key=%q", gotAuth, gotAPIKey)
	}
	if gotBody["to"] != "+2348012345678" || gotBody["message"] != "Your Xego request is fulfilled." || gotBody["sender_id"] != "Xego" {
		t.Fatalf("unexpected request body: %v", gotBody)
	}
	if id != "SMS-1" {
		t.Fatalf("provider id = %q, want SMS-1", id)
	}
}

func TestHTTPSendProviderRefExtraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"message_id", `{"message_id":"a"}`, "a"},
		{"messageId", `{"messageId":"b"}`, "b"},
		{"msg_id", `{"msg_id":"c"}`, "c"},
		{"id", `{"id":"d"}`, "d"},
		{"reference", `{"reference":"e"}`, "e"},
		{"none", `{"status":"queued"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, tc.body)
			})
			id, err := provider.Send(context.Background(), "+2348012345678", "hi")
			if err != nil {
				t.Fatal(err)
			}
			if id != tc.want {
				t.Fatalf("provider id = %q, want %q", id, tc.want)
			}
		})
	}
}

func TestHTTPSendErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unauthorized", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		if _, err := provider.Send(ctx, "+2348012345678", "hi"); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 rejection, got %v", err)
		}
	})
	t.Run("rate_limited", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		if _, err := provider.Send(ctx, "+2348012345678", "hi"); err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 rejection, got %v", err)
		}
	})
	t.Run("server_error_with_message", func(t *testing.T) {
		provider := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":"carrier rejected"}`)
		})
		if _, err := provider.Send(ctx, "+2348012345678", "hi"); err == nil || !strings.Contains(err.Error(), "carrier rejected") {
			t.Fatalf("expected API error message, got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer server.Close()
		provider := NewHTTP(server.URL, "", "Xego", 50*time.Millisecond)
		if _, err := provider.Send(ctx, "+2348012345678", "hi"); err == nil {
			t.Fatal("expected timeout error")
		}
	})
}

func TestHTTPSendValidation(t *testing.T) {
	provider := NewHTTP("http://example.test", "", "Xego", time.Second)
	if _, err := provider.Send(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected error for empty recipient")
	}
	if _, err := provider.Send(context.Background(), "+2348012345678", ""); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestHTTPSendImplementsPort(t *testing.T) {
	var _ ports.SMSSender = NewHTTP("http://example.test", "", "Xego", time.Second)
}
