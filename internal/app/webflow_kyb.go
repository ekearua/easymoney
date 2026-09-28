package app

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// KYB document collection helpers. The web flow is the canonical surface for a
// merchant's corporate evidence; everything here exists so the evidence a
// request carries is derived from the documents actually submitted, and never
// from the tier's requirement list.

var (
	// wfCorporateNumberRe finds a CAC/RC registration number in a typed value
	// or in the reading of an uploaded certificate. The RC/CAC prefix is
	// optional because a customer may copy either form off the document.
	wfCorporateNumberRe = regexp.MustCompile(`(?i)\b(?:RC|CAC)?\s*([0-9]{6,10})\b`)
	// wfTINRe finds a taxpayer number: 10-13 digits, optionally spaced or
	// hyphenated on the certificate.
	wfTINRe = regexp.MustCompile(`\b([0-9][0-9\-\s]{8,16}[0-9])\b`)
)

// wfKYBDocumentsFromPayload assembles the store record from a KYB flow's
// collected payload. A typed value always wins over an OCR reading; the
// reading is kept as the document that backs the claim either way.
func (a *App) wfKYBDocumentsFromPayload(payload map[string]string) store.KYBDocuments {
	cacDoc := strings.TrimSpace(payload["cac_document"])
	tinDoc := strings.TrimSpace(payload["tin_document"])
	return store.KYBDocuments{
		CACRegNumber:     wfFirstNonEmpty(payload["cac_reg_number"], wfCorporateNumberFrom(cacDoc)),
		TIN:              wfFirstNonEmpty(payload["tin"], wfTINFromDocument(tinDoc)),
		BeneficialOwners: strings.TrimSpace(payload["beneficial_owners"]),
		BusinessAddress:  strings.TrimSpace(payload["business_address"]),
		SCUMLNumber:      strings.TrimSpace(payload["scuml_number"]),
		DNFBP:            strings.EqualFold(strings.TrimSpace(payload["dnfbp"]), "yes"),
		CACDocument:      cacDoc,
		TINDocument:      tinDoc,
		AddressDocument:  strings.TrimSpace(payload["address_document"]),
	}
}

