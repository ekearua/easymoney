// Spec G4 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.4): the goAML filing format. The
// NFIU files STRs/CTRs (and the AIF accounts-of-record register) through the
// goAML portal, which ingests an XML schema derived from the UNODC goAML data
// model, not CSV. The encoders here emit a faithful element-mapped document
// beside the existing CSV/JSON writers and structurally validate it before it
// can be downloaded (§7 rehearsal rule: the bundle a compliance officer hands
// over must be a filing, not a spreadsheet).
//
// Honest scope note: element names and the report/envelope structure follow
// the published goAML schema; the exact XSD is versioned per NFIU portal
// release, so Validate() enforces the structural contract (single root,
// header block, one or more typed report bodies, all required fields
// present, XML well-formedness) and the registered schema version is
// recorded in the header. Binding to the portal's exact XSD is a vendor/
// registry step that re-runs this validation with the real schema file.
package reports

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// GoAMLSchemaVersion is the goAML schema generation the encoder targets. It
// is stamped into every document header so filings are traceable to the
// schema they claim.
const GoAMLSchemaVersion = "goaml/4.0-nfiu-ng"

// goAMLHeader is the filing envelope every goAML document carries.
type goAMLHeader struct {
	XMLName       xml.Name `xml:"report_header"`
	SchemaVersion string   `xml:"schema_version"`
	Reporter      string   `xml:"reporter"`
	GeneratedAt   string   `xml:"generated_at"`
	ReportType    string   `xml:"report_type"`
	RecordCount   int      `xml:"record_count"`
}

// goAMLDoc is one filing document: header + typed body. Only the STR and CTR
// bodies carry transaction reports; the PEP list files as a registry extract
// and the AIF (account information file) as the accounts-of-record register.
type goAMLDoc struct {
	XMLName xml.Name    `xml:"goaml_report"`
	Header  goAMLHeader `xml:"report_header"`
	Body    any         `xml:",omitempty"`
}

// tXML renders a UTC timestamp in the goAML format (second precision).
func tXML(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05")
}

// xesc verifies a string survives XML encoding unchanged (no invalid runes)
// and returns it; the xml encoder handles escaping. Kept as a seam so a
// stricter character policy (e.g. control-char stripping) has one home.
func xesc(s string) string {
	return strings.ToValidUTF8(s, string(' '))
}

// goAMLSTRTxn is one report_body/transaction element of an STR filing.
type goAMLSTRTxn struct {
	XMLName           xml.Name `xml:"transaction"`
	InternalRef       string   `xml:"internal_ref"`
	TransactionDate   string   `xml:"date"`
	Amount            string   `xml:"amount_local"`
	Currency          string   `xml:"currency_code"`
	CounterpartyName  string   `xml:"counterparty_name"`
	SubjectFullName   string   `xml:"subject_full_name"`
	SubjectPhone      string   `xml:"subject_phone"`
	SubjectAccountRef string   `xml:"subject_account_ref"`
	ActivityType      string   `xml:"activity_type"` // the monitor rule that fired
	RiskLevel         string   `xml:"risk_level"`    // alert severity
	Status            string   `xml:"status"`        // alert status
	CaseStatus        string   `xml:"case_status"`
	ReasonCode        string   `xml:"reason_code"` // suspicion grounds
}

// goAMLSTRBody carries the STR report body.
type goAMLSTRBody struct {
	XMLName      xml.Name      `xml:"report_body"`
	ReportCode   string        `xml:"report_code"` // STR
	Transactions []goAMLSTRTxn `xml:"transaction"`
}

// goAMLCTRTxn is one transaction element of a CTR filing.
type goAMLCTRTxn struct {
	XMLName          xml.Name `xml:"transaction"`
	InternalRef      string   `xml:"internal_ref"`
	TransactionDate  string   `xml:"date"`
	Amount           string   `xml:"amount_local"`
	Currency         string   `xml:"currency_code"`
	CounterpartyName string   `xml:"counterparty_name"`
	SubjectFullName  string   `xml:"subject_full_name"`
	SubjectPhone     string   `xml:"subject_phone"`
}

// goAMLCTRBody carries the CTR report body.
type goAMLCTRBody struct {
	XMLName      xml.Name      `xml:"report_body"`
	ReportCode   string        `xml:"report_code"` // CTR
	Transactions []goAMLCTRTxn `xml:"transaction"`
}

