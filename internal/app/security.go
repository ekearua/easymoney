package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
)

// securityRoutes carries the routes of one portal's security page, so a single
// set of handlers serves both portals.
type securityRoutes struct {
	Page          string
	TOTPStart     string
	TOTPVerify    string
	EmailStart    string
	EmailVerify   string
	PasskeyBegin  string
	PasskeyFinish string
	RevokeAll     string
}

// securityRoutesFor returns the security routes of a portal.
func securityRoutesFor(scope string) securityRoutes {
	base := "/admin/security"
	if scope == store.MFAScopeMerchant {
		base = "/merchant/security"
	}
	return securityRoutes{
		Page:          base,
		TOTPStart:     base + "/totp",
		TOTPVerify:    base + "/totp/verify",
		EmailStart:    base + "/email",
		EmailVerify:   base + "/email/verify",
		PasskeyBegin:  base + "/passkeys/begin",
		PasskeyFinish: base + "/passkeys/finish",
		RevokeAll:     base + "/factors/revoke-all",
	}
}

// factorView is one row of the sign-in method table.
type factorView struct {
	ID         int64
	Kind       string
	KindLabel  string
	Label      string
	Confirmed  bool
	Created    time.Time
	LastUsedAt *time.Time
}

// factorViews shapes stored factors for the security page.
func factorViews(factors []store.MFAFactor) []factorView {
	views := make([]factorView, 0, len(factors))
	for _, factor := range factors {
		label := factor.Label
		if label == "" {
			label = store.MFAKindLabel(factor.Kind)
		}
		views = append(views, factorView{
			ID:         factor.ID,
			Kind:       factor.Kind,
			KindLabel:  store.MFAKindLabel(factor.Kind),
			Label:      label,
			Confirmed:  factor.ConfirmedAt != nil,
			Created:    factor.CreatedAt,
			LastUsedAt: factor.LastUsedAt,
		})
	}
	return views
}

// securitySummaryData is the sign-in security state the settings and metrics
// pages show. Those pages link to the security page instead of duplicating its
// controls, so they only need the enrolled method names and whether the
// deployment requires a second step.
func (a *App) securitySummaryData(ctx context.Context, scope string, subjectID uuid.UUID) map[string]any {
	factors, err := a.store.MFAFactors(ctx, scope, subjectID)
	if err != nil {
		a.logger.ErrorContext(ctx, "list second factors", "scope", scope, "error", err)
	}
	methods := factorViews(factors)
	return map[string]any{
		"MFAMethods":  methods,
		"MFAEnrolled": len(methods) > 0,
		"MFAEnforced": a.cfg.TOTPEnabled,
		"MFAPath":     securityRoutesFor(scope).Page,
	}
}

// securitySubject resolves the signed-in account of a portal.
func (a *App) securitySubject(r *http.Request, scope string) (uuid.UUID, bool) {
	id := adminIDFromContext(r.Context())
	if scope == store.MFAScopeMerchant {
		id = merchantIDFromContext(r.Context())
	}
	return id, id != uuid.Nil
}

// checkSecurityCSRF verifies the form token of a portal session.
func (a *App) checkSecurityCSRF(w http.ResponseWriter, r *http.Request, scope string) bool {
	token := r.FormValue("csrf_token")
	if scope == store.MFAScopeMerchant && token == merchantCSRFFromContext(r.Context()) {
		return true
	}
	if scope == store.MFAScopeAdmin && token == csrfFromContext(r.Context()) {
		return true
	}
	http.Error(w, "invalid CSRF token", http.StatusForbidden)
	return false
}

// checkJSONCSRF verifies the token a JSON caller sends in a header. A JSON body
// is read once, so the token cannot ride inside it.
func (a *App) checkJSONCSRF(w http.ResponseWriter, r *http.Request, scope string) bool {
	token := r.Header.Get("X-CSRF-Token")
	if scope == store.MFAScopeMerchant && token == merchantCSRFFromContext(r.Context()) {
		return true
	}
	if scope == store.MFAScopeAdmin && token == csrfFromContext(r.Context()) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid CSRF token"})
	return false
}