// wfCorporateNumberFrom returns the first CAC/RC registration number in text
// (a typed value or an OCR reading), or "".
func wfCorporateNumberFrom(text string) string {
	m := wfCorporateNumberRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

// wfTINFromDocument returns the first 10-13 digit taxpayer number in text,
// with separators removed, or "".
func wfTINFromDocument(text string) string {
	for _, m := range wfTINRe.FindAllString(text, -1) {
		digits := wfDigitsOnly(m)
		if len(digits) >= 10 && len(digits) <= 13 {
			return digits
		}
	}
	return ""
}

// wfDigitsOnly strips every non-digit from a value. (The package already has a
// digitsOnly predicate; this one extracts.)
func wfDigitsOnly(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wfKYBPreviousStep is the step a Back action returns to. The KYB chain is
// linear, so one table covers it; the first step has no predecessor.
func wfKYBPreviousStep(step string) (string, bool) {
	prev, ok := map[string]string{
		"corporate":  "merchant",
		"owners":     "corporate",
		"location":   "owners",
		"compliance": "location",
		"note":       "compliance",
		"review":     "note",
	}[step]
	return prev, ok
}

// wfDocumentSummary names the corporate documents the merchant attached, so the
// review step shows what will back the request.
func wfDocumentSummary(d store.KYBDocuments) string {
	var labels []string
	for _, key := range store.BusinessEvidenceRequiredFor(kyc.TierB1) {
		if strings.TrimSpace(d.DocumentForEvidence(key)) != "" {
			labels = append(labels, kybEvidenceLabel(key))
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return fmt.Sprintf("%s (%d of 5)", strings.Join(labels, ", "), len(labels))
}

// kybEvidenceLabel is the name of a corporate evidence key, shared by the
// customer-facing review step and the compliance console.
func kybEvidenceLabel(key string) string {
	switch key {
	case kyc.EvCACVerified:
		return "CAC certificate"
	case kyc.EvTINVerified:
		return "TIN certificate"
	case kyc.EvUBOCleared:
		return "beneficial owners"
	case kyc.EvLocationVerified:
		return "operating address"
	case kyc.EvSCUMLSubmitted:
		return "SCUML number"
	}
	return key
}

// wfLocationStatusLabel renders a verification status for the customer.
func wfLocationStatusLabel(status string) string {
	switch status {
	case ports.VerifyMatched:
		return "confirmed"
	case ports.VerifyReview:
		return "recorded, needs review"
	case ports.VerifyNoLocation:
		return "not captured"
	}
	return "recorded"
}

// locationProviderName reports the configured location verifier's identifier
// for audit records, falling back to "unknown".
func (a *App) locationProviderName() string {
	type named interface{ ProviderName() string }
	if n, ok := a.locationVerifier.(named); ok {
		return n.ProviderName()
	}
	return "unknown"
}

// corporateRegistryProviderName reports the configured corporate-registry
// verifier's identifier for audit records.
func (a *App) corporateRegistryProviderName() string {
	type named interface{ ProviderName() string }
	if n, ok := a.corporateRegistry.(named); ok {
		return n.ProviderName()
	}
	return "unknown"
}

// taxIDProviderName reports the configured tax-authority verifier's identifier
// for audit records.
func (a *App) taxIDProviderName() string {
	type named interface{ ProviderName() string }
	if n, ok := a.taxIDVerifier.(named); ok {
		return n.ProviderName()
	}
	return "unknown"
}

// webFlowLocation records the position the customer's browser reported, with
// the visitor's permission, for a location-verification step, compares it with
// the address the flow declared, and stores the outcome. It never advances a
// tier or marks anything verified: the result is a proposal the compliance
// reviewer reads alongside the documents.
func (a *App) webFlowLocation(w http.ResponseWriter, r *http.Request) {
	flow, user, ok := a.webFlowContext(w, r)
	if !ok {
		return
	}
	if flow.Status != store.WebFlowOpen {
		http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	lat, latErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("latitude")), 64)
	lng, lngErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("longitude")), 64)
	accuracy, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("accuracy_m")), 64)
	if latErr != nil || lngErr != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 || (lat == 0 && lng == 0) {
		a.wfLocationError(w, r, flow, user, "We didn't get a usable position. Allow location access and try again.")
		return
	}
	merchant, err := a.store.MerchantBySlug(r.Context(), flow.Payload["merchant_slug"])
	if err != nil {
		a.wfLocationError(w, r, flow, user, "That merchant is no longer available.")
		return
	}
	result, err := a.locationVerifier.VerifyLocation(r.Context(), ports.LocationRequest{
		Address:        flow.Payload["business_address"],
		UtilityBillRef: flow.Payload["address_document"],
		Latitude:       lat,
		Longitude:      lng,
		AccuracyMeters: accuracy,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "location verification failed", "flow", flow.ID, "error", err)
		a.wfLocationError(w, r, flow, user, "We couldn't check that location just now. Try again in a moment.")
		return
	}
	provider := a.locationProviderName()
	if err := a.store.RecordKYBLocation(r.Context(), merchant.ID, lat, lng, accuracy, result.Status, provider); err != nil {
		a.logger.WarnContext(r.Context(), "record kyb location failed", "flow", flow.ID, "error", err)
		a.wfLocationError(w, r, flow, user, "We couldn't save that location. Try again.")
		return
	}
	if err := a.store.SaveKYBVerification(r.Context(), merchant.ID, kyc.EvLocationVerified, store.KYBDocumentVerification{
		Status:      result.Status,
		Provider:    provider,
		ProviderRef: result.ProviderRef,
		MatchName:   result.Status == ports.VerifyMatched,
		Message:     result.Message,
	}); err != nil {
		a.logger.WarnContext(r.Context(), "save location verification failed", "flow", flow.ID, "error", err)
	}
	payload := clonePayload(flow.Payload)
	payload["location_confirmed"] = "1"
	payload["location_summary"] = fmt.Sprintf("%.4f, %.4f (±%.0fm) · %s", lat, lng, accuracy, wfLocationStatusLabel(result.Status))
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, flow.Step, payload); err != nil {
		a.logger.WarnContext(r.Context(), "save web flow location failed", "flow", flow.ID, "error", err)
		http.Error(w, "Could not save that location. Go back and try again.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
}

// wfLocationError re-renders the current step with the failure message so the
// customer can retry without leaving the flow.
func (a *App) wfLocationError(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, msg string) {
	page, err := a.wfRenderStep(r, flow, user)
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow location error render failed", "flow", flow.ID, "error", err)
		http.Error(w, "Something went wrong. Go back to WhatsApp and tap the link again.", http.StatusInternalServerError)
		return
	}
	a.wfPrefillMediaValues(&page, flow)
	a.wfDecorate(&page, flow)
	page.Error = msg
	a.renderStatus(w, "webflow.html", page, http.StatusUnprocessableEntity)
}

// wfFirstNonEmpty returns the first trimmed non-empty value.
func wfFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
