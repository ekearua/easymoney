package app

import (
	"encoding/json"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestHostedFieldsTemplateMountsEveryConfiguredField guards the silent failure
// mode behind the unusable secure-card page: the Hosted Fields SDK mounts one
// iframe per configured selector, so a container renamed in the template (or a
// selector changed in the configuration) leaves an empty box with no input and
// no error anywhere. Every selector the configuration advertises must exist as
// an element id in the served page.
func TestHostedFieldsTemplateMountsEveryConfiguredField(t *testing.T) {
	t.Parallel()
	client := interswitch.New(interswitch.Options{
		MerchantCode: "MX286980",
		PayItemID:    "Default_Payable_MX286980",
		BaseURL:      "http://localhost",
		Mode:         "TEST",
	})
	page := client.NewHostedFieldsPage("wpd_ref", "user@test.com", 50_000, "http://localhost/payments/return")

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money": func(int64) string { return "NGN" },
	}).ParseFS(web.Assets, "templates/hosted_fields_checkout.html")
	if err != nil {
		t.Fatalf("parse hosted fields template: %v", err)
	}
	var buf strings.Builder
	err = tmpl.ExecuteTemplate(&buf, "hosted_fields_checkout.html", map[string]any{
		"AppName": "Xego",
		"Payment": store.PaymentView{
			MerchantName: "Lagos Lunchbox",
			Payment:      domain.Payment{AmountKobo: 49_255, Status: domain.StatusInitialized},
		},
		"Page": page,
	})
	if err != nil {
		t.Fatalf("execute hosted fields template: %v", err)
	}
	rendered := buf.String()

	var config struct {
		Cardinal struct {
			ContainerSelector string `json:"containerSelector"`
		} `json:"cardinal"`
		Fields map[string]struct {
			Selector string `json:"selector"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(page.ConfigJSON), &config); err != nil {
		t.Fatalf("configuration is not valid JSON: %v", err)
	}

	selectors := []string{config.Cardinal.ContainerSelector}
	for _, field := range config.Fields {
		selectors = append(selectors, field.Selector)
	}
	for _, selector := range selectors {
		if !strings.HasPrefix(selector, "#") {
			t.Fatalf("selector %q is not an id selector", selector)
		}
		if !strings.Contains(rendered, `id="`+strings.TrimPrefix(selector, "#")+`"`) {
			t.Fatalf("no element matches the configured selector %q", selector)
		}
	}

	for _, want := range []string{page.SDKURL, "/static/hosted_fields.js", "id=\"details-page\"", "id=\"pin-page\"", "id=\"otp-page\"", "id=\"hf-dots\""} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered page is missing %q", want)
		}
	}
}