// redirectSecurity returns to the security page carrying one notice keyword.
func (a *App) redirectSecurity(w http.ResponseWriter, r *http.Request, scope, notice string) {
	http.Redirect(w, r, securityRoutesFor(scope).Page+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

// securityNotice turns a redirect keyword into copy the page can show.
func securityNotice(keyword string) string {
	switch keyword {
	case "totp":
		return "Authenticator app enrolled."
	case "email":
		return "Emailed codes are now a sign-in method for this account."
	case "passkey":
		return "Passkey added."
	case "renamed":
		return "Sign-in method renamed."
	case "revoked":
		return "Sign-in method removed."
	case "revoked-all":
		return "Every sign-in method was removed. Enroll one to keep two-step sign-in."
	case "step-expired":
		return "That setup step expired. Start again."
	case "label-invalid":
		return "Give the method a name of 60 characters or fewer."
	case "totp-unavailable":
		return "Authenticator apps are not configured on this deployment."
	case "email-unavailable":
		return "Email delivery is not configured, so emailed codes cannot be set up."
	case "email-failed":
		return "The code could not be sent. Try again in a moment."
	case "passkey-failed":
		return "That passkey could not be registered. Try again."
	}
	return ""
}

// securityStepCopy is the enrollment wording of the shared step page.
func securityStepCopy(scope, method string) stepCopy {
	title := "Set up two-step sign-in"
	if method == store.MFAKindEmail {
		title = "Confirm the emailed code"
	}
	return stepCopy{
		Title:       title,
		Eyebrow:     "Security",
		CancelPath:  securityRoutesFor(scope).Page,
		CancelLabel: "Back to security settings",
	}
}

// securityVerifyPath is the step route for a method being enrolled.
func securityVerifyPath(scope, method string) string {
	routes := securityRoutesFor(scope)
	if method == store.MFAKindEmail {
		return routes.EmailVerify
	}
	return routes.TOTPVerify
}

// securityPage renders the second-factor management page of a portal account.
func (a *App) securityPage(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok {
			a.redirectToLogin(w, r, scope)
			return
		}
		factors, err := a.store.MFAFactors(r.Context(), scope, subject)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "list second factors", "scope", scope, "error", err)
			http.Error(w, "security settings unavailable", http.StatusInternalServerError)
			return
		}
		address := a.subjectEmail(r.Context(), scope, subject)
		summary := a.securitySummaryData(r.Context(), scope, subject)
		data := map[string]any{
			"Factors":           factorViews(factors),
			"Paths":             securityRoutesFor(scope),
			"TotpAvailable":     a.totpAvailable(),
			"EmailAvailable":    a.email != nil && address != "",
			"EmailTo":           maskedEmail(address),
			"PasskeysAvailable": a.webAuthn != nil,
			"Enforced":          a.cfg.TOTPEnabled,
			"Enrolled":          len(factors) > 0,
			"Notice":            securityNotice(r.URL.Query().Get("notice")),
			"MFAMethods":        summary["MFAMethods"],
			"MFAEnrolled":       summary["MFAEnrolled"],
			"MFAEnforced":       summary["MFAEnforced"],
			"MFAPath":           summary["MFAPath"],
		}
		if scope == store.MFAScopeMerchant {
			a.renderMerchant(w, "merchant_security.html", r, "Sign-in security", data)
			return
		}
		a.renderAdmin(w, "admin_security.html", r, "Sign-in security", data)
	}
}

// renderSecurityStep stores a fresh enrollment step and renders the shared step
// page with enrollment copy.
func (a *App) renderSecurityStep(w http.ResponseWriter, r *http.Request, scope string, subject uuid.UUID, challenge store.MFAChallenge, postPath string, failure string) {
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256([]byte(token))
	if err := a.store.CreateMFAChallenge(r.Context(), hash[:], challenge); err != nil {
		a.logger.ErrorContext(r.Context(), "create enrollment step", "scope", scope, "error", err)
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	copy := securityStepCopy(scope, challenge.Method)
	copy.PostPath = postPath
	account := a.subjectEmail(r.Context(), scope, subject)
	a.render(w, "mfa_step.html", a.secondFactorRenderData(r, challenge, token, account, failure, "", copy))
}

// securityStartTOTP begins authenticator enrollment: a fresh secret is held on
// the step until a code proves the app holds it.
func (a *App) securityStartTOTP(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		if !a.totpAvailable() {
			a.redirectSecurity(w, r, scope, "totp-unavailable")
			return
		}
		account := a.subjectEmail(r.Context(), scope, subject)
		secretCipher, err := a.newTOTPSecret(account)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "generate totp secret", "scope", scope, "error", err)
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		challenge := store.MFAChallenge{
			Scope:        scope,
			SubjectID:    subject,
			Method:       store.MFAKindTOTP,
			SecretCipher: secretCipher,
			ExpiresAt:    time.Now().Add(mfaStepTTL),
		}
		a.renderSecurityStep(w, r, scope, subject, challenge, securityRoutesFor(scope).TOTPVerify, "")
	}
}

