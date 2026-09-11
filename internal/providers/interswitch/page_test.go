package interswitch

import (
	"strings"
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

func TestHostedFieldsSDKURLPerMode(t *testing.T) {
	t.Parallel()
	testClient := New(Options{BaseURL: "http://localhost", Mode: "TEST"})
	if url := testClient.HostedFieldsSDKURL(); !strings.Contains(url, "hostedifelds.qa.interswitchng.com") {
		t.Fatalf("unexpected TEST SDK URL %q", url)
	}
	liveClient := New(Options{BaseURL: "http://localhost", Mode: "LIVE"})
	if url := liveClient.HostedFieldsSDKURL(); !strings.Contains(url, "hostedfields.interswitchng.com") {
		t.Fatalf("unexpected LIVE SDK URL %q", url)
	}
}

func TestHostedFieldsPageDateIsRecent(t *testing.T) {
	t.Parallel()
	client := New(Options{BaseURL: "http://localhost", Mode: "TEST"})
	page := client.NewHostedFieldsPage("ref", "", 500, "https://xego.test/payments/return")
	_, err := time.Parse("2006-01-02 15:04:05", page.DateOfPayment)
	if err != nil {
		t.Fatalf("unexpected date format %q: %v", page.DateOfPayment, err)
	}
}