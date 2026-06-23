package paystack

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
		if r.URL.Path != "/transaction/initialize" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_example" {
			t.Fatalf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.example/test","reference":"wpd_ref"}}`)
	}))
	defer server.Close()

	client := New("sk_test_example", server.URL)
	checkout, err := client.Initialize(context.Background(), ports.InitializePayment{
		Reference: "wpd_ref", Email: "demo@example.com", AmountKobo: 50_000,
		Currency: "NGN", CallbackURL: "https://example.test/payments/return",
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Reference != "wpd_ref" || checkout.URL == "" {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
}

func TestVerifyNormalizesPaystackResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":true,"message":"Verification successful","data":{"reference":"wpd_ref","status":"success","amount":50000,"currency":"NGN","domain":"test","channel":"card","metadata":{"payment_id":"payment-1","merchant_id":"merchant-1"},"gateway_response":"Successful"}}`)
	}))
	defer server.Close()

	verification, err := New("sk_test_example", server.URL).Verify(context.Background(), "wpd_ref")
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != "success" || verification.AmountKobo != 50_000 || verification.Metadata["merchant_id"] != "merchant-1" {
		t.Fatalf("unexpected verification: %#v", verification)
	}
}

func TestValidateWebhook(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"charge.success","data":{"reference":"wpd_ref"}}`)
	mac := hmac.New(sha512.New, []byte("sk_test_example"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	event, err := New("sk_test_example", "https://example.test").ValidateWebhook(body, signature)
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "charge.success" || event.Reference != "wpd_ref" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if _, err := New("sk_test_example", "").ValidateWebhook(body, "bad"); err == nil {
		t.Fatal("invalid signature should be rejected")
	}
}
