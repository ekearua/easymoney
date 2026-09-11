package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func TestTransferCreatesVirtualAccount(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != virtualAccountPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(virtualAccountResponse{
			AccountNumber:        "4460741408",
			AccountName:          "Xego transaction",
			BankName:             "WEMA",
			Amount:               50_000,
			TransactionReference: "tx-ref-123",
			ResponseCode:         "Z0",
			ValidityPeriodMins:   30,
		})
	})

	client := New(Options{
		ClientID:        "cid",
		ClientSecret:    "secret",
		MerchantCode:    "M1000",
		PayItemID:       "pi",
		BaseURL:         srv.URL,
		TerminalID:      "TD",
		SourceAccount:   "3012345678",
	})
	instruction, err := client.Transfer(context.Background(), InitializePaymentFixture())
	if err != nil {
		t.Fatal(err)
	}
	if instruction.AccountNumber != "4460741408" {
		t.Fatalf("unexpected account number %q", instruction.AccountNumber)
	}
	if instruction.BankName != "WEMA" || instruction.Reference != "tx-ref-123" {
		t.Fatalf("unexpected instruction: %+v", instruction)
	}
	if instruction.ValidityMins != 30 {
		t.Fatalf("expected 30m validity")
	}
	if time.Until(instruction.ExpiresAt) < 29*time.Minute {
		t.Fatalf("expected expiry near 30m from now")
	}
}

func TestTransferRejectsEmptyConfig(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", Mode: "TEST"})
	if _, err := client.Transfer(context.Background(), InitializePaymentFixture()); err == nil {
		t.Fatal("expected error for empty merchant config")
	}
}

func TestTransferServerErrorCode(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(virtualAccountResponse{ResponseCode: "X0"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", MerchantCode: "M", PayItemID: "pi", BaseURL: srv.URL, TerminalID: "TD"})
	_, err := client.Transfer(context.Background(), InitializePaymentFixture())
	if err == nil {
		t.Fatal("expected error for empty account number")
	}
}

func InitializePaymentFixture() ports.InitializePayment {
	return ports.InitializePayment{
		Reference:   "wpd_ref",
		Email:       "demo@example.com",
		AmountKobo:  50_000,
		Currency:    "NGN",
		CallbackURL: "https://xego.test/payments/return",
		Metadata:    map[string]string{"merchant_slug": "Shop"},
	}
}