package interswitch

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestInitializeReturnsPlatformFormPageURL(t *testing.T) {
	t.Parallel()
	client := New(Options{ClientID: "cid", ClientSecret: "secret", MerchantCode: "M1000", PayItemID: "pi", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"})
	checkout, err := client.Initialize(context.Background(), ports.InitializePayment{
		Reference:   "wpd_ref",
		Email:       "demo@example.com",
		AmountKobo:  50_000,
		Currency:    "NGN",
		CallbackURL: "https://xego.test/payments/return",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://xego.test/checkout/interswitch/wpd_ref"
	if checkout.Reference != "wpd_ref" || checkout.URL != want {
		t.Fatalf("unexpected checkout: ref=%q url=%q want=%q", checkout.Reference, checkout.URL, want)
	}
}

func TestInitializeRequiresMerchantConfig(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"})
	if _, err := client.Initialize(context.Background(), ports.InitializePayment{Reference: "r", CallbackURL: "https://xego.test/payments/return"}); err == nil {
		t.Fatal("expected error when merchant config missing")
	}
}

func TestNewPayPageBuildsRedirectForm(t *testing.T) {
	t.Parallel()
	client := New(Options{MerchantCode: "M1000", PayItemID: "pi", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"})
	page := client.NewPayPage("wpd_ref", "demo@example.com", 50_000, "https://xego.test/payments/return")
	if page.Action != "https://webpay.sandbox.interswitchng.com/collections/w/pay" {
		t.Fatalf("unexpected action %q", page.Action)
	}
	if page.MerchantCode != "M1000" || page.PayItemID != "pi" || page.TxnRef != "wpd_ref" || page.Amount != 50_000 {
		t.Fatalf("unexpected page: %#v", page)
	}
	if page.Currency != NGN || page.Mode != "TEST" {
		t.Fatalf("unexpected currency/mode: %#v", page)
	}
}

func TestVerifySendsInterswitchAuthAndNormalizesRequery(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/api/v1/gettransaction.json" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("merchantcode") != "M1000" || r.Form.Get("transactionreference") != "wpd_ref" {
			t.Fatalf("unexpected query: %v", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("Timestamp") == "" || r.Header.Get("Nonce") == "" || r.Header.Get("Signature") == "" {
			t.Fatalf("missing InterswitchAuth headers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Amount":50000,"ResponseCode":"00","ResponseDescription":"Approved","MerchantReference":"wpd_ref"}`))
	}))
	defer server.Close()

	verification, err := New(Options{ClientID: "cid", ClientSecret: "secret", MerchantCode: "M1000", BaseURL: server.URL, Mode: "TEST"}).Verify(context.Background(), "wpd_ref")
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != "success" || verification.AmountKobo != 50_000 || verification.Channel != "card" || verification.Domain != "test" {
		t.Fatalf("unexpected verification: %#v", verification)
	}
}

func TestVerifyRejectsNon2xx(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ResponseCode":"91","ResponseDescription":"Invalid credentials"}`))
	}))
	defer server.Close()
	if _, err := New(Options{ClientID: "cid", ClientSecret: "secret", MerchantCode: "M1000", BaseURL: server.URL, Mode: "TEST"}).Verify(context.Background(), "wpd_ref"); err == nil {
		t.Fatal("expected error on non-2xx requery")
	}
}

func TestVerifyRejectsEmptyResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	if _, err := New(Options{ClientID: "cid", ClientSecret: "secret", MerchantCode: "M1000", BaseURL: server.URL, Mode: "TEST"}).Verify(context.Background(), "wpd_ref"); err == nil {
		t.Fatal("expected error on empty requery response")
	}
}

func TestValidateWebhookParsesNotification(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"TRANSACTION.COMPLETED","uuid":"ref-123","timestamp":1594646111460,"data":{"merchantReference":"wpd_ref","responseCode":"00","responseDescription":"Approved by Financial Institution","amount":50000,"currencyCode":"566"}}`)
	mac := webhookSignature("whsec", body)
	event, err := New(Options{ClientID: "cid", WebhookSecret: "whsec", MerchantCode: "M1000", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"}).ValidateWebhook(body, mac)
	if err != nil {
		t.Fatal(err)
	}
	if event.Reference != "wpd_ref" || event.Event != "TRANSACTION.COMPLETED" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestValidateWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"TRANSACTION.COMPLETED","uuid":"ref-123","timestamp":1,"data":{"merchantReference":"wpd_ref","responseCode":"00"}}`)
	if _, err := New(Options{ClientID: "cid", WebhookSecret: "whsec", MerchantCode: "M1000", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"}).ValidateWebhook(body, "deadbeef"); err == nil {
		t.Fatal("invalid signature should be rejected")
	}
}

func TestValidateWebhookRejectsMissingSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"TRANSACTION.COMPLETED","uuid":"ref-123","timestamp":1,"data":{"merchantReference":"wpd_ref","responseCode":"00"}}`)
	if _, err := New(Options{ClientID: "cid", WebhookSecret: "whsec", MerchantCode: "M1000", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"}).ValidateWebhook(body, ""); err == nil {
		t.Fatal("unsigned webhook should be rejected")
	}
}

func TestValidateWebhookRejectsMissingSecret(t *testing.T) {
	t.Parallel()
	client := New(Options{ClientID: "cid", ClientSecret: "secret", MerchantCode: "M1000", BaseURL: "https://webpay.sandbox.interswitchng.com", Mode: "TEST"})
	body := []byte(`{"event":"TRANSACTION.COMPLETED","uuid":"ref-123","timestamp":1,"data":{"merchantReference":"wpd_ref","responseCode":"00"}}`)
	if _, err := client.ValidateWebhook(body, "whatever"); err == nil {
		t.Fatal("missing webhook secret should be rejected")
	}
}

func TestInterswitchStatusNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"00", "success"},
		{"01", "failed"},
		{"05", "failed"},
		{"91", "failed"},
		{"06", "pending"},
		{"unknown", "pending"},
		{"", "pending"},
	}
	for _, tc := range tests {
		if got := interswitchStatus(tc.input); got != tc.want {
			t.Errorf("interswitchStatus(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestOriginFromCallback(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://xego.test/payments/return", "https://xego.test"},
		{"https://xego.test/payments/return/", "https://xego.test"},
		{"https://xego.test", "https://xego.test"},
	}
	for _, tc := range tests {
		if got := originFromCallback(tc.in); got != tc.want {
			t.Errorf("originFromCallback(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSignatureIsDeterministic(t *testing.T) {
	t.Parallel()
	endpoint := "/collections/api/v1/gettransaction.json?merchantcode=M1000&transactionreference=wpd_ref"
	clientID, secret, method := "cid", "secret", "GET"
	s1 := interswitchSignature(clientID, secret, method, endpoint, 12345, "nonce")
	s2 := interswitchSignature(clientID, secret, method, endpoint, 12345, "nonce")
	if s1 != s2 {
		t.Fatalf("signature not deterministic: %q vs %q", s1, s2)
	}
	if _, err := base64.StdEncoding.DecodeString(s1); err != nil {
		t.Fatalf("signature not base64: %v", err)
	}
}

func webhookSignature(secret string, body []byte) string {
	h := hmac.New(sha512.New, []byte(secret))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