// securityStartEmail begins emailed-code enrollment: the account address gets a
// code, and the factor only counts once that code comes back.
func (a *App) securityStartEmail(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		address := a.subjectEmail(r.Context(), scope, subject)
		if a.email == nil || address == "" {
			a.redirectSecurity(w, r, scope, "email-unavailable")
			return
		}
		factorID, err := a.store.UpsertMFAFactor(r.Context(), store.MFAFactor{
			Scope:     scope,
			SubjectID: subject,
			Kind:      store.MFAKindEmail,
			Label:     address,
		})
		if err != nil {
			a.logger.ErrorContext(r.Context(), "enroll email factor", "scope", scope, "error", err)
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		challenge := store.MFAChallenge{
			Scope:     scope,
			SubjectID: subject,
			Method:    store.MFAKindEmail,
			FactorID:  &factorID,
			ExpiresAt: time.Now().Add(mfaStepTTL),
		}
		if err := a.prepareChallenge(r.Context(), &challenge); err != nil {
			a.logger.ErrorContext(r.Context(), "send enrollment code", "scope", scope, "error", err)
			a.redirectSecurity(w, r, scope, "email-failed")
			return
		}
		a.renderSecurityStep(w, r, scope, subject, challenge, securityRoutesFor(scope).EmailVerify, "")
	}
}

