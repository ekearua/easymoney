// Package reports generates regulator-format ML/FT reports (C15): suspicious
// transaction reports (STR), currency transaction reports (CTR), and
// politically exposed persons (PEP) lists, extracted from audited payment and
// screening data. Rows carry the fields a Nigerian regulator (NFIU/CBN) needs:
// customer identity, transaction reference, amount, date, counterparty, and
// the reason the activity was flagged.
//
// The package is deliberately pure: it defines row types and CSV/JSON
// encoders. Data comes from the store, which maps audited rows into these
// types.
package reports

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ReportType identifies the regulator report.
const (
	TypeSTR = "STR"
	TypeCTR = "CTR"
	TypePEP = "PEP"
)

// Reporter identifies the filing institution.
const Reporter = "Xego Payments"

// STRRow is one suspicious transaction report entry (MLPA 2004 / NFIU).
type STRRow struct {
	ReportDate        time.Time `json:"report_date"`
	CustomerName      string    `json:"customer_name"`
	CustomerPhone     string    `json:"customer_phone"`
	CustomerID        uuid.UUID `json:"customer_id"`
	TransactionID     uuid.UUID `json:"transaction_id"`
	ProviderReference string    `json:"provider_reference"`
	AmountKobo        int64     `json:"amount_kobo"`
	Currency          string    `json:"currency"`
	MerchantName      string    `json:"merchant_name"`
	TransactionDate   time.Time `json:"transaction_date"`
	AlertRule         string    `json:"alert_rule"`
	AlertSeverity     string    `json:"alert_severity"`
	AlertStatus       string    `json:"alert_status"`
	CaseStatus        string    `json:"case_status"`
}

// CTRRow is one currency transaction report entry (large-value transaction).
type CTRRow struct {
	ReportDate        time.Time `json:"report_date"`
	CustomerName      string    `json:"customer_name"`
	CustomerPhone     string    `json:"customer_phone"`
	CustomerID        uuid.UUID `json:"customer_id"`
	TransactionID     uuid.UUID `json:"transaction_id"`
	ProviderReference string    `json:"provider_reference"`
	AmountKobo        int64     `json:"amount_kobo"`
	Currency          string    `json:"currency"`
	MerchantName      string    `json:"merchant_name"`
	TransactionDate   time.Time `json:"transaction_date"`
}

// PEPRow is one politically exposed person list entry.
type PEPRow struct {
	ReportDate    time.Time `json:"report_date"`
	CustomerName  string    `json:"customer_name"`
	CustomerPhone string    `json:"customer_phone"`
	CustomerID    uuid.UUID `json:"customer_id"`
	Decision      string    `json:"decision"`
	MatchedNames  []string  `json:"matched_names"`
	ScreenedAt    time.Time `json:"screened_at"`
	KYCTier       string    `json:"kyc_tier"`
	RiskBand      string    `json:"risk_band"`
}

// naira renders a kobo amount as NGN with comma grouping.
func naira(kobo int64) string {
	neg := kobo < 0
	if neg {
		kobo = -kobo
	}
	whole := kobo / 100
	frac := kobo % 100
	var out []byte
	if neg {
		out = append(out, '-')
	}
	s := fmt.Sprintf("%d", whole)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return fmt.Sprintf("NGN %s.%02d", string(out), frac)
}

// writeHeader writes the fixed regulator preamble: reporter, report type,
// generation timestamp.
func writeHeader(w io.Writer, reportType string, generatedAt time.Time) error {
	header := [][]string{
		{"Reporter", Reporter},
		{"Report", reportType},
		{"Generated", generatedAt.UTC().Format(time.RFC3339)},
		{},
	}
	cw := csv.NewWriter(w)
	for _, row := range header {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// STRCSV encodes STR rows as a comma-separated regulator file.
func STRCSV(w io.Writer, rows []STRRow, generatedAt time.Time) error {
	if err := writeHeader(w, TypeSTR, generatedAt); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ','
	cw.UseCRLF = true
	if err := cw.Write([]string{
		"Report date", "Customer name", "Customer phone", "Customer id",
		"Transaction id", "Provider reference", "Amount", "Currency",
		"Merchant", "Transaction date", "Alert rule", "Severity", "Alert status", "Case status",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.ReportDate.UTC().Format(time.RFC3339),
			r.CustomerName,
			r.CustomerPhone,
			r.CustomerID.String(),
			r.TransactionID.String(),
			r.ProviderReference,
			naira(r.AmountKobo),
			currencyOr(r.Currency, "NGN"),
			r.MerchantName,
			r.TransactionDate.UTC().Format(time.RFC3339),
			r.AlertRule,
			r.AlertSeverity,
			r.AlertStatus,
			r.CaseStatus,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// CTRCSV encodes currency transaction report rows as CSV.
func CTRCSV(w io.Writer, rows []CTRRow, generatedAt time.Time) error {
	if err := writeHeader(w, TypeCTR, generatedAt); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write([]string{
		"Report date", "Customer name", "Customer phone", "Customer id",
		"Transaction id", "Provider reference", "Amount", "Currency", "Merchant", "Transaction date",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.ReportDate.UTC().Format(time.RFC3339),
			r.CustomerName,
			r.CustomerPhone,
			r.CustomerID.String(),
			r.TransactionID.String(),
			r.ProviderReference,
			naira(r.AmountKobo),
			currencyOr(r.Currency, "NGN"),
			r.MerchantName,
			r.TransactionDate.UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// PEPCSV encodes the politically exposed persons list as CSV.
func PEPCSV(w io.Writer, rows []PEPRow, generatedAt time.Time) error {
	if err := writeHeader(w, TypePEP, generatedAt); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write([]string{
		"Report date", "Customer name", "Customer phone", "Customer id",
		"Decision", "Matched names", "Screened at", "KYC tier", "Risk band",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.ReportDate.UTC().Format(time.RFC3339),
			r.CustomerName,
			r.CustomerPhone,
			r.CustomerID.String(),
			r.Decision,
			strings.Join(r.MatchedNames, "; "),
			r.ScreenedAt.UTC().Format(time.RFC3339),
			orDash(r.KYCTier),
			orDash(r.RiskBand),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// STRJSON, CTRJSON and PEPJSON encode the rows as JSON documents.
func STRJSON(rows []STRRow) ([]byte, error) {
	return json.MarshalIndent(rows, "", "  ")
}

func CTRJSON(rows []CTRRow) ([]byte, error) {
	return json.MarshalIndent(rows, "", "  ")
}

func PEPJSON(rows []PEPRow) ([]byte, error) {
	return json.MarshalIndent(rows, "", "  ")
}

func currencyOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func orDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