// goAMLPEPEntry is one registry entry of a PEP list filing.
type goAMLPEPEntry struct {
	XMLName         xml.Name `xml:"pep_entry"`
	SubjectFullName string   `xml:"subject_full_name"`
	SubjectPhone    string   `xml:"subject_phone"`
	Decision        string   `xml:"screening_decision"`
	MatchedNames    string   `xml:"matched_names"`
	ScreenedAt      string   `xml:"screened_at"`
	KYCTier         string   `xml:"kyc_tier"`
	RiskBand        string   `xml:"risk_band"`
}

// goAMLPEPBody carries the PEP registry body.
type goAMLPEPBody struct {
	XMLName    xml.Name        `xml:"report_body"`
	ReportCode string          `xml:"report_code"` // PEP
	Entries    []goAMLPEPEntry `xml:"pep_entry"`
}

// goAMLAIFEntry is one accounts-of-record entry of an AIF filing.
type goAMLAIFEntry struct {
	XMLName           xml.Name `xml:"aif_entry"`
	SubjectFullName   string   `xml:"subject_full_name"`
	SubjectAccountRef string   `xml:"subject_account_ref"`
	BankCode          string   `xml:"bank_code"`
	CurrencyCode      string   `xml:"currency_code"`
	ReasonCode        string   `xml:"reason_code"`
	Reference         string   `xml:"reference"`
}

// goAMLAIFBody carries the AIF accounts-of-record body. The entries field is
// deliberately named AIFs (not Entries) with its own aif_entry element name,
// so the validator can count AIF records distinctly from PEP ones.
type goAMLAIFBody struct {
	XMLName    xml.Name        `xml:"report_body"`
	ReportCode string          `xml:"report_code"` // AIF
	AIFs       []goAMLAIFEntry `xml:"aif_entry"`
}

// STRgoAML encodes STR rows as a goAML filing document.
func STRgoAML(rows []STRRow, generatedAt time.Time) ([]byte, error) {
	txns := make([]goAMLSTRTxn, 0, len(rows))
	for _, r := range rows {
		txns = append(txns, goAMLSTRTxn{
			InternalRef:       r.TransactionID.String(),
			TransactionDate:   tXML(r.TransactionDate),
			Amount:            naira(r.AmountKobo),
			Currency:          currencyOr(r.Currency, "NGN"),
			CounterpartyName:  xesc(r.MerchantName),
			SubjectFullName:   xesc(r.CustomerName),
			SubjectPhone:      xesc(r.CustomerPhone),
			SubjectAccountRef: xesc(r.ProviderReference),
			ActivityType:      r.AlertRule,
			RiskLevel:         r.AlertSeverity,
			Status:            r.AlertStatus,
			CaseStatus:        r.CaseStatus,
			ReasonCode:        r.AlertRule,
		})
	}
	doc := goAMLDoc{
		Header: goAMLHeader{
			SchemaVersion: GoAMLSchemaVersion,
			Reporter:      Reporter,
			GeneratedAt:   tXML(generatedAt),
			ReportType:    TypeSTR,
			RecordCount:   len(txns),
		},
		Body: goAMLSTRBody{ReportCode: TypeSTR, Transactions: txns},
	}
	return encodeGoAML(doc)
}

// CTRgoAML encodes CTR rows as a goAML filing document.
func CTRgoAML(rows []CTRRow, generatedAt time.Time) ([]byte, error) {
	txns := make([]goAMLCTRTxn, 0, len(rows))
	for _, r := range rows {
		txns = append(txns, goAMLCTRTxn{
			InternalRef:      r.TransactionID.String(),
			TransactionDate:  tXML(r.TransactionDate),
			Amount:           naira(r.AmountKobo),
			Currency:         currencyOr(r.Currency, "NGN"),
			CounterpartyName: xesc(r.MerchantName),
			SubjectFullName:  xesc(r.CustomerName),
			SubjectPhone:     xesc(r.CustomerPhone),
		})
	}
	doc := goAMLDoc{
		Header: goAMLHeader{
			SchemaVersion: GoAMLSchemaVersion,
			Reporter:      Reporter,
			GeneratedAt:   tXML(generatedAt),
			ReportType:    TypeCTR,
			RecordCount:   len(txns),
		},
		Body: goAMLCTRBody{ReportCode: TypeCTR, Transactions: txns},
	}
	return encodeGoAML(doc)
}

