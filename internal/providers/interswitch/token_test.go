package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTokenManagerAccessToken(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Basic "+base64Encode("cid:secret") {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "profile" {
			t.Errorf("unexpected body %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "abc123",
			TokenType:    "Bearer",
			ExpiresIn:    43200,
			MerchantCode: "MX6001",
			TerminalID:   "TD1234",
		})
	}))
	defer srv.Close()

	tm := NewTokenManager("cid", "secret", srv.URL, srv.Client())
	ctx := context.Background()

	token, err := tm.AccessToken(ctx)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if token != "abc123" {
		t.Fatalf("got token %q", token)
	}
	if tm.TerminalID() != "TD1234" {
		t.Fatalf("got terminal id %q", tm.TerminalID())
	}
	// Second call within expiry must not hit the server again.
	if _, err := tm.AccessToken(ctx); err != nil {
		t.Fatalf("cached AccessToken: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 token request, got %d", count)
	}
}

func TestTokenManagerRefreshesAfterExpiry(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 1, TerminalID: "TD"})
	}))
	defer srv.Close()

	tm := NewTokenManager("cid", "secret", srv.URL, srv.Client())
	tm.expiresAt = time.Now().Add(-time.Second)
	tm.token = "stale"

	if _, err := tm.AccessToken(context.Background()); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected refresh, got %d requests", count)
	}
}

func TestTokenManagerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	tm := NewTokenManager("cid", "secret", srv.URL, srv.Client())
	if _, err := tm.AccessToken(context.Background()); err == nil {
		t.Fatal("expected error for unauthorized token request")
	}
}

func TestTokenManagerMissingCredentials(t *testing.T) {
	tm := NewTokenManager("", "", "http://example.com/token", http.DefaultClient)
	if _, err := tm.AccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "client ID and secret") {
		t.Fatalf("expected missing-credentials error, got %v", err)
	}
}

func TestTokenManagerExpiryBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 300})
	}))
	defer srv.Close()
	tm := NewTokenManager("cid", "secret", srv.URL, srv.Client())
	if _, err := tm.AccessToken(context.Background()); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	want := tm.expiresAt
	if time.Until(want) > 295*time.Second {
		t.Fatalf("expiry not buffered by 5 minutes: %v until expiry", time.Until(want))
	}
}