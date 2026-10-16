package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
)

// agentLoginPage renders the agent sign-in page.
func (a *App) agentLoginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "agent_login.html", map[string]any{"AppName": a.cfg.AppName})
}

// agentLoginPost authenticates an agent console identity by email or phone, then
// runs the shared second-factor step when a factor is enrolled (or the
// deployment requires one).
func (a *App) agentLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	login := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	user, err := a.store.AgentUserByLogin(r.Context(), login)
	if err != nil || user.Status != store.AgentUserActive || user.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, "agent_login.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid email or password."}, http.StatusUnauthorized)
		return
	}
	if a.secondFactorRequired(r.Context(), store.MFAScopeAgent, user.ID) {
		a.beginSecondFactor(w, r, store.MFAScopeAgent, user.ID, login)
		return
	}
	a.completeAgentLogin(w, r, user.ID)
}

// agentLogout clears the agent session and returns to the sign-in page.
func (a *App) agentLogout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != agentCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(agentCookieName); err == nil {
		_ = a.store.DeleteAgentSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: agentCookieName, Path: "/agent", MaxAge: -1, HttpOnly: true, Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/agent/login", http.StatusSeeOther)
}

// agentRedeemPage renders the cash-pickup redemption form and the agent's
// awaiting pickups.
func (a *App) agentRedeemPage(w http.ResponseWriter, r *http.Request) {
	a.renderAgent(w, "agent_redeem.html", r, "Cash pickup", a.agentRedeemData(r, ""))
}

// agentRedeemData assembles the redemption page payload. The pickups list is
// display-only; the code the operator types is re-resolved and locked by the
// store choke point at payout time.
func (a *App) agentRedeemData(r *http.Request, failure string) map[string]any {
	agentMerchantID := agentMerchantIDFromContext(r.Context())
	agentUserID := agentUserIDFromContext(r.Context())
	data := map[string]any{
		"Enabled":         a.cfg.AgentNetworkEnabled,
		"Error":           failure,
		"IDRequired":      a.cfg.AgentIDRequired,
		"StepUpRequired":  a.totpAvailable(),
		"StepUpAvailable": a.agentStepUpAvailable(r.Context(), agentUserID),
		"Pickups":         []store.AgentPickup{},
	}
	if a.cfg.AgentNetworkEnabled {
		if pickups, err := a.store.ListAgentPickups(r.Context(), agentMerchantID, 50); err != nil {
			a.logger.WarnContext(r.Context(), "list agent pickups", "error", err)
		} else {
			data["Pickups"] = pickups
		}
	}
	if merchant, err := a.store.MerchantByID(r.Context(), agentMerchantID); err == nil {
		data["AgentName"] = merchant.Name
	}
	return data
}

// agentRedeemPost pays out a cash pickup through AgentRemittanceService, which
// screens the recipient and delegates authorisation to store.RedeemPickupTx. It
// refuses unless a fresh authenticator code is presented when TOTP is configured
// (per-payout step-up), then redirects to the receipt on success.
func (a *App) agentRedeemPost(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != agentCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !a.cfg.AgentNetworkEnabled {
		a.renderAgentStatus(w, "agent_redeem.html", r, "Cash pickup",
			a.agentRedeemData(r, "Cash pickup at an agent is currently unavailable."), http.StatusServiceUnavailable)
		return
	}
	agentMerchantID := agentMerchantIDFromContext(r.Context())
	agentUserID := agentUserIDFromContext(r.Context())

	if a.totpAvailable() {
		if !a.agentStepUpAvailable(r.Context(), agentUserID) {
			a.renderAgentStatus(w, "agent_redeem.html", r, "Cash pickup",
				a.agentRedeemData(r, "Add an authenticator app under Security before paying out."), http.StatusForbidden)
			return
		}
		if !a.verifyAgentStepUp(r.Context(), agentUserID, strings.TrimSpace(r.FormValue("step_up_code"))) {
			a.renderAgentStatus(w, "agent_redeem.html", r, "Cash pickup",
				a.agentRedeemData(r, "That authenticator code was not valid. Try again."), http.StatusUnauthorized)
			return
		}
	}

	receipt, err := a.agentRemittance.Redeem(r.Context(), service.RedeemRequest{
		AgentMerchantID:  agentMerchantID,
		OperatorUserID:   &agentUserID,
		Code:             strings.TrimSpace(r.FormValue("code")),
		OTP:              strings.TrimSpace(r.FormValue("otp")),
		ReceiverIDType:   strings.TrimSpace(r.FormValue("id_type")),
		ReceiverIDNumber: strings.TrimSpace(r.FormValue("id_number")),
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "agent redemption refused", "error", err)
		a.renderAgentStatus(w, "agent_redeem.html", r, "Cash pickup", a.agentRedeemData(r, agentRedeemError(err)), http.StatusOK)
		return
	}
	a.audit(r, "agent.redeem", "remittance", receipt.Remittance.ID.String(), map[string]any{
		"agent_merchant_id": agentMerchantID.String(),
		"amount_kobo":       receipt.Remittance.AmountKobo,
	})
	http.Redirect(w, r, "/agent/receipt/"+receipt.Remittance.ID.String(), http.StatusSeeOther)
}

