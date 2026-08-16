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
		"money":       func(k int64) string { return "NGN" },
		"maskPII":     func(s string) string { return s },
		"statusClass": func(any) string { return "" },
		"percent":     func(float64) string { return "" },
		"sub":         func(a, b int64) int64 { return a - b },
		"inc":         func(i int) int { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
		"date":        func(any) string { return "" },
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
}
