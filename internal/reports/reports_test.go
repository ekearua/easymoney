package reports

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSTRCSV(t *testing.T) {
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	rows := []STRRow{{
		ReportDate:        at,
		CustomerName:      "Ada Obi",
		CustomerPhone:     "+2348000000001",
		CustomerID:        uuid.New(),
		TransactionID:     uuid.New(),
		ProviderReference: "PYSTK-REF-1",
		AmountKobo:        12_345_678,
		Currency:          "NGN",
		MerchantName:      "MTN Data",
		TransactionDate:   at,
		AlertRule:         "velocity_high",
		AlertSeverity:     "high",
		AlertStatus:       "open",
	}}
	var buf bytes.Buffer
	if err := STRCSV(&buf, rows, at); err != nil {
		t.Fatalf("STRCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Reporter", "STR", "Ada Obi", "+2348000000001", "PYSTK-REF-1",
		"NGN 123,456.78", "MTN Data", "velocity_high",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("STRCSV missing %q in:\n%s", want, out)
		}
	}
}

func TestCTRCSV(t *testing.T) {
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	rows := []CTRRow{{
		ReportDate:        at,
		CustomerName:      "Bola Ahmed",
		CustomerPhone:     "+2348000000002",
		AmountKobo:        15_000_000_00,
		Currency:          "NGN",
		MerchantName:      "Glo Data",
		TransactionDate:   at,
		ProviderReference: "PYSTK-REF-2",
	}}
	var buf bytes.Buffer
	if err := CTRCSV(&buf, rows, at); err != nil {
		t.Fatalf("CTRCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"CTR", "Bola Ahmed", "NGN 15,000,000.00", "Glo Data", "PYSTK-REF-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("CTRCSV missing %q in:\n%s", want, out)
		}
	}
}

func TestPEPCSV(t *testing.T) {
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	rows := []PEPRow{{
		ReportDate:   at,
		CustomerName: "Chidi Nwosu",
		Decision:     "strong",
		MatchedNames: []string{"CHIDI NWOSU", "CHIEF CHIDI"},
		ScreenedAt:   at,
		KYCTier:      "L2",
		RiskBand:     "high",
	}}
	var buf bytes.Buffer
	if err := PEPCSV(&buf, rows, at); err != nil {
		t.Fatalf("PEPCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"PEP", "Chidi Nwosu", "strong", "CHIDI NWOSU; CHIEF CHIDI", "L2", "high"} {
		if !strings.Contains(out, want) {
			t.Errorf("PEPCSV missing %q in:\n%s", want, out)
		}
	}
}

func TestNegativeAmountFormatting(t *testing.T) {
	got := naira(-1_234_567)
	want := "NGN -12,345.67"
	if got != want {
		t.Errorf("naira(-1234567) = %q, want %q", got, want)
	}
}
