package store

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

func TestExtractScanToken(t *testing.T) {
	tests := map[string]string{
		"XGSCAN_abcd":                              "XGSCAN_abcd",
		"https://xego.test/scan/scan_123?x=1":      "scan_123",
		"https://xego.test/scan/XGSCAN_123#manual": "XGSCAN_123",
		"  /scan/scan_token/  ":                    "scan_token",
	}
	for input, want := range tests {
		if got := ExtractScanToken(input); got != want {
			t.Fatalf("ExtractScanToken(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeAcceptedReceiptTypes(t *testing.T) {
	got := normalizeAcceptedReceiptTypes("invoice, data, bad, invoice, thrift")
	if got != "invoice,data,thrift" {
		t.Fatalf("normalizeAcceptedReceiptTypes returned %q", got)
	}
	if got := normalizeAcceptedReceiptTypes("unknown"); got != "merchant_payment" {
		t.Fatalf("empty accepted type fallback returned %q", got)
	}
}

func TestScanValidationStatus(t *testing.T) {
	serviceID := uuid.New()
	reader := ServiceReaderView{ServiceID: serviceID, Active: true}
	token := ReceiptScanTokenView{ServiceID: serviceID, ExpiresAt: time.Now().Add(time.Hour)}
	payment := PaymentView{Payment: domain.Payment{Status: domain.StatusSucceeded}}

	if got := scanValidationStatus(reader, token, payment, true); got != "valid_consumed" {
		t.Fatalf("valid status=%q", got)
	}

	otherReader := reader
	otherReader.ServiceID = uuid.New()
	if got := scanValidationStatus(otherReader, token, payment, true); got != "wrong_service" {
		t.Fatalf("wrong service status=%q", got)
	}

	token.ConsumedAt = ptrTime(time.Now())
	if got := scanValidationStatus(reader, token, payment, true); got != "already_used" {
		t.Fatalf("consumed status=%q", got)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
