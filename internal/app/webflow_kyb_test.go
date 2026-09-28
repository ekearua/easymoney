package app

import (
	"html/template"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestWFCorporateNumberAndTINExtraction pins the OCR fallback: an uploaded
// certificate can supply a number the customer could not type, and the shape
// rules must not pick up unrelated digits from the reading.
func TestWFCorporateNumberAndTINExtraction(t *testing.T) {
	t.Parallel()
	cacCases := map[string]string{
		"RC1234567 ACME LIMITED":                      "1234567",
		"Company registration number: CAC7654321":     "7654321",
		"incorporated on 14/03/2019 under RC 998877":  "998877",
		"no registration number printed on this page": "",
	}
	for input, want := range cacCases {
		if got := wfCorporateNumberFrom(input); got != want {
			t.Errorf("wfCorporateNumberFrom(%q) = %q, want %q", input, got, want)
		}
	}
	tinCases := map[string]string{
		"TIN 1234567890 ACME LIMITED":             "1234567890",
		"Tax Identification Number: 123-456-7890": "1234567890",
		"no taxpayer number here":                 "",
	}
	for input, want := range tinCases {
		if got := wfTINFromDocument(input); got != want {
			t.Errorf("wfTINFromDocument(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestWFKYBDocumentsFromPayload pins that a typed value always wins over an OCR
// reading, and that the reading is kept as the document behind the claim
// either way.
func TestWFKYBDocumentsFromPayload(t *testing.T) {
	t.Parallel()
	var a App
	payload := map[string]string{
		"cac_reg_number":    "RC1111111",
		"cac_document":      "RC9999999 ACME LIMITED",
		"tin_document":      "TIN 1234567890 ACME LIMITED",
		"beneficial_owners": "Ada Obi 60%",
		"business_address":  "12 Marina Road, Lagos",
		"scuml_number":      "SC123456",
		"dnfbp":             "yes",
		"address_document":  "Meter 5551234, 12 Marina Road, Lagos",
	}
	docs := a.wfKYBDocumentsFromPayload(payload)
	if docs.CACRegNumber != "RC1111111" {
		t.Errorf("typed CAC must win over the reading, got %q", docs.CACRegNumber)
	}
	if docs.TIN != "1234567890" {
		t.Errorf("TIN should come from the certificate reading, got %q", docs.TIN)
	}
	if docs.CACDocument != "RC9999999 ACME LIMITED" || docs.AddressDocument == "" {
		t.Errorf("the uploaded readings must be kept: %+v", docs)
	}
	if !docs.DNFBP {
		t.Error("a yes answer must set DNFBP")
	}
	// Every field the merchant supplied justifies exactly one key, and the
	// derivation never invents one the merchant did not supply.
	if got := len(store.DeriveKYBEvidence(docs)); got != 5 {
		t.Fatalf("derived %d evidence keys, want 5", got)
	}
}

// TestWFKYBDocumentsFromEmptyPayload pins the honest direction: a payload with
// nothing in it justifies nothing, so a bare request can never claim a tier.
func TestWFKYBDocumentsFromEmptyPayload(t *testing.T) {
	t.Parallel()
	var a App
	docs := a.wfKYBDocumentsFromPayload(map[string]string{})
	if got := store.DeriveKYBEvidence(docs); len(got) != 0 {
		t.Fatalf("an empty payload justified %v, want nothing", got)
	}
	if summary := wfDocumentSummary(docs); summary != "none" {
		t.Fatalf("summary = %q, want none", summary)
	}
}

// TestWFKYBPreviousStep pins the Back table: every step after the first has a
// predecessor, and the first step has none (so Back cannot walk off the chain).
func TestWFKYBPreviousStep(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"corporate":  "merchant",
		"owners":     "corporate",
		"location":   "owners",
		"compliance": "location",
		"note":       "compliance",
		"review":     "note",
	}
	for step, prev := range want {
		got, ok := wfKYBPreviousStep(step)
		if !ok || got != prev {
			t.Errorf("wfKYBPreviousStep(%q) = %q, %v; want %q", step, got, ok, prev)
		}
	}
	if _, ok := wfKYBPreviousStep("merchant"); ok {
		t.Error("the first step must have no predecessor")
	}
}

// TestKYBEvidenceLabelsCoverTheHardGate keeps the customer-facing and console
// labels from drifting off the keys the gate actually reads.
func TestKYBEvidenceLabelsCoverTheHardGate(t *testing.T) {
	t.Parallel()
	for _, key := range store.BusinessEvidenceRequiredFor(kyc.TierB1) {
		if label := kybEvidenceLabel(key); label == "" || label == key {
			t.Errorf("evidence key %q has no label", key)
		}
	}
}

// TestWebFlowLocationStepRendersConfirmation guards the location step's whole
// point: the button, the form it posts to, the hidden coordinate fields and the
// script that fills them all have to be served, and the field must not leak
// into the main form as a submittable input.
func TestWebFlowLocationStepRendersConfirmation(t *testing.T) {
	t.Parallel()
	tmpl, err := template.New("").ParseFS(web.Assets, "templates/webflow.html")
	if err != nil {
		t.Fatalf("parse webflow template: %v", err)
	}
	token := strings.Repeat("a", 32)
	page := webFlowPage{
		AppName:  "Xego",
		FlowType: service.WebFlowKYBRequest,
		Token:    token,
		Title:    "Operating address and location",
		Intro:    "Upload a recent utility bill, then confirm where you are.",
		Shell:    true,
		Fields: []webFlowField{
			{Name: "address_document", Label: "Utility bill for the operating address", Type: "upload"},
			{Name: "location", Label: "Where you are now", Type: "location", Hint: "Your browser asks for permission once."},
		},
		Actions: []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}},
		Steps: []webFlowStepLabel{
			{Label: "Merchant", State: "done"}, {Label: "Corporate", State: "done"},
			{Label: "Owners", State: "done"}, {Label: "Location", State: "current", Dot: "4"},
			{Label: "Compliance"}, {Label: "Note"}, {Label: "Review"},
		},
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "webflow.html", page); err != nil {
		t.Fatalf("execute webflow template: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		`action="/w/` + token + `/location"`,
		"data-location-form",
		"data-location>",
		"Confirm my current location",
		"/static/webflow-location.js",
		`name="latitude"`,
		`name="longitude"`,
		`name="accuracy_m"`,
		`action="/w/` + token + `/media"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("location step is missing %q", want)
		}
	}
	if strings.Contains(body, `name="location"`) {
		t.Error("the location field must not render as a submittable input")
	}
}
