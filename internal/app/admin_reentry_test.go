package app

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// TestBuildReentryPanelReading pins the panel's actual product: the sentence an
// operator reads instead of doing the arithmetic. Every branch must name the
// sample it rests on, because the failure mode of an evidence dashboard is a
// confident percentage drawn from three rows — and this panel exists precisely
// to stop a consequential decision (a PCI DSS scope change) being made on one.
func TestBuildReentryPanelReading(t *testing.T) {
	t.Parallel()
	window := 30 * 24 * time.Hour

	cases := []struct {
		name        string
		metrics     store.CardReentryMetrics
		wantIn      []string
		wantNotIn   []string
		wantHasData bool
	}{
		{
			name:        "an empty funnel refuses to be read",
			metrics:     store.CardReentryMetrics{},
			wantIn:      []string{"No payments in this window", "cannot be made on an empty funnel"},
			wantNotIn:   []string{"%"},
			wantHasData: false,
		},
		{
			name:        "payments but no card attempts is not a card finding",
			metrics:     store.CardReentryMetrics{Payments: 12, RepeatPayments: 4},
			wantIn:      []string{"No card attempts in this window", "another rail"},
			wantHasData: true,
		},
		{
			name:        "a sample too small is refused rather than called",
			metrics:     store.CardReentryMetrics{Payments: 12, CardAttempts: 9, CardAbandoned: 5},
			wantIn:      []string{"Only 9 card attempt(s)", "too few to read a rate from", "starts saying something at 30"},
			wantHasData: true,
		},
		{
			name:        "material friction is named as the case for spending",
			metrics:     store.CardReentryMetrics{Payments: 400, CardAttempts: 100, CardAbandoned: 30},
			wantIn:      []string{"30% of card attempts did not complete (30 of 100)", "over the 15%", "case for spending on saved methods"},
			wantHasData: true,
		},
		{
			name:        "immaterial friction keeps the do-nothing option alive",
			metrics:     store.CardReentryMetrics{Payments: 400, CardAttempts: 100, CardAbandoned: 5},
			wantIn:      []string{"5% of card attempts did not complete (5 of 100)", "below the 15%", "Option 0"},
			wantHasData: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panel := buildReentryPanel(tc.metrics, nil, window)
			if panel.HasData != tc.wantHasData {
				t.Errorf("HasData = %v, want %v", panel.HasData, tc.wantHasData)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(panel.Reading, want) {
					t.Errorf("reading %q missing %q", panel.Reading, want)
				}
			}
			for _, bad := range tc.wantNotIn {
				if strings.Contains(panel.Reading, bad) {
					t.Errorf("reading %q should not contain %q", panel.Reading, bad)
				}
			}
			if panel.Window != "30d" {
				t.Errorf("window label = %q, want 30d", panel.Window)
			}
		})
	}
}

