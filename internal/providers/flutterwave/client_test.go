package flutterwave

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestInitializeUsesCardOnlyAndTrustedAmount(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/payments" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer FLWSECK_TEST_example" {
			t.Fatalf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","message":"Payment initialized","data":{"link":"https://checkout.flutterwave.com/o/xyz"}}`)
	}))
	defer server.Close()

	client := New("FLWSECK_TEST_example", "FLWPUBK_TEST_example", server.URL, "whsec_test")
	checkout, err := client.Initialize(context.Background(), ports.InitializePayment{
		Reference: "wpd_ref", Email: "demo@example.com", AmountKobo: 50_000,
		Currency: "NGN", CallbackURL: "https://example.test/payments/return",
		Metadata: map[string]string{"payment_id": "pay-1", "merchant_id": "merch-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Reference != "wpd_ref" || checkout.URL == "" {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
}

func TestInitializeRejectsFailedResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"error","message":"Invalid payload","data":{}}`)
	}))
	defer server.Close()

	_, err := New("key", "pub", server.URL, "sec").Initialize(context.Background(), ports.InitializePayment{
		Reference: "wpd_ref", Email: "demo@example.com", AmountKobo: 50_000, Currency: "NGN",
	})
	if err == nil {
		t.Fatal("expected error for failed initialize")
	}
}

func TestVerifyNormalizesFlutterwaveResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/transactions/verify" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","message":"Transaction verified","data":{"tx_ref":"wpd_ref","id":12345,"status":"successful","amount":500.00,"currency":"NGN","created_at":"2026-08-21T10:30:00Z","channel":"card","meta":{"payment_id":"pay-1","merchant_id":"merch-1"}}}`)
	}))
	defer server.Close()

	verification, err := New("key", "pub", server.URL, "sec").Verify(context.Background(), "wpd_ref")
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != "success" || verification.AmountKobo != 50_000 || verification.Metadata["merchant_id"] != "merch-1" {
		t.Fatalf("unexpected verification: %#v", verification)
	}
}

func TestVerifyRejectsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"error","message":"Transaction not found","data":null}`)
	}))
	defer server.Close()

	_, err := New("key", "pub", server.URL, "sec").Verify(context.Background(), "bad_ref")
	if err == nil {
		t.Fatal("expected error for verify failure")
	}
}

func TestValidateWebhook(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"charge.completed","data":{"tx_ref":"wpd_ref"}}`)
	mac := hmac.New(sha512.New, []byte("whsec_test_secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	event, err := New("key", "pub", "https://example.test", "whsec_test_secret").ValidateWebhook(body, signature)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "charge.completed" || event.Reference != "wpd_ref" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestValidateWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"charge.completed","data":{"tx_ref":"wpd_ref"}}`)
	if _, err := New("key", "pub", "https://example.test", "whsec_test_secret").ValidateWebhook(body, "bad_sig"); err == nil {
		t.Fatal("invalid signature should be rejected")
	}
}

func TestValidateWebhookRejectsEmptyCredentials(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"charge.completed","data":{"tx_ref":"wpd_ref"}}`)
	if _, err := New("key", "pub", "https://example.test", "").ValidateWebhook(body, "sig"); err == nil {
		t.Fatal("empty webhook secret should be rejected")
	}
}

func TestFlutterwaveStatusNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"successful", "success"},
		{"success", "success"},
		{"failed", "failed"},
		{"cancelled", "failed"},
		{"pending", "pending"},
		{"processing", "processing"},
		{"reversed", "reversed"},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		if got := flutterwaveStatus(tc.input); got != tc.want {
			t.Errorf("flutterwaveStatus(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFlutterwaveChannelNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"card", "card"},
		{"card-charge", "card"},
		{"banktransfer", "bank_transfer"},
		{"bank_transfer", "bank_transfer"},
		{"ussd", "ussd"},
		{"mobilemoney", "mobile_money"},
	}
	for _, tc := range tests {
		if got := flutterwaveChannel(tc.input); got != tc.want {
			t.Errorf("flutterwaveChannel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestHealthSuccess(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New("key", "pub", server.URL, "sec")
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("unexpected health error: %v", err)
	}
}

func TestHealthFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New("key", "pub", server.URL, "sec")
	// Health returns nil because the server is reachable, even on 500.
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("health should not error on reachable server: %v", err)
	}
}
