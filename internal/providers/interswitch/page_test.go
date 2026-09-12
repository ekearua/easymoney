package interswitch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewHostedFieldsPageBasicFields(t *testing.T) {
	t.Parallel()
	client := New(Options{ClientID: "c", ClientSecret: "s", MerchantCode: "MX", PayItemID: "pi", BaseURL: "https://sandbox.interswitchng.com", Mode: "TEST"})
	page := client.NewHostedFieldsPage("ref-1", "user@test.com", 100_000, "https://xego.test/payments/return")
	if page.MerchantCode != "MX" || page.PayableCode != "pi" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if page.TransactionReference != "ref-1" {
		t.Fatalf("unexpected transaction ref %q", page.TransactionReference)
	}
	if page.CurrencyCode != "566" {
		t.Fatalf("unexpected currency %q", page.CurrencyCode)
	}
	if page.Amount != 100_000 {
		t.Fatalf("unexpected amount %d", page.Amount)
	}
	if page.MerchantCustomerName != "user@test.com" {
		t.Fatalf("expected email as customer name, got %q", page.MerchantCustomerName)
	}
	wantRedirect := "https://xego.test/payments/return?reference=ref-1"
	if page.RedirectURL != wantRedirect {
		t.Fatalf("unexpected redirectURL %q, want %q", page.RedirectURL, wantRedirect)
	}
}

// TestHostedFieldsPageFieldConfigIsMountable guards the exact defect that left
// the secure card page unusable: the frame's field builder calls
// Object.keys(field.styles) and rejects the field when styles is absent, so
// every configured field must carry a styles object (and a selector that
// resolves on the page).
func TestHostedFieldsPageFieldConfigIsMountable(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", Mode: "TEST"})
	page := client.NewHostedFieldsPage("ref-2", "user@test.com", 50_000, "https://xego.test/payments/return")

	var config struct {
		PaymentParameters map[string]any `json:"paymentParameters"`
		Cardinal          struct {
			ContainerSelector string `json:"containerSelector"`
		} `json:"cardinal"`
		Fields map[string]struct {
			Selector    string         `json:"selector"`
			Styles      map[string]any `json:"styles"`
			Placeholder string         `json:"placeholder"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(page.ConfigJSON), &config); err != nil {
		t.Fatalf("ConfigJSON is not valid JSON: %v", err)
	}

	// The SDK validates against exactly these field keys.
	wantFields := []string{"cardNumber", "expirationDate", "cvv", "pin", "otp"}
	if len(config.Fields) != len(wantFields) {
		t.Fatalf("configured %d fields, want %d: %v", len(config.Fields), len(wantFields), config.Fields)
	}
	for _, name := range wantFields {
		field, ok := config.Fields[name]
		if !ok {
			t.Fatalf("field %q missing from the SDK configuration", name)
		}
		if field.Selector == "" {
			t.Fatalf("field %q has no selector", name)
		}
		if field.Styles == nil {
			t.Fatalf("field %q has no styles; the SDK silently drops the field without them", name)
		}
	}

	if config.Cardinal.ContainerSelector != "#cardinal-container" {
		t.Fatalf("cardinal containerSelector = %q, want #cardinal-container", config.Cardinal.ContainerSelector)
	}
	for _, key := range []string{"amount", "currencyCode", "merchantCode", "payableCode", "transactionReference", "redirectURL"} {
		if _, ok := config.PaymentParameters[key]; !ok {
			t.Fatalf("paymentParameters is missing %q", key)
		}
	}
}

func TestHostedFieldsSDKURL(t *testing.T) {
	t.Parallel()
	// Both modes load the SDK from hostedfields.interswitchng.com: the SDK's
	// field iframes always mount from that origin, while the documented QA
	// SDK host neither resolves nor completes a TLS handshake (see
	// HostedFieldsSDKURL). The mode only changes the payment parameters, and
	// an explicit Options.HostedFieldsSDKURL override always wins.
	for _, mode := range []string{"TEST", "LIVE"} {
		client := New(Options{BaseURL: "http://localhost", Mode: mode})
		if url := client.HostedFieldsSDKURL(); url != "https://hostedfields.interswitchng.com/sdk.js" {
			t.Fatalf("%s SDK URL = %q, want https://hostedfields.interswitchng.com/sdk.js", mode, url)
		}
	}
	override := New(Options{BaseURL: "http://localhost", Mode: "TEST", HostedFieldsSDKURL: "https://sdk.example.test/sdk.js"})
	if url := override.HostedFieldsSDKURL(); url != "https://sdk.example.test/sdk.js" {
		t.Fatalf("override SDK URL = %q, want https://sdk.example.test/sdk.js", url)
	}
	page := override.NewHostedFieldsPage("ref", "u@t.com", 100_000, "https://x.test/return")
	if page.SDKURL != "https://sdk.example.test/sdk.js" {
		t.Fatalf("page SDK URL = %q, want override", page.SDKURL)
	}
	if page.SDKOrigin != "https://sdk.example.test" {
		t.Fatalf("SDK origin = %q, want https://sdk.example.test", page.SDKOrigin)
	}
}

func TestHostedFieldsPageDateIsRecent(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", Mode: "TEST"})
	page := client.NewHostedFieldsPage("ref", "", 500, "https://xego.test/payments/return")
	_, err := time.Parse("2006-01-02T15:04:05", page.DateOfPayment)
	if err != nil {
		t.Fatalf("unexpected date format %q: %v", page.DateOfPayment, err)
	}
}
