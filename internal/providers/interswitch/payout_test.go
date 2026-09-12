package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

// The MAC in Interswitch's published sample: 100000 kobo, CA→AC, GBP... NGN NG.
// The documented digest for "100000566CA100000566ACNG" must be reproduced
// exactly, or the rail rejects every transfer as an invalid MAC.
func TestSendMoneyMACMatchesDocumentedSample(t *testing.T) {
	const want = "9f4e4f53c57be63e1f08d8f07a7bc1a9461e4a7d5304043daa1ef54bd727b6cde148f4fbfc5e2ad8c4a60f78dfa76304de671fbeb70657b1628f14b6b6baa5e1"
	if got := sendMoneyMAC(100_000); got != want {
		t.Fatalf("MAC = %s\nwant %s", got, want)
	}
}

func TestPayoutSuccess(t *testing.T) {
	t.Parallel()
	var got transferRequest
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transferPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.MAC == "" {
			t.Fatal("mac is required")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{
			ResponseCode:         "90000",
			ResponseCodeGrouping: "SUCCESSFUL",
			TransactionReference: "PBL|LOC|CA|ABP|AC|260918000000|U4AN48D7",
		})
	})

	client := New(Options{
		ClientID:        "c",
		ClientSecret:    "s",
		BaseURL:         srv.URL,
		TransferBaseURL: srv.URL,
		TerminalID:      "TD",
		SenderName:      "Xego Pay",
		SenderPhone:     "08012345678",
		SenderEmail:     "ops@xego.example",
	})
	result, err := client.Payout(context.Background(), ports.PayoutRequest{
		Reference:     "batch_1",
		AccountName:   "John Doe",
		AccountNumber: "3010000002",
		BankCode:      "044",
		AmountKobo:    100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.ExternalRef != "PBL|LOC|CA|ABP|AC|260918000000|U4AN48D7" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got.MAC != sendMoneyMAC(100_000) {
		t.Fatalf("wire MAC %q != %q", got.MAC, sendMoneyMAC(100_000))
	}
	if got.TransferCode != "batch_1" {
		t.Fatalf("transferCode = %q", got.TransferCode)
	}
	if got.Termination.Amount != "100000" || got.Termination.CurrencyCode != NGN ||
		got.Termination.PaymentMethodCode != terminationPaymentMethod || got.Termination.CountryCode != countryCode {
		t.Fatalf("termination mismatch: %+v", got.Termination)
	}
	if got.Termination.EntityCode != "044" {
		t.Fatalf("termination.entityCode = %q", got.Termination.EntityCode)
	}
	if got.Termination.AccountReceivable.AccountNumber != "3010000002" || got.Termination.AccountReceivable.AccountType != "00" {
		t.Fatalf("accountReceivable mismatch: %+v", got.Termination.AccountReceivable)
	}
	if got.Initiation.Amount != "100000" || got.Initiation.CurrencyCode != NGN ||
		got.Initiation.PaymentMethodCode != initiationPaymentMethod || got.Initiation.Channel != channelCode {
		t.Fatalf("initiation mismatch: %+v", got.Initiation)
	}
	if got.InitiatingEntityCode != initiatingEntityCode {
		t.Fatalf("initiatingEntityCode = %q", got.InitiatingEntityCode)
	}
	if got.Beneficiary.Lastname != "Doe" || got.Beneficiary.Othernames != "John" {
		t.Fatalf("beneficiary mismatch: %+v", got.Beneficiary)
	}
	if got.Sender.Lastname != "Pay" || got.Sender.Othernames != "Xego" ||
		got.Sender.Phone != "08012345678" || got.Sender.Email != "ops@xego.example" {
		t.Fatalf("sender mismatch: %+v", got.Sender)
	}
}

func TestPayoutBlankSenderName(t *testing.T) {
	t.Parallel()
	var got transferRequest
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{ResponseCode: "90000", ResponseCodeGrouping: "SUCCESSFUL", TransactionReference: "REF"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	if _, err := client.Payout(context.Background(), ports.PayoutRequest{
		Reference: "r", AccountName: "Jane", AccountNumber: "302", BankCode: "011", AmountKobo: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if got.Sender.Lastname != defaultSenderName {
		t.Fatalf("sender lastname default = %q, want %q", got.Sender.Lastname, defaultSenderName)
	}
}

func TestPayoutDeclined(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{
			ResponseCode:         "903",
			ResponseCodeGrouping: "FAILED",
		})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	result, err := client.Payout(context.Background(), ports.PayoutRequest{
		Reference: "r", AccountName: "Jane", AccountNumber: "302", BankCode: "011", AmountKobo: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Message, "FAILED") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPayoutCircuitBreakerPending(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transferResponse{ResponseCode: "70120"})
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	_, err := client.Payout(context.Background(), ports.PayoutRequest{
		Reference: "r", AccountName: "Jane", AccountNumber: "302", BankCode: "011", AmountKobo: 1000,
	})
	if err != ports.ErrPayoutPending {
		t.Fatalf("expected ErrPayoutPending, got %v", err)
	}
}

func TestPayoutMissingDestinationAccount(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", TransferBaseURL: "http://localhost"})
	_, err := client.Payout(context.Background(), ports.PayoutRequest{Reference: "r", AmountKobo: 1000})
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("expected destination error, got %v", err)
	}
}

func TestNameEnquirySuccess(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nameEnquiryPath || r.Method != http.MethodGet {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("bankCode"); got != "044" {
			t.Fatalf("bankCode header = %q", got)
		}
		if got := r.Header.Get("accountId"); got != "0730804844" {
			t.Fatalf("accountId header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccountName":"VICTOR ADEKUNLE ODEBODE","ResponseCode":"90000","ResponseCodeGrouping":"SUCCESSFUL"}`))
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	accountName, err := client.NameEnquiry(context.Background(), "0730804844", "044")
	if err != nil {
		t.Fatal(err)
	}
	if accountName != "VICTOR ADEKUNLE ODEBODE" {
		t.Fatalf("account name = %q", accountName)
	}
}

func TestNameEnquiryNotFound(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ResponseCode":"59","ResponseCodeGrouping":"ACCOUNT NOT FOUND"}`))
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	_, err := client.NameEnquiry(context.Background(), "0000000000", "044")
	if err == nil || !strings.Contains(err.Error(), "ACCOUNT NOT FOUND") {
		t.Fatalf("expected account-not-found error, got %v", err)
	}
}

func TestQueryTransaction(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != queryPath || r.Method != http.MethodGet {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("requestRef"); got != "batch_9" {
			t.Fatalf("requestRef query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestReference":"batch_9","status":"Completed","transactionRef":"PBL|LOC|CA|ABP|AC|260918000000|U4AN48D7","transactionResponseCode":"90000","amount":"200000","currencyCode":"566"}`))
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	status, err := client.QueryTransaction(context.Background(), "batch_9")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "Completed" || status.TransactionRef == "" ||
		status.ResponseCode != "90000" || status.AmountKobo != 200_000 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestGetBankCodes(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != bankCodesPath || r.Method != http.MethodGet {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"banks":[{"id":"31","cbnCode":"044","bankName":"Access Bank Nigeria Plc","bankCode":"ABP"},{"id":"10","cbnCode":"058","bankName":"Guaranty Trust Bank Plc","bankCode":"GTB"}]}`))
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})
	banks, err := client.GetBankCodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(banks) != 2 || banks[0].CBNCode != "044" || banks[1].BankCode != "GTB" {
		t.Fatalf("unexpected banks: %+v", banks)
	}
}