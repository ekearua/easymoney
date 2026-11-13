package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCreateMerchantWalletUsesPublishedRequestAndParsesVirtualAccount(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/merchant-wallet/api/v1/wallet" {
			t.Errorf("request = %s %s, want POST /merchant-wallet/api/v1/wallet", r.Method, r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization = %q, want Bearer token", got)
		}
		var request merchantWalletCreatePayload
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.MerchantCode != "MX6072" || request.Provider != "PRIME" || request.Status != "ACTIVE" {
			t.Errorf("unexpected wallet request identity fields: %+v", request)
		}
		if request.PIN != "1234" {
			t.Errorf("PIN = %q, want supplied PIN", request.PIN)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":2678,"provider":"PRIME","currencyCode":"566","walletId":"2700007687","merchantCode":"MX6072","status":"ACTIVE","walletCreationStatus":"COMPLETED","virtualAccount":{"id":2368,"walletId":2678,"merchantCode":"MX6072","accountPayableCode":"payable-1","accountName":"Ada Obi","accountNumber":"7620588514","bankName":"Wema Bank","bankCode":"WEMA","currencyCode":"566"}}`))
	})
	client := New(Options{
		ClientID: "client", ClientSecret: "secret", BaseURL: server.URL,
		WalletBaseURL: server.URL + "/merchant-wallet",
	})

	wallet, err := client.CreateMerchantWallet(context.Background(), MerchantWalletCreateRequest{
		Name: "Ada Obi", MerchantCode: "MX6072", Status: "ACTIVE", MobileNumber: "2348012345678",
		Provider: "PRIME", FirstName: "Ada", PIN: "1234", Email: "ada@example.com",
	})
	if err != nil {
		t.Fatalf("create merchant wallet: %v", err)
	}
	if wallet.WalletID != "2700007687" || wallet.WalletCreationStatus != "COMPLETED" {
		t.Fatalf("wallet = %+v", wallet)
	}
	if wallet.VirtualAccount.AccountNumber != "7620588514" || wallet.VirtualAccount.BankCode != "WEMA" {
		t.Fatalf("virtual account = %+v", wallet.VirtualAccount)
	}
}

func TestCreateMerchantWalletRejectsInvalidPINBeforeRequest(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
	})
	client := New(Options{
		ClientID: "client", ClientSecret: "secret", BaseURL: server.URL,
		WalletBaseURL: server.URL + "/merchant-wallet",
	})

	_, err := client.CreateMerchantWallet(context.Background(), MerchantWalletCreateRequest{
		Name: "Ada Obi", MerchantCode: "MX6072", MobileNumber: "2348012345678",
		Provider: "PRIME", FirstName: "Ada", PIN: "12x4", Email: "ada@example.com",
	})
	if err == nil {
		t.Fatal("invalid PIN should be rejected")
	}
}

func TestCreateMerchantWalletDoesNotExposePINInProviderError(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid pin 1234"}`))
	})
	client := New(Options{
		ClientID: "client", ClientSecret: "secret", BaseURL: server.URL,
		WalletBaseURL: server.URL + "/merchant-wallet",
	})

	_, err := client.CreateMerchantWallet(context.Background(), MerchantWalletCreateRequest{
		Name: "Ada Obi", MerchantCode: "MX6072", MobileNumber: "2348012345678",
		Provider: "PRIME", FirstName: "Ada", PIN: "1234", Email: "ada@example.com",
	})
	if err == nil {
		t.Fatal("provider error should be returned")
	}
	if strings.Contains(err.Error(), "1234") {
		t.Fatalf("provider error exposed wallet PIN: %v", err)
	}
}

func TestMerchantWalletBalanceAndTransact(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization = %q, want Bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/merchant-wallet/api/v1/wallet/balance/MX6072":
			if got := r.URL.Query().Get("walletId"); got != "2700007687" {
				t.Errorf("walletId = %q", got)
			}
			_, _ = w.Write([]byte(`{"responseCode":"00","availableBalance":25000,"ledgerBalance":25000,"successful":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/merchant-wallet/api/v1/transaction/transact":
			var request merchantWalletTransactionPayload
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode transaction request: %v", err)
				return
			}
			if request.Amount != "5000" || request.Reference != "debit-1" || request.PIN != "1234" {
				t.Errorf("transaction request = %+v", request)
			}
			_, _ = w.Write([]byte(`{"responseCode":"00","responseMessage":"SUCCESSFUL","reference":"debit-1"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	client := New(Options{
		ClientID: "client", ClientSecret: "secret", BaseURL: server.URL,
		WalletBaseURL: server.URL + "/merchant-wallet",
	})

	balance, err := client.MerchantWalletBalance(context.Background(), "MX6072", "2700007687")
	if err != nil {
		t.Fatalf("get wallet balance: %v", err)
	}
	if !balance.Successful || balance.Available != 25000 {
		t.Fatalf("balance = %+v", balance)
	}
	transaction, err := client.TransactMerchantWallet(context.Background(), MerchantWalletTransactionRequest{
		WalletID: "2700007687", Amount: 5000, PIN: "1234", Reference: "debit-1",
		TransactionCode: "api-charge", Narration: "Merchant purchase",
	})
	if err != nil {
		t.Fatalf("debit wallet: %v", err)
	}
	if transaction.ResponseCode != "00" || transaction.Reference != "debit-1" {
		t.Fatalf("transaction = %+v", transaction)
	}
}

func TestMerchantWalletTransactionByReferenceReconcilesMovement(t *testing.T) {
	server := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/merchant-wallet/api/v1/transaction/" {
			t.Errorf("request = %s %s, want GET /merchant-wallet/api/v1/transaction/", r.Method, r.URL.Path)
			return
		}
		if r.URL.Query().Get("merchantCode") != "MX6072" || r.URL.Query().Get("reference") != "credit-1" {
			t.Errorf("query = %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"merchantCode":"MX6072","merchantWalletId":2678,"transactionDirection":"CREDIT","amount":5000,"responseCode":"00","paymentReference":"credit-1","reference":"credit-1","reversed":false,"currencyCode":"566"}]`))
	})
	client := New(Options{
		ClientID: "client", ClientSecret: "secret", BaseURL: server.URL,
		WalletBaseURL: server.URL + "/merchant-wallet",
	})

	transaction, err := client.CustomerWalletTransaction(context.Background(), "MX6072", "credit-1")
	if err != nil {
		t.Fatalf("get wallet transaction: %v", err)
	}
	if transaction.Reference != "credit-1" || transaction.Direction != "CREDIT" || transaction.AmountKobo != 5000 ||
		transaction.CurrencyCode != "566" || transaction.WalletRecordID != 2678 || transaction.Reversed {
		t.Fatalf("transaction = %+v", transaction)
	}
}