// securityVerifyFactor confirms an enrollment step with the code it sent.
func (a *App) securityVerifyFactor(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok {
			a.redirectToLogin(w, r, scope)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		token := strings.TrimSpace(r.FormValue("token"))
		hash := sha256.Sum256([]byte(token))
		challenge, found, err := a.store.MFAChallengeByToken(r.Context(), hash[:])
		if err != nil {
			a.logger.ErrorContext(r.Context(), "load enrollment step", "scope", scope, "error", err)
		}
		if !found || challenge.Scope != scope || challenge.SubjectID != subject {
			a.redirectSecurity(w, r, scope, "step-expired")
			return
		}
		account := a.subjectEmail(r.Context(), scope, subject)
		copy := securityStepCopy(scope, challenge.Method)
		copy.PostPath = securityVerifyPath(scope, challenge.Method)
		if strings.TrimSpace(r.FormValue("action")) == "resend" {
			if challenge.Method != store.MFAKindEmail {
				a.renderSecondFactor(w, r, challenge, token, account,
					"Only an emailed code can be resent.", "", copy, http.StatusBadRequest)
				return
			}
			codeHash, err := a.issueStepCode(r.Context(), challenge, hash[:])
			if err != nil {
				a.logger.ErrorContext(r.Context(), "resend enrollment code", "scope", scope, "error", err)
				a.renderSecondFactor(w, r, challenge, token, account,
					"That code could not be sent. Try again in a moment.", "", copy, http.StatusBadGateway)
				return
			}
			updated := challenge
			updated.CodeHash = codeHash
			a.renderSecondFactor(w, r, updated, token, account, "", "A new code is on its way.", copy, http.StatusOK)
			return
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if code == "" {
			a.renderSecondFactor(w, r, challenge, token, account, "Enter the code to continue.", "", copy, http.StatusOK)
			return
		}
		if err := a.verifySecondFactor(r.Context(), challenge, code); err != nil {
			if !errors.Is(err, errSecondFactorCode) {
				a.logger.ErrorContext(r.Context(), "verify enrollment code", "scope", scope, "error", err)
				a.renderSecondFactor(w, r, challenge, token, account,
					"That code could not be checked. Try again.", "", copy, http.StatusOK)
				return
			}
			remaining, failErr := a.store.FailMFAChallenge(r.Context(), hash[:], mfaChallengeMaxAttempts)
			if failErr != nil {
				a.logger.ErrorContext(r.Context(), "record failed enrollment code", "scope", scope, "error", failErr)
			}
			if remaining <= 0 {
				a.redirectSecurity(w, r, scope, "step-expired")
				return
			}
			a.renderSecondFactor(w, r, challenge, token, account, invalidCodeCopy(remaining), "", copy, http.StatusUnauthorized)
			return
		}
		if err := a.confirmSecurityFactor(r, challenge, token); err != nil {
			a.logger.ErrorContext(r.Context(), "confirm enrollment", "scope", scope, "error", err)
			a.redirectSecurity(w, r, scope, "step-expired")
			return
		}
		if challenge.Method == store.MFAKindEmail {
			a.redirectSecurity(w, r, scope, "email")
			return
		}
		a.redirectSecurity(w, r, scope, "totp")
	}
}

// confirmSecurityFactor consumes an enrollment step and records the factor.
func (a *App) confirmSecurityFactor(r *http.Request, challenge store.MFAChallenge, token string) error {
	hash := sha256.Sum256([]byte(token))
	if _, consumed, err := a.store.ConsumeMFAChallenge(r.Context(), hash[:]); err != nil || !consumed {
		return errors.New("this setup step has expired")
	}
	switch challenge.Method {
	case store.MFAKindEmail:
		if challenge.FactorID == nil {
			return errors.New("email factor missing")
		}
		if err := a.store.ConfirmMFAFactor(r.Context(), *challenge.FactorID); err != nil {
			return err
		}
		a.auditMFAEvent(r, challenge.Scope, challenge.SubjectID, "email_enrolled", *challenge.FactorID)
		return nil
	default:
		if challenge.SecretCipher == nil {
			return errors.New("no authenticator secret on this step")
		}
		now := time.Now()
		id, err := a.store.UpsertMFAFactor(r.Context(), store.MFAFactor{
			Scope:        challenge.Scope,
			SubjectID:    challenge.SubjectID,
			Kind:         store.MFAKindTOTP,
			SecretCipher: challenge.SecretCipher,
			ConfirmedAt:  &now,
		})
		if err != nil {
			return err
		}
		a.auditMFAEvent(r, challenge.Scope, challenge.SubjectID, "totp_enrolled", id)
		return nil
	}
}

// newTOTPSecret generates and seals a fresh authenticator secret.
func (a *App) newTOTPSecret(account string) ([]byte, error) {
	if !a.totpAvailable() {
		return nil, errors.New("authenticator apps are not configured")
	}
	key, err := totp.Generate(a.cfg.AppName, account)
	if err != nil {
		return nil, err
	}
	return totp.EncryptSecret(a.totpKey, key.Secret())
}

// securityRevokeFactor removes one method the subject owns. Every live step for
// the subject is dropped with it, so a revocation cannot be outrun by a step
// that is already open.
func (a *App) securityRevokeFactor(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid factor", http.StatusBadRequest)
			return
		}
		if err := a.store.DeleteMFAFactor(r.Context(), scope, subject, id); err != nil {
			a.logger.ErrorContext(r.Context(), "revoke factor", "scope", scope, "error", err)
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		if err := a.store.DeleteMFAChallengesForSubject(r.Context(), scope, subject); err != nil {
			a.logger.ErrorContext(r.Context(), "clear factor steps", "scope", scope, "error", err)
		}
		a.auditMFAEvent(r, scope, subject, "factor_revoked", id)
		a.redirectSecurity(w, r, scope, "revoked")
	}
}

// securityRenameFactor relabels one method the subject owns.
func (a *App) securityRenameFactor(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid factor", http.StatusBadRequest)
			return
		}
		label := strings.TrimSpace(r.FormValue("label"))
		if label == "" || len(label) > 60 {
			a.redirectSecurity(w, r, scope, "label-invalid")
			return
		}
		if err := a.store.RenameMFAFactor(r.Context(), scope, subject, id, label); err != nil {
			a.logger.ErrorContext(r.Context(), "rename factor", "scope", scope, "error", err)
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		a.redirectSecurity(w, r, scope, "renamed")
	}
}

