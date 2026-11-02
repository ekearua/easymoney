package app

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminSetMerchantBulkSettings updates the three bulk switches for a merchant.
func (a *App) adminSetMerchantBulkSettings(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("bulk_enabled") == "on"
	locationRequired := r.FormValue("bulk_location_required") == "on"
	payoutMode := strings.TrimSpace(r.FormValue("bulk_payout_mode"))
	if payoutMode != "wallet" && payoutMode != "bank" {
		payoutMode = "wallet"
	}
	if err := a.store.SetMerchantBulkSettings(r.Context(), id, enabled, locationRequired, payoutMode); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.bulk_settings_updated", "merchant", id.String(), map[string]any{
		"bulk_enabled": enabled, "location_required": locationRequired, "payout_mode": payoutMode,
	})
	http.Redirect(w, r, "/admin/merchants?bulk_set=1", http.StatusSeeOther)
}


// adminInviteMerchantMember creates a staff invite for a merchant.
func (a *App) adminInviteMerchantMember(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := strings.TrimSpace(r.FormValue("role"))
	if role != store.MerchantRoleRecords && role != store.MerchantRolePayments {
		http.Redirect(w, r, "/admin/merchants?error="+url.QueryEscape("Choose a valid role"), http.StatusSeeOther)
		return
	}
	token, err := randomToken(24)
	if err != nil {
		http.Error(w, "could not create invite", http.StatusInternalServerError)
		return
	}
	if _, err := a.store.CreateMerchantMember(r.Context(), merchantID, email, role,
		store.HashMemberInviteToken(token), time.Now().Add(7*24*time.Hour), false, nil); err != nil {
		a.logger.WarnContext(r.Context(), "admin invite merchant member failed", "error", err)
		http.Redirect(w, r, "/admin/merchants?error="+url.QueryEscape("Could not invite that address"), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.merchants.member_invited", "merchant_member", merchantID.String(), map[string]any{
		"email": email, "role": role,
	})
	http.Redirect(w, r, "/admin/merchants?invite="+url.QueryEscape(token), http.StatusSeeOther)
}

// adminSetMerchantMemberRole changes a staff member's role.
func (a *App) adminSetMerchantMemberRole(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "memberID"))
	if err != nil {
		http.Error(w, "invalid member", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role != store.MerchantRoleRecords && role != store.MerchantRolePayments {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if err := a.store.SetMerchantMemberRole(r.Context(), merchantID, memberID, role); err != nil {
		http.Error(w, "could not update role", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.member_role_changed", "merchant_member", memberID.String(), map[string]any{"role": role})
	http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
}

// adminRevokeMerchantMember removes a staff member and ends their sessions.
func (a *App) adminRevokeMerchantMember(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "memberID"))
	if err != nil {
		http.Error(w, "invalid member", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteMerchantMember(r.Context(), merchantID, memberID); err != nil {
		http.Error(w, "could not revoke access", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.member_revoked", "merchant_member", memberID.String(), nil)
	http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
}
