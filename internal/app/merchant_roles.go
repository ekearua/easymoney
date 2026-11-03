package app

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
)

const (
	merchantRoleContextKey        contextKey = "merchant_role"
	merchantMemberIDContextKey    contextKey = "merchant_member_id"
	merchantMemberEmailContextKey contextKey = "merchant_member_email"
)

func merchantRoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(merchantRoleContextKey).(string)
	if role == "" {
		return store.MerchantRoleOwner
	}
	return role
}

func merchantMemberIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(merchantMemberIDContextKey).(uuid.UUID)
	return id
}

func merchantMemberEmailFromContext(ctx context.Context) string {
	email, _ := ctx.Value(merchantMemberEmailContextKey).(string)
	return email
}

// requireMerchantRole gates a merchant route to the owner or to the listed
// staff roles. The owner always passes.
func (a *App) requireMerchantRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := merchantRoleFromContext(r.Context())
			if role != store.MerchantRoleOwner && !allowed[role] {
				http.Error(w, "you do not have access to this section", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireMerchantOwner gates a merchant route to the owner login only.
func (a *App) requireMerchantOwner() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if merchantRoleFromContext(r.Context()) != store.MerchantRoleOwner {
				http.Error(w, "only the merchant owner can do this", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// completeMerchantMemberLogin issues a session that carries the staff role.
func (a *App) completeMerchantMemberLogin(w http.ResponseWriter, r *http.Request, member store.MerchantMember) {
	// A disabled merchant disables every staff seat too: the owner account and
	// its members share one workspace gate.
	if active, _, err := a.store.MerchantAccess(r.Context(), member.MerchantID); err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	} else if !active {
		a.renderStatus(w, "merchant_login.html", map[string]any{
			"AppName": a.cfg.AppName,
			"Error":   "This merchant account has been disabled. Contact support.",
		}, http.StatusForbidden)
		return
	}
	ttl := a.cfg.AuthSessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	ownerUserID, err := a.store.MerchantOwnerID(r.Context(), member.MerchantID)
	if err != nil {
		ownerUserID = uuid.Nil
	}
	if err := a.store.CreateMerchantMemberSession(r.Context(), member.MerchantID, member.ID, ownerUserID,
		member.Role, token, csrf, time.Now().Add(ttl)); err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	a.store.TouchMerchantMemberLogin(r.Context(), member.ID)
	http.SetCookie(w, &http.Cookie{
		Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode,
		MaxAge: int(ttl.Seconds()),
	})
	target := "/merchant/records"
	if member.Role == store.MerchantRolePayments {
		target = "/merchant/payables"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// merchantTeam renders the owner's staff management page.
func (a *App) merchantTeam(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	members, err := a.store.ListMerchantMembers(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	settings, err := a.store.MerchantBulkSettings(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	inviteToken := strings.TrimSpace(r.URL.Query().Get("invited"))
	inviteLink := ""
	if inviteToken != "" {
		inviteLink = strings.TrimRight(a.cfg.BaseURL, "/") + "/merchant/invite?token=" + inviteToken
	}
	a.renderMerchant(w, "merchant_team.html", r, "Team", map[string]any{
		"Members": members, "Settings": settings,
		"InviteLink": inviteLink, "Error": r.URL.Query().Get("error"),
		"Role": merchantRoleFromContext(r.Context()),
	})
}

// merchantTeamInvite creates an invited staff account and surfaces its link.
func (a *App) merchantTeamInvite(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := strings.TrimSpace(r.FormValue("role"))
	if role != store.MerchantRoleRecords && role != store.MerchantRolePayments {
		http.Redirect(w, r, "/merchant/team?error=Choose+a+valid+role", http.StatusSeeOther)
		return
	}
	token, err := randomToken(24)
	if err != nil {
		http.Error(w, "could not create invite", http.StatusInternalServerError)
		return
	}
	if _, err := a.store.CreateMerchantMember(r.Context(), merchantID, email, role,
		store.HashMemberInviteToken(token), time.Now().Add(7*24*time.Hour), true, nil); err != nil {
		a.logger.WarnContext(r.Context(), "invite merchant member failed", "error", err)
		http.Redirect(w, r, "/merchant/team?error=Could+not+invite+that+address", http.StatusSeeOther)
		return
	}
	a.audit(r, "merchant.team.member_invited", "merchant_member", email, map[string]any{"role": role})
	http.Redirect(w, r, "/merchant/team?invited="+token, http.StatusSeeOther)
}

// merchantTeamSetRole changes a staff member's role.
func (a *App) merchantTeamSetRole(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid member", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role != store.MerchantRoleRecords && role != store.MerchantRolePayments {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if err := a.store.SetMerchantMemberRole(r.Context(), merchantIDFromContext(r.Context()), memberID, role); err != nil {
		http.Error(w, "could not update role", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.team.member_role_changed", "merchant_member", memberID.String(), map[string]any{"role": role})
	http.Redirect(w, r, "/merchant/team", http.StatusSeeOther)
}

// merchantTeamRevoke removes a staff member and their sessions.
func (a *App) merchantTeamRevoke(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid member", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteMerchantMember(r.Context(), merchantIDFromContext(r.Context()), memberID); err != nil {
		http.Error(w, "could not revoke access", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.team.member_revoked", "merchant_member", memberID.String(), nil)
	http.Redirect(w, r, "/merchant/team", http.StatusSeeOther)
}

// merchantInvitePage renders the accept-invite form.
func (a *App) merchantInvitePage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	member, err := a.store.MerchantMemberByInviteToken(r.Context(), token)
	if err != nil {
		a.renderStatus(w, "merchant_invite.html", map[string]any{
			"AppName": a.cfg.AppName, "Invalid": true,
		}, http.StatusNotFound)
		return
	}
	a.render(w, "merchant_invite.html", map[string]any{
		"AppName": a.cfg.AppName, "Token": token, "Email": member.Email,
		"Role": member.Role, "MerchantName": member.MerchantName,
	})
}

// merchantInviteAccept sets the password and activates the staff account.
func (a *App) merchantInviteAccept(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	member, err := a.store.MerchantMemberByInviteToken(r.Context(), token)
	if err != nil {
		a.renderStatus(w, "merchant_invite.html", map[string]any{
			"AppName": a.cfg.AppName, "Invalid": true,
		}, http.StatusNotFound)
		return
	}
	if len(password) < 8 || password != confirm {
		a.renderStatus(w, "merchant_invite.html", map[string]any{
			"AppName": a.cfg.AppName, "Token": token, "Email": member.Email,
			"Role": member.Role, "MerchantName": member.MerchantName,
			"Error": "Use at least 8 characters and make both passwords match.",
		}, http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "could not set password", http.StatusInternalServerError)
		return
	}
	if err := a.store.ActivateMerchantMember(r.Context(), member.ID, string(hash)); err != nil {
		http.Error(w, "could not set password", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.team.invite_accepted", "merchant_member", member.ID.String(), map[string]any{"role": member.Role})
	http.Redirect(w, r, "/merchant/login", http.StatusSeeOther)
}
