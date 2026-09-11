package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestRefundSuccess(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != refundPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refundResponse{
			ResponseCode:        "Z0",
			ResponseDescription: "Refund accepted",
			RefundReference:     "ISW-REF-001",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	result, err := client.Refund(context.Background(), ports.RefundRequest{
		PaymentID:         "parent-ref",
		PaymentAmountKobo: 50_000,
		AmountKobo:        50_000,
		Currency:          "NGN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.RefundID == "" {
		t.Fatalf("unexpected refund result: %+v", result)
	}
}

func TestRefundPartialAmountUsesPartType(t *testing.T) {
	t.Parallel()
	var gotType string
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req refundRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotType = req.RefundType
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refundResponse{ResponseCode: "Z0", RefundReference: "R"})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	_, err := client.Refund(context.Background(), ports.RefundRequest{
		PaymentID:         "p1",
		PaymentAmountKobo: 100_000,
		AmountKobo:        20_000,
		Currency:          "NGN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "PARTIAL" {
		t.Fatalf("expected PARTIAL, got %q", gotType)
	}
}

func TestRefundDeclined(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refundResponse{
			ResponseCode:        "X0",
			ResponseDescription: "Not allowed",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	result, err := client.Refund(context.Background(), ports.RefundRequest{PaymentID: "p", AmountKobo: 5000, Currency: "NGN"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Message, "Not allowed") {
		t.Fatalf("unexpected result: %+v", result)
	}
}