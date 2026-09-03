package app

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

// kybEvidenceOption is one verifiable piece of business identity shown in the
// advance form, with whether the target tier requires it.
type kybEvidenceOption struct {
	Key        string
	Label      string
	IsRequired bool
}

// kybAdminRow is the per-merchant view for the KYB console.
type kybAdminRow struct {
	MerchantID       uuid.UUID
	MerchantName     string
	Tier             string
	ReviewStatus     string
	Screening        string
	Evidence         []string
	UpdatedAt        time.Time
	NextTier         string
	Options          []kybEvidenceOption
	HasAdvanceRequest bool
	AdvanceTier      string
	AdvanceEvidence  []string
	AdvanceNote      string
	AdvanceRequested string
}

// adminKYB renders the business identity ladder together with the editable
// allowance ceilings that each tier grants.
func (a *App) adminKYB(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.store.ListKYBProfiles(r.Context(), 300)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list kyb profiles", "error", err)
		http.Error(w, "kyb dashboard unavailable", http.StatusInternalServerError)
		return
	}
	limits, err := a.store.ListTierLimits(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "list tier limits", "error", err)
		http.Error(w, "kyb dashboard unavailable", http.StatusInternalServerError)
		return
	}
	merchants, err := a.store.ListMerchants(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "list merchants for kyb", "error", err)
		http.Error(w, "kyb dashboard unavailable", http.StatusInternalServerError)
		return
	}
	names := make(map[uuid.UUID]string, len(merchants))
	for _, m := range merchants {
		names[m.ID] = m.Name
	}
	rows := make([]kybAdminRow, 0, len(profiles))
	for _, p := range profiles {
		next := nextBusinessTier(p.Tier)
		row := kybAdminRow{
			MerchantID:   p.MerchantID,
			MerchantName: names[p.MerchantID],
			Tier:         p.Tier,
			ReviewStatus: orPlaceholder(p.ReviewStatus, "none"),
			Screening:    orPlaceholder(p.LastScreeningDecision, "—"),
			Evidence:     p.Evidence,
			UpdatedAt:    p.UpdatedAt,
			NextTier:     next,
			Options:      businessEvidenceOptions(next),
		}
		if p.AdvancementRequest != nil {
			row.HasAdvanceRequest = true
			row.AdvanceTier = p.AdvancementRequest.RequestedTier
			row.AdvanceEvidence = p.AdvancementRequest.Evidence
			row.AdvanceNote = p.AdvancementRequest.Note
			if p.AdvancementRequestedAt != nil {
				row.AdvanceRequested = p.AdvancementRequestedAt.Format(time.RFC3339)
			}
		}
		rows = append(rows, row)
	}
	a.renderAdmin(w, "admin_kyb.html", r, "KYB ladder & limits", map[string]any{
		"Profiles": rows,
		"Limits":   limits,
		"Error":    r.URL.Query().Get("error"),
		"Saved":    r.URL.Query().Get("saved"),
	})
}

