package reports

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// G4 (AML spec §5): the goAML filing encoders beside the CSV/JSON writers.
// Each document must satisfy the structural validator: single root, complete
// header, one body matching the header type, declared record count, and
// round-trip through encoding/xml.

func goAMLFixtureTime() time.Time {
	return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
}

func TestSTRGoAMLEncodesAndValidates(t *testing.T) { //nolint:parallel // table-style unit test
	at := goAMLFixtureTime()
	rows := []STRRow{{
		ReportDate:        at,
		CustomerName:      "Ada <Obi> & Sons", // escaping must round-trip
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
	body, err := STRgoAML(rows, at)
	if err != nil {
		t.Fatalf("STRgoAML: %v", err)
	}
	if err := ValidateGoAML(body); err != nil {
		t.Fatalf("ValidateGoAML: %v\n%s", err, body)
	}
	out := string(body)
	for _, want := range []string{
		"<goaml_report>", "<schema_version>", GoAMLSchemaVersion,
		Reporter, "Ada &lt;Obi&gt; &amp; Sons", "NGN 123,456.78",
		"velocity_high", "<case_status>", "2026-08-01T10:00:00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("STR goAML missing %q in:\n%s", want, out)
		}
	}
	// Zero rows still validate: an empty filing is a legitimate document.
	empty, err := STRgoAML(nil, at)
	if err != nil {
		t.Fatalf("STRgoAML empty: %v", err)
	}
	if err := ValidateGoAML(empty); err != nil {
		t.Fatalf("empty STR filing must validate: %v", err)
	}
}

func TestCTRGoAMLEncodesAndValidates(t *testing.T) { //nolint:parallel
	at := goAMLFixtureTime()
	rows := []CTRRow{{
		ReportDate:        at,
		CustomerName:      "Bola Ahmed",
		CustomerPhone:     "+2348000000002",
		CustomerID:        uuid.New(),
		TransactionID:     uuid.New(),
		ProviderReference: "PYSTK-REF-2",
		AmountKobo:        15_000_000_00,
		Currency:          "NGN",
		MerchantName:      "Glo Data",
		TransactionDate:   at,
	}}
	body, err := CTRgoAML(rows, at)
	if err != nil {
		t.Fatalf("CTRgoAML: %v", err)
	}
	if err := ValidateGoAML(body); err != nil {
		t.Fatalf("ValidateGoAML: %v\n%s", err, body)
	}
	if !strings.Contains(string(body), "NGN 15,000,000.00") {
		t.Errorf("CTR goAML amount missing:\n%s", body)
	}
}

func TestPEPGoAMLEncodesAndValidates(t *testing.T) { //nolint:parallel
	at := goAMLFixtureTime()
	rows := []PEPRow{{
		ReportDate:    at,
		CustomerName:  "Chief D. Money",
		CustomerPhone: "+2348000000003",
		CustomerID:    uuid.New(),
		Decision:      "strong",
		MatchedNames:  []string{"Danjuma Money", "D. Money"},
		ScreenedAt:    at,
		KYCTier:       "L2",
		RiskBand:      "high",
	}}
	body, err := PEPgoAML(rows, at)
	if err != nil {
		t.Fatalf("PEPgoAML: %v", err)
	}
	if err := ValidateGoAML(body); err != nil {
		t.Fatalf("ValidateGoAML: %v\n%s", err, body)
	}
	if !strings.Contains(string(body), "Danjuma Money; D. Money") {
		t.Errorf("PEP goAML matched names missing:\n%s", body)
	}
}

func TestValidateGoAMLRejectsBrokenDocuments(t *testing.T) { //nolint:parallel
	at := goAMLFixtureTime()
	good, err := STRgoAML([]STRRow{{
		ReportDate: at, CustomerID: uuid.New(), TransactionID: uuid.New(),
		TransactionDate: at, AmountKobo: 100,
	}}, at)
	if err != nil {
		t.Fatal(err)
	}

	// Count mismatch: header declares more records than the body carries.
	tampered := bytes.Replace(good, []byte("<record_count>1</record_count>"), []byte("<record_count>5</record_count>"), 1)
	if err := ValidateGoAML(tampered); err == nil {
		t.Error("record-count mismatch must fail validation")
	}

	// Not XML at all.
	if err := ValidateGoAML([]byte("this is not xml")); err == nil {
		t.Error("non-XML document must fail validation")
	}

	// Wrong root element.
	if err := ValidateGoAML([]byte(xml.Header + "<other_root/>")); err == nil {
		t.Error("wrong root element must fail validation")
	}

	// Missing header fields.
	if err := ValidateGoAML([]byte(xml.Header + `<goaml_report><report_header></report_header></goaml_report>`)); err == nil {
		t.Error("incomplete header must fail validation")
	}

	// Valid document must pass (control).
	if err := ValidateGoAML(good); err != nil {
		t.Errorf("control document must validate: %v", err)
	}
}