// securityRevokeAll removes every method of a subject — the recovery path when
// a device is lost and no other method works.
func (a *App) securityRevokeAll(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkSecurityCSRF(w, r, scope) {
			return
		}
		count, err := a.store.DeleteMFAFactorsForSubject(r.Context(), scope, subject)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "revoke all factors", "scope", scope, "error", err)
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		if err := a.store.DeleteMFAChallengesForSubject(r.Context(), scope, subject); err != nil {
			a.logger.ErrorContext(r.Context(), "clear factor steps", "scope", scope, "error", err)
		}
		a.auditMFAEvent(r, scope, subject, "factors_revoked", count)
		a.redirectSecurity(w, r, scope, "revoked-all")
	}
}

// securityPasskeyBegin hands the browser its creation options.
func (a *App) securityPasskeyBegin(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkJSONCSRF(w, r, scope) {
			return
		}
		if a.webAuthn == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Passkeys are not configured on this deployment."})
			return
		}
		account := a.subjectEmail(r.Context(), scope, subject)
		options, state, err := a.beginPasskeyRegistration(r.Context(), scope, subject, account)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "begin passkey registration", "scope", scope, "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": passkeyPublicError(err)})
			return
		}
		token, err := randomToken(32)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "security error"})
			return
		}
		hash := sha256.Sum256([]byte(token))
		challenge := store.MFAChallenge{
			Scope:         scope,
			SubjectID:     subject,
			Method:        store.MFAKindPasskey,
			WebAuthnState: state,
			ExpiresAt:     time.Now().Add(mfaStepTTL),
		}
		if err := a.store.CreateMFAChallenge(r.Context(), hash[:], challenge); err != nil {
			a.logger.ErrorContext(r.Context(), "store passkey ceremony", "scope", scope, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "security error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "options": json.RawMessage(options)})
	}
}

// securityPasskeyFinish verifies the attestation and stores the credential.
func (a *App) securityPasskeyFinish(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject, ok := a.securitySubject(r, scope)
		if !ok || !a.checkJSONCSRF(w, r, scope) {
			return
		}
		var payload struct {
			Token      string          `json:"token"`
			Label      string          `json:"label"`
			Credential json.RawMessage `json:"credential"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}
		hash := sha256.Sum256([]byte(payload.Token))
		challenge, found, err := a.store.MFAChallengeByToken(r.Context(), hash[:])
		if err != nil {
			a.logger.ErrorContext(r.Context(), "load passkey ceremony", "scope", scope, "error", err)
		}
		if !found || challenge.Method != store.MFAKindPasskey || challenge.Scope != scope || challenge.SubjectID != subject {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Ask for the passkey prompt again."})
			return
		}
		if len(payload.Credential) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No passkey response was sent."})
			return
		}
		account := a.subjectEmail(r.Context(), scope, subject)
		id, err := a.finishPasskeyRegistration(r.Context(), scope, subject, account, challenge.WebAuthnState, payload.Credential, payload.Label)
		if err != nil {
			a.logger.WarnContext(r.Context(), "passkey registration rejected", "scope", scope, "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": passkeyPublicError(err)})
			return
		}
		if _, _, err := a.store.ConsumeMFAChallenge(r.Context(), hash[:]); err != nil {
			a.logger.ErrorContext(r.Context(), "consume passkey ceremony", "scope", scope, "error", err)
		}
		a.auditMFAEvent(r, scope, subject, "passkey_registered", id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": passkeyLabel(payload.Label)})
	}
}

// adminResetMerchantFactors clears every second factor a merchant has enrolled,
// which is the recovery path for a merchant locked out by a lost device. It is
// operator gated and audited.
func (a *App) adminResetMerchantFactors(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant", http.StatusBadRequest)
		return
	}
	count, err := a.store.DeleteMFAFactorsForSubject(r.Context(), store.MFAScopeMerchant, merchantID)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "reset merchant factors", "merchant_id", merchantID, "error", err)
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	if err := a.store.DeleteMFAChallengesForSubject(r.Context(), store.MFAScopeMerchant, merchantID); err != nil {
		a.logger.ErrorContext(r.Context(), "clear merchant factors steps", "merchant_id", merchantID, "error", err)
	}
	a.audit(r, "admin.merchant_mfa_reset", "admin", merchantID.String(), map[string]any{"factors": count})
	http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
}
