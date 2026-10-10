package app

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestAdminAdverseMediaTemplate guards the G9 read-only queue page: it renders
// the adverse-media rows (with the hits summary) and an empty state, and the
// nav exposes the link. Dispositions live on the KYC review queue, so the page
// intentionally has no POST form.
func TestAdminAdverseMediaTemplate(t *testing.T) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":             func(k int64) string { return "NGN" },
		"maskPII":           func(s string) string { return s },
		"statusClass":       func(any) string { return "" },
		"percent":           func(float64) string { return "" },
		"sub":               func(a, b int64) int64 { return a - b },
		"add":               func(a, b int64) int64 { return a + b },
		"inc":               func(i int) int { return i + 1 },
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
		"date":              func(any) string { return "2026-10-09" },
		"join":              func(items []string, sep string) string { return strings.Join(items, sep) },
	}).ParseFS(web.Assets, "templates/admin_adverse_media.html", "templates/nav.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	var buf strings.Builder
	err = tmpl.ExecuteTemplate(&buf, "admin_adverse_media.html", map[string]any{
		"AppName": "Xego", "Title": "Adverse media", "CSRF": "x", "AdminRole": "compliance",
		"Screenings": []store.AdverseMediaScreening{{
			ID: 1, Provider: "simulated", Decision: kyc.ScreenBlocked,
			MatchedHits:  []store.AdverseMediaHit{{Source: "OpenSanctions", Title: "flagged"}},
			MatchedNames: []string{"record flagged for adverse media"},
			LegalName:    "Adverse Ventures Ltd", ListVersion: "sim-1",
			ProviderRef: "sim-adverse-1", Message: "blocked by the simulator",
			ScreenedAt: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("execute admin_adverse_media.html: %v", err)
	}
	rendered := buf.String()
	if !strings.Contains(rendered, "Adverse Ventures Ltd") || !strings.Contains(rendered, "sim-1") {
		t.Fatal("admin_adverse_media.html must render the screening row")
	}
	if !strings.Contains(rendered, `href="/admin/adverse-media"`) {
		t.Fatal("nav must expose the adverse-media link")
	}
	if strings.Contains(rendered, `method="post" action="/admin/adverse-media`) {
		t.Fatal("the adverse-media queue is read-only; no POST form expected")
	}

	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "admin_adverse_media.html", map[string]any{
		"AppName": "Xego", "Title": "Adverse media", "CSRF": "x", "AdminRole": "admin",
		"Screenings": []store.AdverseMediaScreening{},
	}); err != nil {
		t.Fatalf("execute empty admin_adverse_media.html: %v", err)
	}
	if !strings.Contains(buf.String(), "No adverse-media screenings") {
		t.Fatal("empty queue must render the empty-state message")
	}
}