// TestAdminKYCAlertRailFilter pins the compliance-queue rail triage: the filter
// links render, the selected rail is the active one, and each alert row shows
// the rails it fired over plus the reference that points at the rail row.
func TestAdminKYCAlertRailFilter(t *testing.T) {
	t.Parallel()
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
	}).ParseFS(web.Assets, "templates/admin_kyc.html", "templates/nav.html")
	if err != nil {
		t.Fatalf("parse admin kyc template: %v", err)
	}
	var buf strings.Builder
	err = tmpl.ExecuteTemplate(&buf, "admin_kyc.html", map[string]any{
		"AppName":    "Xego",
		"Title":      "KYC ladder",
		"CSRF":       "x",
		"AdminRole":  "admin",
		"Profiles":   []store.KYCProfile{},
		"Cases":      []store.ManualReviewCase{},
		"ByTier":     map[string]int{"L0": 0, "L1": 0, "L2": 0, "L3": 0},
		"AlertRails": []string{"payment", "topup", "standing_credit", "refund", "payout"},
		"AlertRail":  "standing_credit",
		"Alerts": []store.TransactionAlert{{
			ID: 7, UserID: uuid.New(), Rule: "structuring", Severity: "high", Status: "open",
			Details: map[string]any{
				"rails":            []any{"standing_credit"},
				"reference":        "STD-CREDIT-0007",
				"report_threshold": float64(1_000_000),
			},
			CreatedAt: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("execute admin_kyc.html: %v", err)
	}
	rendered := buf.String()
	for _, want := range []string{
		`href="/admin/kyc?rail=payout"`,
		"<strong>standing_credit</strong>",
		"STD-CREDIT-0007",
		"report_threshold",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered KYC page is missing %q", want)
		}
	}
}

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

	for _, name := range []string{"admin_reports.html", "admin_dsr.html", "admin_ledger.html", "admin_reconciliation.html", "admin_chat_guard.html", "admin_internal_watchlist.html"} {
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
	execMetrics := map[string]any{
		"AppName": "Xego", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin", "Metrics": store.Metrics{Users: 3, Payments: 5, Succeeded: 4, SuccessRate: 0.8, VolumeKobo: 250_000},
		// Priced week: 4493 tokens at 250/₦ ≈ ₦17.97 → kobo math, not template math.
		"AITokens7d":     int64(4493),
		"AITokensPerNGN": int64(250),
		"AISpendKobo":    int64(1797),
	}
	if err := tmpl.ExecuteTemplate(&buf, "metrics.html", execMetrics); err != nil {
		t.Fatalf("execute metrics.html: %v", err)
	}
	metricsRendered := buf.String()
	if !strings.Contains(metricsRendered, "AI extraction spend (7d)") || !strings.Contains(metricsRendered, "4493 tokens at 250/₦") {
		t.Fatal("metrics.html must surface the 7-day AI token spend with its pricing basis")
	}

	buf.Reset()
	execMetricsUnpriced := map[string]any{
		"AppName": "Xego", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin", "Metrics": store.Metrics{},
		"AITokens7d":     int64(900),
		"AITokensPerNGN": int64(0),
		"AISpendKobo":    int64(0),
	}
	if err := tmpl.ExecuteTemplate(&buf, "metrics.html", execMetricsUnpriced); err != nil {
		t.Fatalf("execute unpriced metrics.html: %v", err)
	}
	if !strings.Contains(buf.String(), "AI tokens (7d)") || strings.Contains(buf.String(), "AI extraction spend") {
		t.Fatal("an unpriced week must render the token count without a naira estimate")
	}

	buf.Reset()
	execReminders := map[string]any{
		"AppName":   "Xego",
		"Title":     "Onboarding reminders",
		"CSRF":      "x",
		"AdminRole": "admin",
		"Stats": store.OnboardingReminderStats{
			StalledByStage:         map[string]int{"name": 1, "email": 2, "details": 3, "confirm": 0, "kyb": 4, "start": 5},
			Sent24h:                6,
			Pending24h:             7,
			Failed24h:              8,
			CompletedAfterReminder: 9,
		},
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_onboarding_reminders.html", execReminders); err != nil {
		t.Fatalf("execute admin_onboarding_reminders.html: %v", err)
	}
	remindersRendered := buf.String()
	if !strings.Contains(remindersRendered, "Deliverability guard") || !strings.Contains(remindersRendered, "kyb") {
		t.Fatal("admin_onboarding_reminders.html must render the guard panel and the stage table")
	}

	buf.Reset()
	execMedia := map[string]any{
		"AppName":   "Xego",
		"Title":     "Channel media",
		"CSRF":      "x",
		"AdminRole": "admin",
		"Report": store.ChannelMediaReport{
			Since:         time.Now(),
			TotalAttempts: 2,
			TotalTokens:   390,
			Totals: []store.ChannelMediaStat{
				{Channel: "whatsapp", MediaKind: "image", Attempts: 2, Successes: 1, Failures: 1, AITokens: 390},
			},
			PerChannelDay: []store.ChannelMediaDayStat{
				{Day: time.Now(), Channel: "whatsapp", MediaKind: "image", Attempts: 2, Successes: 1, Failures: 1, AITokens: 390},
			},
		},
		"TokenTrend": mediaTokenTrendSVG([]store.TokenDayStat{
			{Day: time.Now().AddDate(0, 0, -1), Tokens: 90},
			{Day: time.Now(), Tokens: 300},
		}, 250),
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_media_report.html", execMedia); err != nil {
		t.Fatalf("execute admin_media_report.html: %v", err)
	}
	mediaRendered := buf.String()
	if !strings.Contains(mediaRendered, "whatsapp") || !strings.Contains(mediaRendered, "50%") {
		t.Fatal("admin_media_report.html must render the per-channel success rate")
	}
	if !strings.Contains(mediaRendered, `class="token-trend"`) || !strings.Contains(mediaRendered, "tt-bar") {
		t.Fatal("admin_media_report.html must render the per-day AI token trend sparkline")
	}
	// The spike day (300 of a 300 peak) must get the spike highlight class.
	if !strings.Contains(mediaRendered, "tt-spike") {
		t.Fatal("token trend must highlight near-peak spike days")
	}
	buf.Reset()
	execMediaEmpty := map[string]any{
		"AppName": "Xego", "Title": "Channel media", "CSRF": "x", "AdminRole": "admin",
		"Report":     store.ChannelMediaReport{Since: time.Now()},
		"TokenTrend": mediaTokenTrendSVG(nil, 0),
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_media_report.html", execMediaEmpty); err != nil {
		t.Fatalf("execute empty admin_media_report.html: %v", err)
	}
	if strings.Contains(buf.String(), "token-trend\" viewBox") {
		t.Fatal("empty window must not render a trend sparkline")
	}

	// G8: the attestation checklist executes with one overdue in-progress
	// cycle and renders the record form, and with no cycles at all.
	buf.Reset()
	execAttest := map[string]any{
		"AppName": "Faya", "Title": "Compliance attestations", "CSRF": "x", "AdminRole": "compliance",
		"Cycles": []store.ComplianceAttestation{{
			ID: 7, CycleYear: 2026, Scope: "Automated AML solution",
			Status:  store.AttestationInProgress,
			DueDate: time.Now().Add(-24 * time.Hour),
		}},
		"Now": time.Now(),
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_compliance.html", execAttest); err != nil {
		t.Fatalf("execute admin_compliance.html: %v", err)
	}
	attestRendered := buf.String()
	if !strings.Contains(attestRendered, "OVERDUE") || !strings.Contains(attestRendered, "/admin/compliance/7/record") {
		t.Fatal("admin_compliance.html must flag overdue cycles and post to the record endpoint")
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "admin_compliance.html", map[string]any{
		"AppName": "Faya", "Title": "Compliance attestations", "CSRF": "x", "AdminRole": "compliance",
		"Cycles": []store.ComplianceAttestation{}, "Now": time.Now(),
	}); err != nil {
		t.Fatalf("execute empty admin_compliance.html: %v", err)
	}

	// G2: the payment-screening queue executes with one open hold (the
	// disposition form posts to the per-row endpoint with the CSRF field)
	// and with an empty queue.
	buf.Reset()
	holdAt := time.Now()
	execScreening := map[string]any{
		"AppName": "Faya", "Title": "Payment screening", "CSRF": "x", "AdminRole": "compliance",
		"Holds": []store.PaymentScreening{{
			ID: 9, Rail: store.RailMerchantPayout, MovementRef: "payout:BATCH-X:abc",
			AmountKobo: 2_500_000, CounterpartyName: "Similar Sanctioned Ltd",
			AccountNumber: "0123456790", BankCode: "044", Provider: "simulated",
			Decision: kyc.ScreenStrong, MatchedNames: []string{"Similar Sanctioned Ltd"},
			Message: "fuzzy hit", CreatedAt: holdAt,
		}},
		"Recent": []store.PaymentScreening{},
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_payment_screenings.html", execScreening); err != nil {
		t.Fatalf("execute admin_payment_screenings.html: %v", err)
	}
	screeningRendered := buf.String()
	if !strings.Contains(screeningRendered, "/admin/payment-screenings/9/dispose") || !strings.Contains(screeningRendered, "csrf_token") {
		t.Fatal("admin_payment_screenings.html must post dispositions to the per-row endpoint with a CSRF field")
	}
	if strings.Contains(screeningRendered, "No open holds") {
		t.Fatal("open hold must render, not the empty-queue message")
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "admin_payment_screenings.html", map[string]any{
		"AppName": "Faya", "Title": "Payment screening", "CSRF": "x", "AdminRole": "compliance",
		"Holds": []store.PaymentScreening{}, "Recent": []store.PaymentScreening{},
	}); err != nil {
		t.Fatalf("execute empty admin_payment_screenings.html: %v", err)
	}

	// G7 residual: the audit page renders the scheduled sweep history and
	// the open alert's disposition form, and executes cleanly without them.
	buf.Reset()
	execAudit := map[string]any{
		"AppName": "Faya", "Title": "Audit log", "CSRF": "x", "AdminRole": "compliance",
		"Entries":    []store.AuditLog{},
		"ChainCount": 3, "ChainBroken": -1,
		"ChainChecks":   []store.AuditChainCheck{{ID: 1, CheckedAt: time.Now(), RowsChecked: 3, BrokenAt: -1, AlertOpen: false}},
		"ChainAlert":    store.AuditChainAlert{ID: 9, BrokenAtRow: 2, OpenedAt: time.Now()},
		"HasChainAlert": true,
	}
	if err := tmpl.ExecuteTemplate(&buf, "admin_audit.html", execAudit); err != nil {
		t.Fatalf("execute admin_audit.html (chain panels): %v", err)
	}
	auditRendered := buf.String()
	if !strings.Contains(auditRendered, "/admin/audit/chain-alerts/9/dispose") {
		t.Fatal("admin_audit.html must render the open chain alert's disposition form")
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "admin_audit.html", map[string]any{
		"AppName": "Faya", "Title": "Audit log", "CSRF": "x", "AdminRole": "compliance",
		"Entries": []store.AuditLog{}, "ChainCount": 0, "ChainBroken": -1,
		"ChainChecks": []store.AuditChainCheck{}, "ChainAlert": store.AuditChainAlert{}, "HasChainAlert": false,
	}); err != nil {
		t.Fatalf("execute admin_audit.html (no sweeps): %v", err)
	}

	buf.Reset()
	flowPage := webFlowPage{
		AppName: "Xego", FlowType: "pay", Token: strings.Repeat("a", 40), Title: "Review your payment",
		WhatsAppLink: "https://wa.me/234", BaseURL: "http://localhost:8080", Steps: []webFlowStepLabel{{Label: "Merchant", State: "done", Dot: "✓"}, {Label: "Review", State: "current", Dot: "4"}},
		Shell:  true,
		Review: []webFlowLine{{Term: "Merchant", Desc: "Ade's Kitchen"}},
		Error:  "Choose a payment method.",
		Fields: []webFlowField{
			{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: []webFlowOption{{Value: "interswitch", Label: "Card checkout"}, {Value: "bank_transfer", Label: "Bank transfer"}}},
			{Name: "note", Label: "Note", Type: "textarea"},
			{Name: "merchant_slug", Label: "Merchant", Type: "select", Options: []webFlowOption{{Value: "ade-kitchen", Label: "Ade's Kitchen"}}},
			{Name: "amount_kobo", Label: "Amount", Type: "amount"},
			{Name: "due_date", Label: "Due", Type: "date"},
			{Name: "ok", Label: "Hidden", Type: "hidden"},
			{Name: "id_slip", Label: "Photo of your NIN slip", Type: "upload", MediaPrompt: "Extract the 11-digit NIN or BVN number from this identity slip. Reply with only the digits.", Value: "NIN: 12345678901"},
			{Name: "id_voice", Label: "Say your number", Type: "voice"},
		},
		Actions: []webFlowAction{{Kind: "submit", Name: "pay", Label: "Pay now"}},
	}
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", flowPage); err != nil {
		t.Fatalf("execute webflow.html: %v", err)
	}
	if !strings.Contains(buf.String(), "name=\"method\"") {
		t.Fatal("webflow.html must render grouped radio inputs with the field name")
	}
	html := buf.String()
	if !strings.Contains(html, "/w/"+flowPage.Token+"/media") {
		t.Fatal("webflow.html must render the media upload action for upload/voice fields")
	}
	if !strings.Contains(html, "accept=\"image/*\"") || !strings.Contains(html, "accept=\"audio/*\" capture") {
		t.Fatal("webflow.html must render image capture for upload fields and audio capture for voice fields")
	}
	// The camera scanner is an external, CSP-safe enhancement wired only when
	// an upload (image) field is on the page.
	if !strings.Contains(html, "📷 Scan with camera") || !strings.Contains(html, "data-scan=\"1\"") {
		t.Fatal("webflow.html must render the camera-scan button for image upload fields")
	}
	if !strings.Contains(html, "/static/webflow-scan.js") {
		t.Fatal("webflow.html must load the external scan script")
	}
	if !strings.Contains(html, "/static/date-picker.js") {
		t.Fatal("webflow.html must load the friendly date-picker script")
	}
	// A date field stays a native type="date" box so the external date-picker
	// script (enhancement keyed on input[type=date]) can upgrade it to
	// independent day/month/year selects without a server-round trip.
	if !strings.Contains(html, `type="date"`) {
		t.Fatal("webflow.html must render web flow date fields as type=date for the date-picker enhancement")
	}
	if !strings.Contains(html, "Read from your upload:") || !strings.Contains(html, "NIN: 12345678901") {
		t.Fatal("webflow.html must show previously extracted upload text as confirmation")
	}
	// Stepped layer: a page carrying steps scopes the flow CSS (main.wf) and
	// renders the stepper with done/current states from the labels the flow
	// engine supplies — the same shell for money, KYC, and thrift flows.
	if !strings.Contains(html, "class=\"receipt wf\"") {
		t.Fatal("webflow.html must scope the stepped flow CSS layer via main.wf when the page has steps")
	}
	if !strings.Contains(html, "wf-stepper") || !strings.Contains(html, "class=\"done\"") || !strings.Contains(html, "class=\"current\"") {
		t.Fatal("webflow.html must render the horizontal stepper with done/current states for stepped flows")
	}
	if !strings.Contains(html, "Merchant") || !strings.Contains(html, ">✓<") {
		t.Fatal("webflow.html stepper must carry step labels and the done checkmark")
	}
	if !strings.Contains(html, "Extract the 11-digit NIN or BVN number from this identity slip") {
		t.Fatal("webflow.html must carry the OCR prompt as a hidden field")
	}
	// The upload/voice inputs must live in their own form, not inside the main
	// POST-to-/w/{token} form (nested forms are invalid HTML). The main form
	// is the first one to close, so the media form must start after it.
	mainFormEnd := strings.Index(html, "</form>")
	mediaForm := strings.Index(html, "/media")
	if mediaForm < 0 || mediaForm < mainFormEnd {
		t.Fatal("media upload form must render after the main flow form, not nested inside it")
	}

	// A flow without a step map keeps the plain one-page layout: no scoping
	// class, no stepper, so an unmigrated flow cannot render a half-built shell.
	buf.Reset()
	plainPage := flowPage
	plainPage.Steps = nil
	plainPage.Shell = false
	plainPage.FlowType = "invoice_create"
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", plainPage); err != nil {
		t.Fatalf("execute webflow.html (unstepped): %v", err)
	}
	plainHTML := buf.String()
	if strings.Contains(plainHTML, "receipt wf") || strings.Contains(plainHTML, "wf-stepper") {
		t.Fatal("webflow.html must not render the stepped shell for a flow without steps")
	}

	// AI search bar: a page whose fields carry BarIcon markers renders one
	// ask-bar with file/camera/mic icons backed by hidden /media forms, and
	// those fields never render the stacked media sections.
	buf.Reset()
	barPage := flowPage
	barPage.Steps = nil
	barPage.Shell = false
	barPage.Fields = []webFlowField{
		{Name: "ai_ask", Label: "Ask Xego", Type: "aisearch", Hint: "e.g. pay Ade's Kitchen"},
		{Name: "bill_photo", Label: "Attach a bill", Type: "upload", BarIcon: "file", MediaPrompt: "Read the bill"},
		{Name: "bill_voice", Label: "Or say it", Type: "voice", BarIcon: "mic"},
	}
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", barPage); err != nil {
		t.Fatalf("execute webflow.html (ai bar): %v", err)
	}
	barHTML := buf.String()
	if !strings.Contains(barHTML, "wf-aibar") || !strings.Contains(barHTML, "id=\"wf-ai-input\"") {
		t.Fatal("webflow.html must render the AI ask-bar with its input")
	}
	// The typed ask must reach the server: the input carries the ai_ask name
	// and binds to the main flow form (it renders before the form element).
	if !strings.Contains(barHTML, `name="ai_ask" form="wf-main"`) {
		t.Fatal("the ask-bar input must carry name=ai_ask and bind to the main flow form so typed text is submitted")
	}
	if !strings.Contains(barHTML, `id="wf-main"`) {
		t.Fatal("the main flow form must carry id=wf-main for the ask-bar binding")
	}
	if !strings.Contains(barHTML, `data-ai-open-upload="bill_photo"`) || !strings.Contains(barHTML, `data-ai-open-scan="bill_photo"`) || !strings.Contains(barHTML, `data-ai-open-voice="bill_voice"`) {
		t.Fatal("webflow.html must render file/camera/mic icons carrying their backing field names")
	}
	if !strings.Contains(barHTML, `data-backing-form="bill_photo"`) || !strings.Contains(barHTML, `data-backing-form="bill_voice"`) {
		t.Fatal("webflow.html must render one hidden backing /media form per bar icon field")
	}
	if !strings.Contains(barHTML, `name="prompt" value="Read the bill"`) {
		t.Fatal("webflow.html must carry the upload OCR prompt in the backing form")
	}
	if strings.Contains(barHTML, "media-upload") {
		t.Fatal("bar-icon fields must not render the stacked media-upload sections")
	}
	// Backing forms must also sit outside the main flow form (no nesting).
	barMainFormEnd := strings.Index(barHTML, "</form>")
	if backingAt := strings.Index(barHTML, "data-backing-form"); backingAt < 0 || backingAt < barMainFormEnd {
		t.Fatal("AI-bar backing forms must render after the main flow form, not nested inside it")
	}

	// Done pages never show a stepper even for money flows.
	buf.Reset()
	donePage := flowPage
	donePage.Done = true
	donePage.DoneTitle = "Payment sent"
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", donePage); err != nil {
		t.Fatalf("execute webflow.html (done): %v", err)
	}
	if strings.Contains(buf.String(), "wf-stepper") {
		t.Fatal("webflow.html must not render the stepper on done pages")
	}
}
