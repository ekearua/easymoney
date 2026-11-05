package app

// Guards the two card consoles' rendered shape. The handover form must never
// invite an identity-document number (the workflow records the document type
// only), and the compliance queue must show the reference and the gate state an
// operator acts on — a template that silently drops either would make the
// console unusable without any error.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// cardPageTemplates parses every template with the same function map the app
// mounts, so a broken card page fails here rather than at first request.
func cardPageTemplates(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     maskPII,
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"mul":         func(a, b int64) int64 { return a * b },
		"divf": func(a, b float64, c float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b * c
		},
		"inc": func(i int) int { return i + 1 },
		"collectionFeeKobo": func(p store.PaymentView) int64 {
			var meta struct {
				CollectionFeeKobo int64 `json:"collection_fee_kobo"`
			}
			if len(p.Metadata) > 0 {
				_ = json.Unmarshal(p.Metadata, &meta)
			}
			return meta.CollectionFeeKobo
		},
		"join":      func(items []string, sep string) string { return strings.Join(items, sep) },
		"hasPrefix": func(s, prefix string) bool { return strings.HasPrefix(s, prefix) },
		"date": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Local().Format("Jan 2, 2006 3:04 PM")
			case *time.Time:
				if v == nil {
					return ""
				}
				return v.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return tmpl
}

func TestAdminCardRequestsPage(t *testing.T) {
	t.Parallel()
	tmpl := cardPageTemplates(t)
	now := time.Now()
	agentID := uuid.New()
	request := store.CardRequest{
		ID: uuid.New(), UserID: uuid.New(), Channel: "whatsapp", RegionID: uuid.New(),
		RegionName: "Ibadan", CardholderName: "Ada Obi", Status: store.CardRequestApproved,
		ReviewerEmail: "reviewer@xego.ng", ReviewedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	ready := request
	ready.Status = store.CardRequestReady
	ready.AgentMerchantID = &agentID
	ready.AgentName = "Ibadan Agent"
	ready.IssuerReference = "ISW-CARD-0001"
	ready.ReadyAt = &now
	collected := ready
	collected.Status = store.CardRequestCollected
	collected.CollectionIDType = "nin"
	collected.CollectedAt = &now

	data := map[string]any{
		"AppName": "Xego", "Title": "Card requests", "CSRF": "csrf", "AdminRole": "compliance",
		"AllReady": false,
		"Gates": []cardGateView{
			{CardIssuerGate: store.CardIssuerGate{GateKey: store.CardGateIssuerContract, Ready: true,
				EvidenceRef: "MSA-2026-01", AttestedBy: "reviewer@xego.ng", AttestedAt: &now},
				Label: "Issuer contract", Help: "Signed agreement."},
			{CardIssuerGate: store.CardIssuerGate{GateKey: store.CardGatePCIScope},
				Label: "PCI scope", Help: "Data-flow review."},
		},
		"Requests": []adminCardRequestView{{
			CardRequest: request, Reference: "XG-CARD-ABC12345",
			Agents: []store.AgentOption{{MerchantID: agentID, Name: "Ibadan Agent"}},
		}},
		"Ready":     []adminCardRequestView{{CardRequest: ready, Reference: "XG-CARD-READY001"}},
		"Collected": []adminCardRequestView{{CardRequest: collected, Reference: "XG-CARD-DONE0001"}},
		"Error":     "",
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "admin_card_requests.html", data); err != nil {
		t.Fatalf("execute admin card page: %v", err)
	}
	page := buf.String()
	for _, want := range []string{
		"Readiness gates", "Issuer contract", "MSA-2026-01", "PCI scope",
		"XG-CARD-ABC12345", "Ada Obi", "Ibadan Agent",
		"XG-CARD-READY001", "Resend code", "XG-CARD-DONE0001", "nin",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("admin card page is missing %q", want)
		}
	}
	// The gates are not all evidenced, so no stock can be marked ready yet.
	if strings.Contains(page, "Mark ready &amp; send code") || strings.Contains(page, "Mark ready & send code") {
		t.Error("admin card page offers a ready action while readiness is incomplete")
	}
}

func TestAgentCardPageRecordsIDTypeOnly(t *testing.T) {
	t.Parallel()
	tmpl := cardPageTemplates(t)
	now := time.Now()
	agentID := uuid.New()
	ready := store.CardRequest{
		ID: uuid.New(), UserID: uuid.New(), Channel: "whatsapp", RegionID: uuid.New(),
		RegionName: "Ibadan", CardholderName: "Ada Obi", Status: store.CardRequestReady,
		AgentMerchantID: &agentID, AgentName: "Ibadan Agent", ReadyAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	data := map[string]any{
		"AppName": "Xego", "Title": "Card handover", "CSRF": "csrf",
		"Cards": []store.CardRequest{ready}, "AgentName": "Ibadan Agent",
		"Error": "", "Success": "XG-CARD-ABC12345",
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "agent_cards.html", data); err != nil {
		t.Fatalf("execute agent card page: %v", err)
	}
	page := buf.String()
	for _, want := range []string{"Record handover", "name=\"code\"", "name=\"id_type\"", "XG-CARD-ABC12345", "Ada Obi"} {
		if !strings.Contains(page, want) {
			t.Errorf("agent card page is missing %q", want)
		}
	}
	if strings.Contains(page, "id_number") {
		t.Error("the handover form must never ask for an identity-document number")
	}
}