// TestBuildReentryPanelFigures pins the displayed figures, including the
// per-merchant breakdown and the two labels a reader needs to interpret the
// follow-up proxy honestly: how long the window is, and that it is a proxy.
func TestBuildReentryPanelFigures(t *testing.T) {
	t.Parallel()
	metrics := store.CardReentryMetrics{
		Payments: 40, CardAttempts: 12, CardAbandoned: 3,
		RepeatPayments: 18, RepeatPaymentPayerCount: 4, CardFollowUps: 2,
	}
	byMerchant := []store.MerchantReentryMetric{
		{MerchantID: uuid.New(), MerchantName: "Ade's Kitchen", Payments: 25, CardAttempts: 8, CardAbandoned: 2, RepeatPayments: 14},
		{MerchantID: uuid.New(), MerchantName: "Kola Foods", Payments: 15, CardAttempts: 4, CardAbandoned: 1, RepeatPayments: 4},
	}

	panel := buildReentryPanel(metrics, byMerchant, 30*24*time.Hour)

	if panel.AbandonRate != "25%" {
		t.Errorf("abandon rate = %q, want 25%%", panel.AbandonRate)
	}
	if panel.RepeatShare != "45%" {
		t.Errorf("repeat share = %q, want 45%%", panel.RepeatShare)
	}
	if panel.FollowUps != 2 {
		t.Errorf("follow-ups = %d, want 2", panel.FollowUps)
	}
	if panel.FollowUpWindow != "30m" {
		t.Errorf("follow-up window label = %q, want 30m", panel.FollowUpWindow)
	}
	if len(panel.Merchants) != 2 {
		t.Fatalf("merchant rows = %d, want 2", len(panel.Merchants))
	}
	if panel.Merchants[0].Name != "Ade's Kitchen" || panel.Merchants[0].RepeatPayments != 14 {
		t.Errorf("merchant order/content drifted: %+v", panel.Merchants[0])
	}
	if panel.Merchants[0].AbandonRate != "25%" {
		t.Errorf("merchant abandon rate = %q, want 25%%", panel.Merchants[0].AbandonRate)
	}
	// A merchant with no card attempts must read as 0%, not NaN or a crash.
	panel = buildReentryPanel(metrics, []store.MerchantReentryMetric{{MerchantName: "Wallet only", Payments: 3}}, 30*24*time.Hour)
	if panel.Merchants[0].AbandonRate != "0%" {
		t.Errorf("no-attempt merchant rate = %q, want 0%%", panel.Merchants[0].AbandonRate)
	}
}

// TestMetricsTemplateReentryPanel renders metrics.html with the evidence panel
// so template drift is caught here rather than on the dashboard, and pins the
// two things the panel must say out loud: the follow-up figure is a proxy, and
// no message content was read to produce it.
func TestMetricsTemplateReentryPanel(t *testing.T) {
	t.Parallel()
	tmpl := parseTOTPTestTemplates(t)
	buf := new(strings.Builder)

	panel := buildReentryPanel(store.CardReentryMetrics{
		Payments: 40, CardAttempts: 100, CardAbandoned: 30,
		RepeatPayments: 18, RepeatPaymentPayerCount: 4, CardFollowUps: 2,
	}, []store.MerchantReentryMetric{
		{MerchantID: uuid.New(), MerchantName: "Ade's Kitchen", Payments: 25, CardAttempts: 8, CardAbandoned: 2, RepeatPayments: 14},
	}, 30*24*time.Hour)

	data := map[string]any{
		"AppName": "Faya", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin",
		"Metrics": store.Metrics{}, "ReentryPanel": panel,
	}
	if err := tmpl.ExecuteTemplate(buf, "metrics.html", data); err != nil {
		t.Fatalf("execute metrics.html with re-entry panel: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Saved-method evidence (30d)",
		"Card attempts",
		"Kitchen",
		// The merchant name is rendered through html/template, so the
		// apostrophe must arrive escaped: proof the panel escapes rather than
		// interpolates.
		"&#39;",
		"proxy, not a support-ticket count",
		"no message content is read",
		"sealed at rest",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered panel missing %q", want)
		}
	}

	// Without the panel the page still renders: the block is guarded, so the
	// dashboard degrades rather than breaking when the query fails.
	buf.Reset()
	if err := tmpl.ExecuteTemplate(buf, "metrics.html", map[string]any{
		"AppName": "Faya", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin",
		"Metrics": store.Metrics{},
	}); err != nil {
		t.Fatalf("execute metrics.html without re-entry panel: %v", err)
	}
	if strings.Contains(buf.String(), "Saved-method evidence") {
		t.Error("panel heading rendered without a panel")
	}
}

// TestWindowLabel covers the window formatter the panel uses for both the
// measurement window and the follow-up proxy window.
func TestWindowLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * 24 * time.Hour, "30d"},
		{7 * 24 * time.Hour, "7d"},
		{24 * time.Hour, "1d"},
		{6 * time.Hour, "6h"},
		{90 * time.Minute, "1h"},
		{30 * time.Minute, "30m"},
		{45 * time.Second, "45s"},
	}
	for _, c := range cases {
		if got := windowLabel(c.in); got != c.want {
			t.Errorf("windowLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
