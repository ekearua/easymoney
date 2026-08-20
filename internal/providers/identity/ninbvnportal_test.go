package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func TestNINBVNPortal_VerifyNIN_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/nin-verification" {
			t.Errorf("expected /api/nin-verification, got %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key test-key, got %s", r.Header.Get("x-api-key"))
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["nin"] != "12345678901" {
			t.Errorf("expected nin 12345678901, got %s", body["nin"])
		}
		if body["consent"] != "true" {
			t.Errorf("expected consent true, got %s", body["consent"])
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ninbvnAPIResponse{
			Status:   "success",
			ReportID: "RPT-001",
			Message:  "success",
			Data: ninbvnRecord{
				ReportID:  "RPT-001",
				Firstname: "Amina",
				Surname:   "Abubakar",
				Birthdate: "1992-05-24",
			},
		})
	}))
	defer srv.Close()

	p := NewNINBVNPortal(srv.URL, "test-key", 10*time.Second)
	result, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:      "nin",
		IDNumber:    "12345678901",
		LegalName:   "Amina Abubakar",
		DateOfBirth: "1992-05-24",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "verified" {
		t.Errorf("expected verified, got %s", result.Status)
	}
	if result.ProviderRef != "RPT-001" {
		t.Errorf("expected RPT-001, got %s", result.ProviderRef)
	}
	if !result.MatchName {
		t.Error("expected MatchName true")
	}
	if !result.MatchDOB {
		t.Error("expected MatchDOB true")
	}
}

func TestNINBVNPortal_VerifyBVN_NameMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ninbvnAPIResponse{
			Status:   "success",
			ReportID: "RPT-002",
			Message:  "success",
			Data: ninbvnRecord{
				ReportID:  "RPT-002",
				Firstname: "John",
				Surname:   "Doe",
				Birthdate: "1992-05-24",
			},
		})
	}))
	defer srv.Close()

	p := NewNINBVNPortal(srv.URL, "test-key", 10*time.Second)
	result, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:      "bvn",
		IDNumber:    "22222222222",
		LegalName:   "Jane Smith",
		DateOfBirth: "1992-05-24",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "mismatch" {
		t.Errorf("expected mismatch, got %s", result.Status)
	}
	if result.MatchName {
		t.Error("expected MatchName false")
	}
	if !result.MatchDOB {
		t.Error("expected MatchDOB true")
	}
}

func TestNINBVNPortal_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ninbvnAPIResponse{
			Status:  "error",
			Message: "NIN record not found",
		})
	}))
	defer srv.Close()

	p := NewNINBVNPortal(srv.URL, "test-key", 10*time.Second)
	result, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:   "nin",
		IDNumber: "99999999999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" {
		t.Errorf("expected not_found, got %s", result.Status)
	}
}

func TestNINBVNPortal_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "invalid key"})
	}))
	defer srv.Close()

	p := NewNINBVNPortal(srv.URL, "bad-key", 10*time.Second)
	_, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:   "nin",
		IDNumber: "12345678901",
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestNINBVNPortal_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := NewNINBVNPortal(srv.URL, "test-key", 10*time.Second)
	_, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:   "nin",
		IDNumber: "12345678901",
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
}

func TestNINBVNPortal_InvalidIDType(t *testing.T) {
	p := NewNINBVNPortal("http://localhost", "key", 5*time.Second)
	_, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:   "passport",
		IDNumber: "12345678901",
	})
	if err == nil {
		t.Fatal("expected error for invalid ID type")
	}
}

func TestNINBVNPortal_InvalidNumberLength(t *testing.T) {
	p := NewNINBVNPortal("http://localhost", "key", 5*time.Second)
	_, err := p.VerifyIdentity(context.Background(), ports.IdentityVerificationRequest{
		IDType:   "nin",
		IDNumber: "123",
	})
	if err == nil {
		t.Fatal("expected error for short number")
	}
}

func TestNINBVNPortal_ProviderName(t *testing.T) {
	p := NewNINBVNPortal("http://localhost", "key", 5*time.Second)
	if p.ProviderName() != "ninbvnportal" {
		t.Errorf("expected ninbvnportal, got %s", p.ProviderName())
	}
}

func TestNINBVNPortal_DefaultTimeout(t *testing.T) {
	p := NewNINBVNPortal("http://localhost", "key", 0)
	if p.httpClient.Timeout != 30*time.Second {
		t.Errorf("expected default 30s timeout, got %v", p.httpClient.Timeout)
	}
}

func TestNINBVNPortal_InstalledInterface(t *testing.T) {
	var _ ports.IdentityVerifier = (*NINBVNPortal)(nil)
}