// agentReceiptPage renders the printable receipt for a payout this agent made.
func (a *App) agentReceiptPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid receipt", http.StatusBadRequest)
		return
	}
	receipt, err := a.agentRemittance.Receipt(r.Context(), agentMerchantIDFromContext(r.Context()), id)
	if err != nil {
		a.logger.WarnContext(r.Context(), "agent receipt lookup", "error", err)
		http.Error(w, "receipt not found", http.StatusNotFound)
		return
	}
	a.renderAgent(w, "agent_receipt.html", r, "Cash pickup receipt", map[string]any{"Receipt": receipt})
}

// agentStepUpAvailable reports whether the console can complete a per-payout
// step-up: authenticator apps configured and a confirmed TOTP factor enrolled.
func (a *App) agentStepUpAvailable(ctx context.Context, agentUserID uuid.UUID) bool {
	if !a.totpAvailable() {
		return false
	}
	factor, err := a.store.MFAFactorByKind(ctx, store.MFAScopeAgent, agentUserID, store.MFAKindTOTP)
	return err == nil && factor != nil && factor.ConfirmedAt != nil && len(factor.SecretCipher) > 0
}

// verifyAgentStepUp checks a fresh authenticator code against the agent
// operator's enrolled TOTP factor.
func (a *App) verifyAgentStepUp(ctx context.Context, agentUserID uuid.UUID, code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	factor, err := a.store.MFAFactorByKind(ctx, store.MFAScopeAgent, agentUserID, store.MFAKindTOTP)
	if err != nil || factor == nil || factor.ConfirmedAt == nil || len(factor.SecretCipher) == 0 {
		return false
	}
	secret, err := totp.DecryptSecret(a.totpKey, factor.SecretCipher)
	if err != nil {
		return false
	}
	return totp.Validate(code, secret)
}

// agentRedeemError turns a redemption refusal into operator-friendly copy.
func agentRedeemError(err error) string {
	switch {
	case errors.Is(err, store.ErrRemittanceCodeInvalid), errors.Is(err, store.ErrRemittanceNotFound):
		return "That pickup code was not recognised. Check it and try again."
	case errors.Is(err, store.ErrRemittanceOTPRequired):
		return "There is no active one-time code for this pickup. Ask the sender to request one."
	case errors.Is(err, store.ErrRemittanceOTPInvalid):
		return "That one-time code was not correct."
	case errors.Is(err, store.ErrRemittanceOTPExpired):
		return "That one-time code has expired. Ask the sender to request a new one."
	case errors.Is(err, store.ErrRemittanceOTPLocked):
		return "Too many wrong one-time codes. Ask the sender to request a new one."
	case errors.Is(err, store.ErrReceiverIDRequired):
		return "A government photo ID is required to pay out."
	case errors.Is(err, store.ErrRecipientBlocked):
		return "This payout is on hold pending a compliance review."
	case errors.Is(err, store.ErrCustomerCapExceeded):
		return "This recipient has reached their cash-out limit for the period."
	case errors.Is(err, store.ErrAgentRegionMismatch):
		return "This remittance is not assigned to your agent location."
	case errors.Is(err, store.ErrRemittanceAlreadyRedeemed):
		return "This pickup was already redeemed."
	case errors.Is(err, store.ErrRemittanceNotClaimable):
		return "This pickup has expired or is not yet claimable."
	case errors.Is(err, store.ErrAgentNotActive):
		return "Your agent account is not active. Contact operations."
	case errors.Is(err, store.ErrAgentPayoutLimitExceeded):
		return "This payout exceeds your agent's limit."
	case errors.Is(err, store.ErrAgentFloatCapExceeded), errors.Is(err, store.ErrInsufficientAgentFloat):
		return "Your agent float cannot cover this payout. Settle or top up first."
	}
	return "The payout could not be completed. Check the details and try again."
}
