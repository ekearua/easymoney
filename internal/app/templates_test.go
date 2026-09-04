package app

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestTemplatesParse ensures every embedded template, including the C15
// regulator reports page and the C20 data-subject rights page, parses and
// executes with the shared function map.
func TestTemplatesParse(t *testing.T) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":             func(k int64) string { return "NGN" },
		"maskPII":           func(s string) string { return s },
		"statusClass":       func(any) string { return "" },
		"percent":           func(float64) string { return "" },
		"sub":               func(a, b int64) int64 { return a - b },
		"add":               func(a, b int64) int64 { return a + b },
		"inc":               func(i int) int { return i + 1 },
		"join":              func(items []string, sep string) string { return strings.Join(items, sep) },
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
		"date":              func(any) string { return "" },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	for _, name := range []string{"admin_reports.html", "admin_dsr.html", "admin_ledger.html", "admin_reconciliation.html", "admin_chat_guard.html"} {
		if tmpl.Lookup(name) == nil {
			t.Fatalf("%s not found in parsed templates", name)
		}
	}

	var buf strings.Builder
	exec := map[string]any{
		"AppName":   "Xego",
		"Title":     "Regulator reports",
		"CSRF":      "x",
		"AdminRole": "admin",
		"Since":     time.Now(),
		"Threshold": int64(1_000_000_000),
		"Bundle":    map[string]any{"STR": []map[string]any{}, "CTR": []map[string]any{}, "PEP": []map[string]any{}},
		"STRCSV":    "str",
		"CTRCSV":    "ctr",
		"PEPCSV":    "pep",
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_reports.html", exec); err != nil {
		t.Fatalf("execute admin_reports.html: %v", err)
	}

	buf.Reset()
	execDSR := map[string]any{
		"AppName":   "Xego",
		"Title":     "Data subject rights",
		"CSRF":      "x",
		"AdminRole": "compliance",
		"Lookup":    "",
		"Found":     nil,
		"Consents":  []map[string]any{},
		"Export":    nil,
		"Counts":    map[string]int{},
		"Requests":  []map[string]any{},
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_dsr.html", execDSR); err != nil {
		t.Fatalf("execute admin_dsr.html: %v", err)
	}

	buf.Reset()
	execLedger := map[string]any{
		"AppName":        "Xego",
		"Title":          "Ledger",
		"CSRF":           "x",
		"AdminRole":      "admin",
		"Entries":        []store.LedgerEntry{{JournalRef: "r1", EntryType: "debit", Account: "1100_operating_bank", AmountKobo: 100, Description: "d", Hash: "h"}},
		"Balances":       []store.LedgerAccountBalance{{Account: "1100_operating_bank", DebitKobo: 100, CreditKobo: 0, NetKobo: 100}},
		"ChainCount":     2,
		"ChainBroken":    -1,
		"ChainSound":     true,
		"NetTotalKobo":   int64(0),
		"AccountNames":   map[string]string{"1100_operating_bank": "Operating bank"},
		"ReversalResult": "",
		"ReversalError":  "",
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_ledger.html", execLedger); err != nil {
		t.Fatalf("execute admin_ledger.html: %v", err)
	}

	buf.Reset()
	execRecon := map[string]any{
		"AppName":   "Xego",
		"Title":     "Reconciliation",
		"CSRF":      "x",
		"AdminRole": "compliance",
		"Runs":      []store.ReconciliationRun{{ID: 1, RunType: "auto", CreatedBy: "system", InternalCount: 3, LedgerCount: 3, BankCount: 3, DiscrepancyCount: 1, Status: "discrepancies", CreatedAt: time.Now()}},
		"Items":     []store.ReconciliationItem{{Category: "internal_without_ledger", Reference: "r1", ExpectedKobo: 100, ActualKobo: 0, Detail: "succeeded payment has no ledger money-in posting"}},
		"Result":    "",
		"Error":     "",
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_reconciliation.html", execRecon); err != nil {
		t.Fatalf("execute admin_reconciliation.html: %v", err)
	}

	buf.Reset()
	execGuard := map[string]any{
		"AppName":   "Xego",
		"Title":     "Chat guard",
		"AdminRole": "compliance",
		"Events":    []store.ChatGuardEvent{{Channel: "whatsapp", Sender: "+2348012345678", Category: "card", RedactedText: "my card [REDACTED:card] thanks", CreatedAt: time.Now()}},
		"Total":     int64(1),
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_chat_guard.html", execGuard); err != nil {
		t.Fatalf("execute admin_chat_guard.html: %v", err)
	}

	buf.Reset()
	execMsg := map[string]any{
		"AppName":   "Xego",
		"Title":     "Messaging cost",
		"CSRF":      "x",
		"AdminRole": "admin",
		"Summary": store.MessageLogSummary{
			Since: time.Now(), TotalMessages: 10, ServiceMessages: 10,
			EstimatedCostNGN: 140, EstimatedCostUSDC: 10, TotalTransactions: 5,
			AveragePerTxn: 2, MessageCostService: 14,
			PerFlow: []store.MessageFlowStat{{Flow: "pay", Channel: "whatsapp", Messages: 4, Transactions: 2, AveragePerTxn: 2, EstimatedCostNGN: 56}},
		},
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_messaging.html", execMsg); err != nil {
		t.Fatalf("execute admin_messaging.html: %v", err)
	}

	buf.Reset()
	flowPage := webFlowPage{
		AppName: "Xego", FlowType: "pay", Token: strings.Repeat("a", 40), Title: "Review your payment",
		WhatsAppLink: "https://wa.me/234", BaseURL: "http://localhost:8080",
		Review: []webFlowLine{{Term: "Merchant", Desc: "Ade's Kitchen"}},
		Error:  "Choose a payment method.",
		Fields: []webFlowField{
			{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: []webFlowOption{{Value: "interswitch", Label: "Card checkout"}, {Value: "bank_transfer", Label: "Bank transfer"}}},
			{Name: "note", Label: "Note", Type: "textarea"},
			{Name: "merchant_slug", Label: "Merchant", Type: "select", Options: []webFlowOption{{Value: "ade-kitchen", Label: "Ade's Kitchen"}}},
			{Name: "amount_kobo", Label: "Amount", Type: "amount"},
			{Name: "due_date", Label: "Due", Type: "date"},
			{Name: "ok", Label: "Hidden", Type: "hidden"},
		},
		Actions: []webFlowAction{{Kind: "submit", Name: "pay", Label: "Pay now"}},
	}
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", flowPage); err != nil {
		t.Fatalf("execute webflow.html: %v", err)
	}
	if !strings.Contains(buf.String(), "name=\"method\"") {
		t.Fatal("webflow.html must render grouped radio inputs with the field name")
	}
}