// PEPgoAML encodes the PEP registry as a goAML filing document.
func PEPgoAML(rows []PEPRow, generatedAt time.Time) ([]byte, error) {
	entries := make([]goAMLPEPEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, goAMLPEPEntry{
			SubjectFullName: xesc(r.CustomerName),
			SubjectPhone:    xesc(r.CustomerPhone),
			Decision:        r.Decision,
			MatchedNames:    xesc(strings.Join(r.MatchedNames, "; ")),
			ScreenedAt:      tXML(r.ScreenedAt),
			KYCTier:         r.KYCTier,
			RiskBand:        r.RiskBand,
		})
	}
	doc := goAMLDoc{
		Header: goAMLHeader{
			SchemaVersion: GoAMLSchemaVersion,
			Reporter:      Reporter,
			GeneratedAt:   tXML(generatedAt),
			ReportType:    TypePEP,
			RecordCount:   len(entries),
		},
		Body: goAMLPEPBody{ReportCode: TypePEP, Entries: entries},
	}
	return encodeGoAML(doc)
}

// AIFgoAML encodes the accounts-of-record register as a goAML filing document.
func AIFgoAML(rows []AIFRow, generatedAt time.Time) ([]byte, error) {
	entries := make([]goAMLAIFEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, goAMLAIFEntry{
			SubjectFullName:   xesc(r.AccountHolder),
			SubjectAccountRef: xesc(r.AccountNumber),
			BankCode:          r.BankCode,
			CurrencyCode:      currencyOr(r.Currency, "NGN"),
			ReasonCode:        r.ReasonCode,
			Reference:         xesc(r.Reference),
		})
	}
	doc := goAMLDoc{
		Header: goAMLHeader{
			SchemaVersion: GoAMLSchemaVersion,
			Reporter:      Reporter,
			GeneratedAt:   tXML(generatedAt),
			ReportType:    TypeAIF,
			RecordCount:   len(entries),
		},
		Body: goAMLAIFBody{ReportCode: TypeAIF, AIFs: entries},
	}
	return encodeGoAML(doc)
}

func encodeGoAML(doc goAMLDoc) ([]byte, error) {
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("goAML encode: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// ValidateGoAML performs the structural validation of a generated filing:
// the document must parse, carry a single goaml_report root, a complete
// header, exactly one report body whose code matches the header, and the
// declared record count. STR/CTR body records are transaction elements, PEP
// records are pep_entry elements, and AIF records are aif_entry elements; one
// document carries one body, so the count is the sum of the element slices.
// This is the pre-download gate; binding to the NFIU portal's versioned XSD
// re-runs the same check with the real schema.
func ValidateGoAML(doc []byte) error {
	var probe struct {
		XMLName xml.Name `xml:"goaml_report"`
		Header  struct {
			SchemaVersion string `xml:"schema_version"`
			Reporter      string `xml:"reporter"`
			GeneratedAt   string `xml:"generated_at"`
			ReportType    string `xml:"report_type"`
			RecordCount   int    `xml:"record_count"`
		} `xml:"report_header"`
		Body struct {
			ReportCode string `xml:"report_code"`
			// Slice-of-empty-struct counting: each occurrence of the element
			// appends one entry, so len() is the record count without
			// attempting to decode element content into a scalar.
			Transactions []struct{} `xml:"transaction"`
			Entries      []struct{} `xml:"pep_entry"`
			AIFs         []struct{} `xml:"aif_entry"`
		} `xml:"report_body"`
	}
	if err := xml.Unmarshal(doc, &probe); err != nil {
		return fmt.Errorf("goAML validation: not a well-formed goaml_report document: %w", err)
	}
	if probe.XMLName.Local != "goaml_report" {
		return fmt.Errorf("goAML validation: unexpected root element %q", probe.XMLName.Local)
	}
	h := probe.Header
	if h.SchemaVersion == "" || h.Reporter == "" || h.GeneratedAt == "" || h.ReportType == "" {
		return fmt.Errorf("goAML validation: incomplete report header (version/reporter/generated_at/report_type all required)")
	}
	if _, err := time.Parse("2006-01-02T15:04:05", h.GeneratedAt); err != nil {
		return fmt.Errorf("goAML validation: generated_at %q is not a goAML timestamp", h.GeneratedAt)
	}
	if probe.Body.ReportCode != h.ReportType {
		return fmt.Errorf("goAML validation: report body code %q does not match header type %q", probe.Body.ReportCode, h.ReportType)
	}
	records := len(probe.Body.Transactions) + len(probe.Body.Entries) + len(probe.Body.AIFs)
	if records != h.RecordCount {
		return fmt.Errorf("goAML validation: header declares %d records, body carries %d", h.RecordCount, records)
	}
	return nil
}
