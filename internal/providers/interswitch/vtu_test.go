package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestFulfilDataSuccess(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != vtuPurchasePath || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var req vtuPurchaseRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ItemCode != "MTN-1GB" || req.PhoneNumber != "08031234567" {
			t.Fatalf("unexpected payload: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuResponse{
			RequestRef:          "ISW-VTU-001",
			ResponseCode:        "90000",
			ResponseDescription: "Billing On Progress",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	result, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:       "REQ-001",
		ProviderReference: "REF-001",
		NetworkCode:       "MTN",
		ProviderSKU:       "MTN-1GB",
		BeneficiaryPhone:  "+2348031234567",
		AmountKobo:        5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.ProviderReference != "ISW-VTU-001" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestFulfilDataRejected(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuResponse{ResponseCode: "59", ResponseDescription: "Invalid phone"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	_, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{ProviderSKU: "X", BeneficiaryPhone: "080", AmountKobo: 1000})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckDataStatusDelivered(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuResponse{TxnStatus: "Successful", ResponseDescription: "Delivered"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TerminalID: "TD", SourceAccount: "301"})
	result, err := client.CheckDataStatus(context.Background(), "REF1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" {
		t.Fatalf("expected fulfilled, got %q", result.Status)
	}
}

func TestFulfilDataMissingSKU(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost"})
	_, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{BeneficiaryPhone: "080"})
	if err == nil || err.Error() != "interswitch VTU: plan item code is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}