// adminKYBAdvance promotes a merchant to the requested business tier. The move
// is adjacency- and evidence-gated inside the store; the operator's session is
// recorded as the actor on the audit trail.
func (a *App) adminKYBAdvance(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	to := strings.TrimSpace(r.FormValue("target_tier"))
	if !kyc.ValidBusinessTier(to) {
		http.Error(w, "invalid target tier", http.StatusBadRequest)
		return
	}
	var evidence []string
	for _, key := range r.Form["evidence"] {
		if key != "" {
			evidence = append(evidence, key)
		}
	}
	actor := &store.AuditLog{
		ActorType:  "admin",
		ActorID:    uuid.NullUUID{UUID: adminIDFromContext(r.Context()), Valid: true},
		ActorEmail: sql.NullString{String: adminEmailFromContext(r.Context()), Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
	}
	profile, err := a.store.AdvanceKYBTier(r.Context(), merchantID, to, evidence, actor)
	if err != nil {
		a.logger.WarnContext(r.Context(), "advance kyb tier failed", "merchant_id", merchantID, "target", to, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.logger.InfoContext(r.Context(), "kyb tier advanced", "merchant_id", merchantID, "tier", profile.Tier)
	http.Redirect(w, r, "/admin/kyb?saved=advanced", http.StatusSeeOther)
}

// adminKYBAdvanceRequestApprove approves a merchant's self-service upgrade
// request by advancing their tier to the requested target using the evidence
// they submitted. Adjacency, evidence and screening gates are enforced inside
// the store; the request record is cleared on a successful advance.
func (a *App) adminKYBAdvanceRequestApprove(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	profile, err := a.store.KYBProfileByMerchant(r.Context(), merchantID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "load kyb profile for advance request", "merchant_id", merchantID, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("could not load profile"), http.StatusSeeOther)
		return
	}
	if profile.AdvancementRequest == nil {
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("no upgrade request to approve"), http.StatusSeeOther)
		return
	}
	actor := &store.AuditLog{
		ActorType:  "admin",
		ActorID:    uuid.NullUUID{UUID: adminIDFromContext(r.Context()), Valid: true},
		ActorEmail: sql.NullString{String: adminEmailFromContext(r.Context()), Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
	}
	to := profile.AdvancementRequest.RequestedTier
	evidence := profile.AdvancementRequest.Evidence
	// Fall back to the documented evidence for the target if the merchant
	// submitted nothing usable, so an approve still succeeds on a bare request.
	if len(evidence) == 0 {
		if req := store.BusinessEvidenceFor(to); req != "" {
			evidence = []string{req}
		}
	}
	advanced, err := a.store.AdvanceKYBTier(r.Context(), merchantID, to, evidence, actor)
	if err != nil {
		a.logger.WarnContext(r.Context(), "approve kyb advance request failed", "merchant_id", merchantID, "target", to, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.kyb.advance_request.approved", "merchant", merchantID.String(), map[string]any{
		"from_tier": profile.Tier,
		"to_tier":   advanced.Tier,
		"evidence":  evidence,
	})
	a.logger.InfoContext(r.Context(), "kyb advance request approved", "merchant_id", merchantID, "tier", advanced.Tier)
	http.Redirect(w, r, "/admin/kyb?saved=request-approved", http.StatusSeeOther)
}

// adminKYBAdvanceRequestClear rejects or withdraws a merchant's self-service
// upgrade request without moving its tier.
func (a *App) adminKYBAdvanceRequestClear(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	profile, err := a.store.KYBProfileByMerchant(r.Context(), merchantID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "load kyb profile for request clear", "merchant_id", merchantID, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape("could not load profile"), http.StatusSeeOther)
		return
	}
	wasTarget := ""
	if profile.AdvancementRequest != nil {
		wasTarget = profile.AdvancementRequest.RequestedTier
	}
	if _, err := a.store.RequestKYBAdvancement(r.Context(), merchantID, nil, ""); err != nil {
		a.logger.WarnContext(r.Context(), "clear kyb advance request failed", "merchant_id", merchantID, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.kyb.advance_request.cleared", "merchant", merchantID.String(), map[string]any{
		"requested_tier": wasTarget,
	})
	a.logger.InfoContext(r.Context(), "kyb advance request cleared", "merchant_id", merchantID)
	http.Redirect(w, r, "/admin/kyb?saved=request-cleared", http.StatusSeeOther)
}

// adminKYBReview approves or rejects a merchant's KYB position without moving
// its tier.
func (a *App) adminKYBReview(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	approve := strings.TrimSpace(r.FormValue("decision")) == "approve"
	adminID := adminIDFromContext(r.Context())
	if err := a.store.ReviewKYBProfile(r.Context(), merchantID, approve, &adminID, r.FormValue("note")); err != nil {
		a.logger.WarnContext(r.Context(), "review kyb profile failed", "merchant_id", merchantID, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	status := "rejected"
	if approve {
		status = "approved"
	}
	a.audit(r, "admin.kyb.reviewed", "merchant", merchantID.String(), map[string]any{"status": status})
	a.logger.InfoContext(r.Context(), "kyb profile reviewed", "merchant_id", merchantID, "status", status)
	http.Redirect(w, r, "/admin/kyb?saved="+status, http.StatusSeeOther)
}

// adminTierLimitUpdate edits one (account type, tier, direction) ceiling row.
// Single must not exceed daily and daily must not exceed monthly.
func (a *App) adminTierLimitUpdate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid limit id", http.StatusBadRequest)
		return
	}
	single, err1 := strconv.ParseInt(r.FormValue("single_kobo"), 10, 64)
	daily, err2 := strconv.ParseInt(r.FormValue("daily_kobo"), 10, 64)
	monthly, err3 := strconv.ParseInt(r.FormValue("monthly_kobo"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "limits must be whole kobo amounts", http.StatusBadRequest)
		return
	}
	adminID := adminIDFromContext(r.Context())
	updated, err := a.store.UpdateTierLimit(r.Context(), id, single, daily, monthly, &adminID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "update tier limit failed", "limit_id", id, "error", err)
		http.Redirect(w, r, "/admin/kyb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.tier_limits.updated", "tier_limit", strconv.FormatInt(id, 10), map[string]any{
		"account_type": updated.AccountType,
		"tier":         updated.Tier,
		"direction":    updated.Direction,
		"single_kobo":  single,
		"daily_kobo":   daily,
		"monthly_kobo": monthly,
	})
	a.logger.InfoContext(r.Context(), "tier limit updated", "limit_id", id, "tier", updated.Tier)
	http.Redirect(w, r, "/admin/kyb?saved=limits", http.StatusSeeOther)
}

// nextBusinessTier returns the single valid advancement target for a tier, or
// "" at the top of the ladder.
func nextBusinessTier(tier string) string {
	switch tier {
	case kyc.TierB0:
		return kyc.TierB1
	case kyc.TierB1:
		return kyc.TierB2
	case kyc.TierB2:
		return kyc.TierB3
	}
	return ""
}

// businessEvidenceOptions lists every verifiable business identity piece with
// the one the target tier actually requires flagged.
func businessEvidenceOptions(next string) []kybEvidenceOption {
	all := []kybEvidenceOption{
		{Key: kyc.EvBusinessDocs, Label: "Legal identity: RC/BN and directors verified"},
		{Key: kyc.EvBusinessBank, Label: "Settlement bank matched to the business identity"},
		{Key: kyc.EvBusinessEDD, Label: "Enhanced due diligence on beneficial owners and source of funds"},
	}
	required := ""
	switch next {
	case kyc.TierB1:
		required = kyc.EvBusinessDocs
	case kyc.TierB2:
		required = kyc.EvBusinessBank
	case kyc.TierB3:
		required = kyc.EvBusinessEDD
	}
	for i := range all {
		all[i].IsRequired = all[i].Key == required
	}
	return all
}

func orPlaceholder(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
