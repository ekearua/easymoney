package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestPayoutSuccess(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transferPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{
			ResponseCode:        "90000",
			ResponseDescription: "Accepted",
			TransferRef:         "ISW-REF-001",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "3010000001"})
	result, err := client.Payout(context.Background(), ports.PayoutRequest{
		Reference:     "batch_1",
		AccountName:   "John",
		AccountNumber: "3010000002",
		BankCode:      "999292",
		AmountKobo:    100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.ExternalRef != "ISW-REF-001" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPayoutDeclined(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{
			ResponseCode:        "01",
			ResponseDescription: "Insufficient funds",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, SourceAccount: "301"})
	result, err := client.Payout(context.Background(), ports.PayoutRequest{Reference: "r", AccountNumber: "302", BankCode: "011", AmountKobo: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Message, "Insufficient") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPayoutMissingSourceAccount(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost"})
	_, err := client.Payout(context.Background(), ports.PayoutRequest{Reference: "r", AccountNumber: "302", BankCode: "011", AmountKobo: 1000})
	if err == nil || !strings.Contains(err.Error(), "source account") {
		t.Fatalf("expected source account error, got %v", err)
	}
}

func TestPayoutMissingDestinationAccount(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", SourceAccount: "301"})
	_, err := client.Payout(context.Background(), ports.PayoutRequest{Reference: "r", AmountKobo: 1000})
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("expected destination error, got %v", err)
	}
}