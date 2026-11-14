package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestFulfilDataSuccess(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != vtuTransactionsPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("TerminalId"); got != "TERM1" {
			t.Fatalf("TerminalId header = %q, want TERM1", got)
		}
		var req vtuRechargeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.PaymentCode != "10903" {
			t.Fatalf("paymentCode = %q, want 10903", req.PaymentCode)
		}
		if req.CustomerID != "2348031234567" || req.CustomerMobile != "2348031234567" {
			t.Fatalf("unexpected customer fields: %+v", req)
		}
		if req.Amount != "100000" {
			t.Fatalf("amount = %q, want 100000", req.Amount)
		}
		if req.RequestReference == "" || len(req.RequestReference) > 20 {
			t.Fatalf("invalid requestReference %q", req.RequestReference)
		}
		if req.TerminalID != "TERM1" {
			t.Fatalf("TerminalId body = %q, want TERM1", req.TerminalID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuRechargeResponse{
			TransactionRef:       "PBL|Web|3PBL0001|MTNVTU1|010223180453|4WNYQJ3NDB",
			ResponseCode:         "90000",
			ResponseDescription:  "Success",
			ResponseCodeGrouping: "SUCCESSFUL",
		})
	})

	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
	result, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "REQ-001",
		NetworkCode:      "MTN",
		ProviderSKU:      "10903",
		BeneficiaryPhone: "+2348031234567",
		AmountKobo:       100000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" {
		t.Fatalf("status = %q, want fulfilled", result.Status)
	}
	if result.ProviderReference == "" || len(result.ProviderReference) > 20 {
		t.Fatalf("unexpected provider reference %q", result.ProviderReference)
	}
}

func TestFulfilDataPendingOnProcessing(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuRechargeResponse{ResponseCode: "90000", ResponseDescription: "Billing On Progress", ResponseCodeGrouping: "SUCCESSFUL"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
	result, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{RequestCode: "REQ-002", ProviderSKU: "10903", BeneficiaryPhone: "08031234567", AmountKobo: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" {
		t.Fatalf("status = %q, want pending", result.Status)
	}
}

func TestFulfilDataRejected(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuRechargeResponse{ResponseCode: "90001", ResponseDescription: "Invalid customer/phone number", ResponseCodeGrouping: "FAILED"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
	result, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{RequestCode: "REQ-003", ProviderSKU: "10903", BeneficiaryPhone: "08031234567", AmountKobo: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed (result=%+v)", result.Status, result)
	}
}

func TestCheckDataStatusCompleted(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != vtuTransactionsPath {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("requestRef"); got != "145300000001" {
			t.Fatalf("requestRef = %q, want 145300000001", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vtuQueryResponse{
			RequestReference:        "145300000001",
			Status:                  "Completed",
			TransactionResponseCode: "90000",
			TransactionRef:          "PBL|Web|3PBL0001|MTNVTU1|010223180453|4WNYQJ3NDB",
			Amount:                  "100000",
		})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
	result, err := client.CheckDataStatus(context.Background(), "145300000001")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" {
		t.Fatalf("status = %q, want fulfilled", result.Status)
	}
	if result.ProviderReference != "145300000001" {
		t.Fatalf("provider reference = %q, want 145300000001", result.ProviderReference)
	}
}

func TestCheckDataStatusFailedReversed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"failed", "Failed"},
		{"reversed", "Reversed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(vtuQueryResponse{RequestReference: "REF1", Status: tc.status, TransactionResponseCode: "90006"})
			})
			client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
			result, err := client.CheckDataStatus(context.Background(), "REF1")
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "failed" {
				t.Fatalf("status = %q, want failed", result.Status)
			}
		})
	}
}

func TestFulfilDataMissingPaymentCode(t *testing.T) {
	t.Parallel()
	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: "http://localhost", TerminalID: "TERM1"})
	_, err := client.FulfilData(context.Background(), ports.DataFulfilmentRequest{BeneficiaryPhone: "08031234567", AmountKobo: 1000, RequestCode: "REQ"})
	if err == nil || !strings.Contains(err.Error(), "paymentCode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetVTUPaymentItems(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/services/options" || r.URL.Query().Get("serviceid") != "687" {
			t.Fatalf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"PaymentItems": []map[string]any{
				{"Id": "01", "Name": "Airtel 100MB Daily", "Amount": "10000", "IsAmountFixed": true, "PaymentCode": "10903"},
			},
			"ResponseCode": "90000", "ResponseCodeGrouping": "SUCCESSFUL",
		})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", TransferBaseURL: srv.URL, TerminalID: "TERM1"})
	items, err := client.GetVTUPaymentItems(context.Background(), 687)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PaymentCode != "10903" || !items[0].IsAmountFixed {
		t.Fatalf("unexpected items: %+v", items)
	}
}
