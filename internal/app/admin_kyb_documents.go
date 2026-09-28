package app

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// KYB document review. The corporate hard gate accepts a B0 -> B1 evidence key
// only when a reviewer has verified the document behind it, so these are the
// actions that turn a merchant's claim into evidence. Every one of them is
// recorded against the operator's identity.

// kybDocumentViews pairs each corporate evidence key with the document the
// merchant submitted for it, the reviewer's disposition, and any third-party
// proposal. A key with no document is shown too: the reviewer needs to see
// what is missing as much as what is present.
func kybDocumentViews(d store.KYBDocuments) []kybDocumentView {
	out := make([]kybDocumentView, 0, 5)
	for _, key := range store.BusinessEvidenceRequiredFor(kyc.TierB1) {
		view := kybDocumentView{Key: key, Label: kybEvidenceLabel(key)}
		if doc := strings.TrimSpace(d.DocumentForEvidence(key)); doc != "" {
			view.Document = truncateRunes(doc, 240)
		}
		if review, ok := d.Reviews[key]; ok {
			view.Status = review.Status
			view.Reviewer = review.Reviewer
			view.Note = review.Note
			if !review.ReviewedAt.IsZero() {
				view.ReviewedAt = review.ReviewedAt.UTC().Format(time.RFC3339)
			}
		}
		if v, ok := d.Verifications[key]; ok {
			view.Verification = fmt.Sprintf("%s · %s: %s", v.Provider, v.Status, v.Message)
		}
		out = append(out, view)
	}
	return out
}

// kybLocationText renders the captured position for the console.
func kybLocationText(d store.KYBDocuments) string {
	if d.LocationLat == nil || d.LocationLng == nil {
		return ""
	}
	text := fmt.Sprintf("%.4f, %.4f", *d.LocationLat, *d.LocationLng)
	if d.LocationAccuracyM != nil {
		text += fmt.Sprintf(" (±%.0fm)", *d.LocationAccuracyM)
	}
	if d.LocationCapturedAt != nil {
		text += " · " + d.LocationCapturedAt.UTC().Format(time.RFC3339)
	}
	return text
}

// adminKYBDocumentReview records the reviewer's disposition on one submitted
// corporate document. Rejecting a document also removes it from the verified
// set the hard gate reads, so a rejection is as consequential as a verification.
func (a *App) adminKYBDocumentReview(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.FormValue("evidence"))
	status := store.KYBDocumentRejected
	if strings.TrimSpace(r.FormValue("decision")) == "verify" {
		status = store.KYBDocumentVerified
	}
	reviewer := adminEmailFromContext(r.Context())
	if _, err := a.store.ReviewKYBDocument(r.Context(), merchantID, key, status, reviewer, strings.TrimSpace(r.FormValue("note"))); err != nil {
		a.logger.WarnContext(r.Context(), "kyb document review failed", "merchant_id", merchantID, "evidence", key, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.kyb.document.reviewed", "merchant", merchantID.String(), map[string]any{
		"evidence": key,
		"status":   status,
	})
	a.logger.InfoContext(r.Context(), "kyb document reviewed", "merchant_id", merchantID, "evidence", key, "status", status)
	http.Redirect(w, r, "/admin/kyb?saved=document-"+status, http.StatusSeeOther)
}

// adminKYBCheckRegistry runs the corporate-registry and tax-authority checks
// against the merchant's submitted numbers and stores the proposals. It never
// marks anything verified: it exists so a reviewer's disposition is informed by
// what the register says rather than by the merchant's own claim.
func (a *App) adminKYBCheckRegistry(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("could not load that merchant"), http.StatusSeeOther)
		return
	}
	docs, err := a.store.KYBDocumentsByMerchant(r.Context(), merchantID)
	if err != nil {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("that merchant has submitted no documents"), http.StatusSeeOther)
		return
	}
	checked := 0
	if number := strings.TrimSpace(docs.CACRegNumber); number != "" {
		if a.recordCorporateCheck(r, merchantID, kyc.EvCACVerified, number, merchant.Name) {
			checked++
		}
	}
	if tin := strings.TrimSpace(docs.TIN); tin != "" {
		if a.recordTaxCheck(r, merchantID, kyc.EvTINVerified, tin, merchant.Name) {
			checked++
		}
	}
	if checked == 0 {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("no registration number or TIN is on file to check"), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.kyb.documents.registry_checked", "merchant", merchantID.String(), map[string]any{"checked": checked})
	a.logger.InfoContext(r.Context(), "kyb registry check run", "merchant_id", merchantID, "checked", checked)
	http.Redirect(w, r, "/admin/kyb?saved=registry-checked", http.StatusSeeOther)
}

