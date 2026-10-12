package app

// Template contracts for the Trust Fabric admin surfaces: the network ring
// view and the per-profile trust timeline must parse and execute with the
// production funcmap shape, render rings/links with masked account numbers,
// keep the timeline chronological markers, and degrade to their empty
// states without data.

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// parseAdminTemplate parses one admin template with the production funcmap
// shape (the funcs the pages actually call), so a template referencing a
// missing func or a typo'd field fails here rather than in production.
func parseAdminTemplate(t *testing.T, path, name string) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":       func(int64) string { return "NGN" },
		"maskPII":     func(s string) string { return s },
		"statusClass": func(any) string { return "" },
		"percent":     func(float64) string { return "" },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int) int { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
		"date":        func(any) string { return "" },
	}).ParseFS(web.Assets, path, "templates/nav.html")
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return tmpl
}

// testUUID returns a fixed UUID for template fixtures.
func testUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAdminNetworkTemplateContract(t *testing.T) {
	tmpl := parseAdminTemplate(t, "templates/admin_network.html", "admin_network.html")

	var buf strings.Builder
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	edges := []store.IdentityLink{
		{Kind: "device", Value: "fingerprint-a", SubjectA: "u-1", SubjectB: "u-2", NameA: "Ada", NameB: "Bode", LastSeen: now, Sightings: 2},
		{Kind: "recipient_account", Value: "0123456789", SubjectA: "u-2", SubjectB: "u-3", LastSeen: now, Sightings: 2},
	}
	rings := store.DetectRingsFromIdentityLinks(edges, 2)
	scored := []store.ScoredRing{}
	for _, ring := range rings {
		members := make([]store.RingMember, 0, len(ring.Members))
		for _, id := range ring.Members {
			members = append(members, store.RingMember{Subject: id, Name: ring.Names[id], Tier: "L1", RiskBand: "low"})
		}
		scored = append(scored, store.ScoredRing{
			Members: members, Edges: edges, Kinds: ring.Kinds,
			Score: 4.2, ConfirmedHits: 1, OpenAlerts: 0, HasCase: true,
		})
	}
	caseRow := store.NetworkCase{
		ID: 7, MembersKey: "u-1,u-2,u-3", MemberCount: 3,
		Members: []string{"u-1", "u-2", "u-3"}, LinkKinds: []string{"device", "recipient_account"},
		Score: 4.2, ConfirmedAlerts: 1, Status: "open",
	}
	err := tmpl.ExecuteTemplate(&buf, "admin_network.html", map[string]any{
		"AppName": "Xego", "Title": "Trust network", "CSRF": "x", "AdminRole": "admin",
		"Links": edges, "Rings": scored, "Cases": []store.NetworkCase{caseRow},
		"MinMembers": 2, "HasLinks": true,
	})
	if err != nil {
		t.Fatalf("execute admin_network.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Identity rings", "score 4.2", "3 identities via",
		// html/template escapes the '+' in the joined kinds text.
		"device &#43; recipient_account",
		"••••6789",                       // recipient account masked
		"fingerprint-a",                  // device facts stay opaque
		"/admin/kyc/u-1",                 // members link to their profiles
		"Ada",                            // resolved display names
		"confirmed ×1",                   // alert signal badge
		"case raised",                    // linked-case badge
		"Linked cases",                   // the disposition panel
		"Resolve ring", "False positive", // single disposition flow
		"L1", "low", // member trust posture columns
	} {
		if !strings.Contains(out, want) {
			t.Errorf("network page missing %q", want)
		}
	}

	// Empty state renders without error and without the rings section.
	buf.Reset()
	err = tmpl.ExecuteTemplate(&buf, "admin_network.html", map[string]any{
		"AppName": "Xego", "Title": "Trust network", "CSRF": "x", "AdminRole": "admin",
		"Links": []store.IdentityLink{}, "Rings": []store.ScoredRing(nil), "Cases": []store.NetworkCase(nil),
		"MinMembers": 2, "HasLinks": false,
	})
	if err != nil {
		t.Fatalf("execute empty admin_network.html: %v", err)
	}
	if !strings.Contains(buf.String(), "No identity links yet") {
		t.Error("network page must explain its empty state")
	}
}

func TestAdminKYCProfileTemplateContract(t *testing.T) {
	tmpl := parseAdminTemplate(t, "templates/admin_kyc_profile.html", "admin_kyc_profile.html")

	var buf strings.Builder
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	timeline := []store.TimelineEntry{
		{Kind: "audit", When: now, Label: "kyc.tier_advanced", Detail: "system"},
		{Kind: "device", When: now.Add(-time.Hour), Label: "device seen: Windows Chrome", Detail: "abcdef01234567890123456789012345"},
		{Kind: "alert", When: now.Add(-2 * time.Hour), Label: "velocity · high", Detail: "open"},
	}
	err := tmpl.ExecuteTemplate(&buf, "admin_kyc_profile.html", map[string]any{
		"AppName": "Xego", "Title": "KYC profile", "CSRF": "x", "AdminRole": "admin",
		"Profile":          store.KYCProfile{UserID: testUUID(t), Tier: "L2", RiskBand: "medium", RiskScore: 42},
		"UserID":           testUUID(t).String(),
		"DisplayName":      "Ada Ring",
		"Timeline":         timeline,
		"SubjectLinks":     []store.IdentityLinkRow{{LinkKind: "device", LinkValue: "fingerprint-a", SubjectA: testUUID(t).String(), SubjectB: "u-2", NameB: "Bode", LastSharedAt: now, Sightings: 2}},
		"TimelineCount":    3,
		"Frozen":           false,
		"PaymentPrefs":     []store.PaymentPreference{},
		"PaymentPrefLines": nil,
		"HasPaymentPrefs":  false,
	})
	if err != nil {
		t.Fatalf("execute admin_kyc_profile.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Ada Ring", "L2", "medium", "Trust timeline",
		"velocity · high", "device seen", "kyc.tier_advanced",
		"Identity links", "fingerprint-a", "Bode",
		"/admin/network",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("profile page missing %q", want)
		}
	}

	// No-data state: no links section, explicit timeline empty state.
	buf.Reset()
	err = tmpl.ExecuteTemplate(&buf, "admin_kyc_profile.html", map[string]any{
		"AppName": "Xego", "Title": "KYC profile", "CSRF": "x", "AdminRole": "admin",
		"Profile": store.KYCProfile{UserID: testUUID(t), Tier: "L0", RiskBand: "high", RiskScore: 80},
		"UserID":  testUUID(t).String(), "DisplayName": "",
		"Timeline": []store.TimelineEntry{}, "SubjectLinks": []store.IdentityLinkRow(nil), "TimelineCount": 0,
		"Frozen":           false,
		"PaymentPrefs":     []store.PaymentPreference{},
		"PaymentPrefLines": nil,
		"HasPaymentPrefs":  false,
	})
	if err != nil {
		t.Fatalf("execute empty admin_kyc_profile.html: %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "No alerts, device sightings, or audit entries") {
		t.Error("profile page must explain its empty timeline")
	}
	if strings.Contains(out, "Identity links") {
		t.Error("profile page must omit the links section when the subject has none")
	}
}