// adminKYBCheckLocation re-runs the operating-address comparison against the
// position the merchant's browser reported, and stores the proposal.
func (a *App) adminKYBCheckLocation(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	docs, err := a.store.KYBDocumentsByMerchant(r.Context(), merchantID)
	if err != nil {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("that merchant has submitted no documents"), http.StatusSeeOther)
		return
	}
	if docs.LocationLat == nil || docs.LocationLng == nil {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("that merchant has not reported a position"), http.StatusSeeOther)
		return
	}
	accuracy := 0.0
	if docs.LocationAccuracyM != nil {
		accuracy = *docs.LocationAccuracyM
	}
	provider := a.locationProviderName()
	proposal := store.KYBDocumentVerification{Provider: provider}
	result, err := a.locationVerifier.VerifyLocation(r.Context(), ports.LocationRequest{
		Address:        docs.BusinessAddress,
		UtilityBillRef: docs.AddressDocument,
		Latitude:       *docs.LocationLat,
		Longitude:      *docs.LocationLng,
		AccuracyMeters: accuracy,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "location check failed", "merchant_id", merchantID, "error", err)
		proposal.Status = "error"
		proposal.Message = "the location provider could not be reached: " + err.Error()
	} else {
		proposal.Status = result.Status
		proposal.ProviderRef = result.ProviderRef
		proposal.MatchName = result.Status == ports.VerifyMatched
		proposal.Message = result.Message
	}
	if saveErr := a.store.SaveKYBVerification(r.Context(), merchantID, kyc.EvLocationVerified, proposal); saveErr != nil {
		a.logger.WarnContext(r.Context(), "save location verification failed", "merchant_id", merchantID, "error", saveErr)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("could not save the location check"), http.StatusSeeOther)
		return
	}
	// Keep the stored status in step with the proposal, so the merchant row and
	// the document panel never disagree about what the last check said.
	if err == nil {
		if locErr := a.store.RecordKYBLocation(r.Context(), merchantID, *docs.LocationLat, *docs.LocationLng, accuracy, result.Status, provider); locErr != nil {
			a.logger.WarnContext(r.Context(), "update location status failed", "merchant_id", merchantID, "error", locErr)
		}
	}
	a.audit(r, "admin.kyb.documents.location_checked", "merchant", merchantID.String(), map[string]any{"status": proposal.Status})
	a.logger.InfoContext(r.Context(), "kyb location check run", "merchant_id", merchantID, "status", proposal.Status)
	http.Redirect(w, r, "/admin/kyb?saved=location-checked", http.StatusSeeOther)
}

// recordCorporateCheck runs one registry lookup and stores the proposal,
// reporting whether the lookup itself completed.
func (a *App) recordCorporateCheck(r *http.Request, merchantID uuid.UUID, key, number, businessName string) bool {
	provider := a.corporateRegistryProviderName()
	proposal := store.KYBDocumentVerification{Provider: provider}
	result, err := a.corporateRegistry.VerifyCorporate(r.Context(), ports.CorporateRegistryRequest{
		RegistrationNumber: number,
		BusinessName:       businessName,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "corporate registry check failed", "merchant_id", merchantID, "error", err)
		proposal.Status = "error"
		proposal.Message = "the registry could not be reached: " + err.Error()
	} else {
		proposal.Status = result.Status
		proposal.ProviderRef = result.ProviderRef
		proposal.MatchName = result.MatchName
		proposal.Message = result.Message
	}
	if saveErr := a.store.SaveKYBVerification(r.Context(), merchantID, key, proposal); saveErr != nil {
		a.logger.WarnContext(r.Context(), "save corporate verification failed", "merchant_id", merchantID, "evidence", key, "error", saveErr)
		return false
	}
	return err == nil
}

// recordTaxCheck runs one tax-authority lookup and stores the proposal.
func (a *App) recordTaxCheck(r *http.Request, merchantID uuid.UUID, key, tin, businessName string) bool {
	provider := a.taxIDProviderName()
	proposal := store.KYBDocumentVerification{Provider: provider}
	result, err := a.taxIDVerifier.VerifyTaxID(r.Context(), ports.TaxIDRequest{
		TIN:          tin,
		BusinessName: businessName,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "tax authority check failed", "merchant_id", merchantID, "error", err)
		proposal.Status = "error"
		proposal.Message = "the tax authority could not be reached: " + err.Error()
	} else {
		proposal.Status = result.Status
		proposal.ProviderRef = result.ProviderRef
		proposal.MatchName = result.MatchName
		proposal.Message = result.Message
	}
	if saveErr := a.store.SaveKYBVerification(r.Context(), merchantID, key, proposal); saveErr != nil {
		a.logger.WarnContext(r.Context(), "save tax verification failed", "merchant_id", merchantID, "evidence", key, "error", saveErr)
		return false
	}
	return err == nil
}
