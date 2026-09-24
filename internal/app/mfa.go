package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
)

// mfaStepTTL is how long one second-factor step stays valid.
const mfaStepTTL = 10 * time.Minute

// mfaCodeLength is the digit count of emailed one-time codes and authenticator
// codes alike.
const mfaCodeLength = 6

// errSecondFactorCode marks a wrong code. Every other verification failure is a
// fault the subject cannot fix by retyping.
var errSecondFactorCode = errors.New("invalid authentication code")

// methodOption is one selectable second-factor method on the step page.
type methodOption struct {
	Kind  string
	Label string
}

// secondFactorPaths returns the second-factor step route and the sign-in page
// that cancels back to, for a portal scope.
func secondFactorPaths(scope string) (stepPath, loginPath string) {
	if scope == store.MFAScopeMerchant {
		return "/merchant/login/2fa", "/merchant/login"
	}
	return "/admin/login/2fa", "/admin/login"
}

// stepCopy customises the shared second-factor page. Sign-in and enrollment run
// the same step machinery but not the same words, so the words travel with the
// caller instead of being guessed from the database.
type stepCopy struct {
	Title       string
	Eyebrow     string
	PostPath    string
	CancelPath  string
	CancelLabel string
}

// signInCopy is the step page copy for a portal sign-in.
func signInCopy(scope string) stepCopy {
	stepPath, loginPath := secondFactorPaths(scope)
	copy := stepCopy{
		Title:       "Two-step sign-in",
		Eyebrow:     "Operations",
		PostPath:    stepPath,
		CancelPath:  loginPath,
		CancelLabel: "Back to sign in",
	}
	if scope == store.MFAScopeMerchant {
		copy.Eyebrow = "Merchant"
	}
	return copy
}

// scopeFromPath infers the portal from the request path, so a step that can no
// longer be read still returns the visitor to the right sign-in page.
func scopeFromPath(path string) string {
	if strings.HasPrefix(path, "/merchant") {
		return store.MFAScopeMerchant
	}
	return store.MFAScopeAdmin
}

// loginTemplateFor names the sign-in page a scope cancels back to.
func loginTemplateFor(scope string) string {
	if scope == store.MFAScopeMerchant {
		return "merchant_login.html"
	}
	return "login.html"
}

// beginSecondFactor starts the second step of a sign-in. A subject with no
// enrolled factor enrolls an authenticator app (the historical behaviour);
// otherwise the step opens directly on the subject's preferred usable method.
func (a *App) beginSecondFactor(w http.ResponseWriter, r *http.Request, scope string, subjectID uuid.UUID, account string) {
	challenge := store.MFAChallenge{
		Scope:     scope,
		SubjectID: subjectID,
		ExpiresAt: time.Now().Add(mfaStepTTL),
	}
	factors, err := a.usableFactors(r.Context(), scope, subjectID)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load second factors", "scope", scope, "error", err)
		http.Error(w, "security setup error", http.StatusInternalServerError)
		return
	}
	if len(factors) == 0 {
		key, err := totp.Generate(a.cfg.AppName, account)
		if err != nil {
			http.Error(w, "security setup error", http.StatusInternalServerError)
			return
		}
		cipher, err := totp.EncryptSecret(a.totpKey, key.Secret())
		if err != nil {
			http.Error(w, "security setup error", http.StatusInternalServerError)
			return
		}
		challenge.Method = store.MFAKindTOTP
		challenge.SecretCipher = cipher
	} else {
		factor := preferredFactor(factors)
		challenge.Method = factor.Kind
		challenge.FactorID = &factor.ID
		if err := a.prepareChallenge(r.Context(), &challenge); err != nil {
			// A method that cannot be readied (mail delivery down, for one)
			// must not strand the step: fall back to the picker so the
			// subject can choose something that works.
			a.logger.ErrorContext(r.Context(), "prepare second factor", "scope", scope, "method", factor.Kind, "error", err)
			challenge.Method = ""
			challenge.FactorID = nil
			challenge.CodeHash = nil
		}
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256([]byte(token))
	if err := a.store.CreateMFAChallenge(r.Context(), hash[:], challenge); err != nil {
		a.logger.ErrorContext(r.Context(), "create second factor challenge", "scope", scope, "error", err)
		http.Error(w, "security setup error", http.StatusInternalServerError)
		return
	}
	stepPath, _ := secondFactorPaths(scope)
	q := url.Values{"token": {token}, "account": {account}}
	http.Redirect(w, r, stepPath+"?"+q.Encode(), http.StatusSeeOther)
}

// usableFactors lists the factors a subject can actually finish a step with:
// confirmed enrollments, minus emailed codes when no mail sender is configured.
func (a *App) usableFactors(ctx context.Context, scope string, subjectID uuid.UUID) ([]store.MFAFactor, error) {
	factors, err := a.store.MFAFactors(ctx, scope, subjectID)
	if err != nil {
		return nil, err
	}
	usable := make([]store.MFAFactor, 0, len(factors))
	for _, factor := range factors {
		if factor.ConfirmedAt == nil {
			continue
		}
		if factor.Kind == store.MFAKindEmail && a.email == nil {
			continue
		}
		usable = append(usable, factor)
	}
	return usable, nil
}

// preferredFactor picks the method a step opens on: the authenticator app first
// (it works offline and is what nearly every subject has), then a passkey, then
// an emailed code.
func preferredFactor(factors []store.MFAFactor) store.MFAFactor {
	for _, kind := range []string{store.MFAKindTOTP, store.MFAKindPasskey, store.MFAKindEmail} {
		for _, factor := range factors {
			if factor.Kind == kind {
				return factor
			}
		}
	}
	return factors[0]
}

// prepareChallenge readies the selected method. Only an emailed code needs
// work: it is generated, hashed, and sent to the factor's address.
func (a *App) prepareChallenge(ctx context.Context, challenge *store.MFAChallenge) error {
	if challenge.Method != store.MFAKindEmail {
		return nil
	}
	if challenge.FactorID == nil {
		return errors.New("email factor missing")
	}
	factor, err := a.store.MFAFactorByID(ctx, *challenge.FactorID)
	if err != nil {
		return err
	}
	if factor == nil || strings.TrimSpace(factor.Label) == "" {
		return errors.New("email factor has no address")
	}
	code, err := randomDigits(mfaCodeLength)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := a.sendSecondFactorEmail(ctx, factor.Label, code); err != nil {
		return err
	}
	challenge.CodeHash = hash
	return nil
}

// sendSecondFactorEmail delivers one sign-in code.
func (a *App) sendSecondFactorEmail(ctx context.Context, to, code string) error {
	if a.email == nil {
		return errors.New("email delivery is not configured")
	}
	body := fmt.Sprintf("%s sign-in code: %s\n\nIt expires in %d minutes. If you did not try to sign in, change your password now.",
		a.cfg.AppName, code, int(mfaStepTTL.Minutes()))
	return a.email.Send(ctx, to, a.cfg.AppName+" sign-in code", body)
}

// randomDigits returns a cryptographically random n-digit string.
func randomDigits(n int) (string, error) {
	limit := big.NewInt(1)
	for range n {
		limit.Mul(limit, big.NewInt(10))
	}
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", n, value), nil
}

// maskedEmail hides most of an address in step copy.
func maskedEmail(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return address
	}
	local, domain := address[:at], address[at:]
	if len(local) > 2 {
		return local[:2] + strings.Repeat("•", len(local)-2) + domain
	}
	return local[:1] + strings.Repeat("•", len(local)-1) + domain
}

// challengeByToken resolves a live step from the page's single-use token.
func (a *App) challengeByToken(ctx context.Context, token string) (store.MFAChallenge, bool, error) {
	if token == "" {
		return store.MFAChallenge{}, false, nil
	}
	hash := sha256.Sum256([]byte(token))
	return a.store.MFAChallengeByToken(ctx, hash[:])
}

// secondFactorPage renders the live step: the method picker when the subject
// has to choose, otherwise the selected method's form.
func (a *App) secondFactorPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	challenge, found, err := a.challengeByToken(r.Context(), token)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load second factor step", "error", err)
	}
	if !found || challenge.Attempts >= mfaChallengeMaxAttempts {
		a.redirectToLogin(w, r, scopeFromPath(r.URL.Path))
		return
	}
	a.render(w, "mfa_step.html", a.secondFactorRenderData(r, challenge, token, account, "", "", signInCopy(challenge.Scope)))
}

// redirectToLogin sends a visitor back to a scope's sign-in page.
func (a *App) redirectToLogin(w http.ResponseWriter, r *http.Request, scope string) {
	_, loginPath := secondFactorPaths(scope)
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

// secondFactorRenderData builds the step page payload. The QR is a PNG data URI
// and must be typed template.URL: html/template's URL filter defangs every
// scheme other than http, https, and mailto, so an untyped string renders as
// src="#ZgotmplZ" and the subject gets a broken image instead of a scannable
// code.
func (a *App) secondFactorRenderData(r *http.Request, challenge store.MFAChallenge, token, account, failure, notice string, copy stepCopy) map[string]any {
	if copy.PostPath == "" {
		copy = signInCopy(challenge.Scope)
	}
	data := map[string]any{
		"AppName":     a.cfg.AppName,
		"Title":       copy.Title,
		"Eyebrow":     copy.Eyebrow,
		"Token":       token,
		"Account":     account,
		"StepPath":    copy.PostPath,
		"CancelPath":  copy.CancelPath,
		"CancelLabel": copy.CancelLabel,
		"Error":       failure,
		"Notice":      notice,
		"Method":      challenge.Method,
		"MethodLabel": store.MFAKindLabel(challenge.Method),
		"CodeLength":  mfaCodeLength,
		"Enrolling":   challenge.SecretCipher != nil,
	}
	if challenge.SecretCipher != nil {
		secret, err := totp.DecryptSecret(a.totpKey, challenge.SecretCipher)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "decrypt totp secret", "error", err)
		} else {
			data["Secret"] = secret
			png, err := qrcode.Encode(totp.KeyURI(a.cfg.AppName, account, secret), qrcode.Medium, 220)
			if err != nil {
				a.logger.ErrorContext(r.Context(), "encode totp qr", "error", err)
			} else {
				data["QR"] = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
			}
		}
	}
	if challenge.Method == store.MFAKindEmail && challenge.FactorID != nil {
		if factor, err := a.store.MFAFactorByID(r.Context(), *challenge.FactorID); err == nil && factor != nil {
			data["EmailTo"] = maskedEmail(factor.Label)
		}
	}
	// Enrollment carries its own secret, so a method switch makes no sense
	// there — the security page's cancel link covers it.
	if challenge.SecretCipher != nil {
		return data
	}
	factors, err := a.usableFactors(r.Context(), challenge.Scope, challenge.SubjectID)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "list second factors", "error", err)
	}
	options := make([]methodOption, 0, len(factors))
	passkeys := 0
	for _, factor := range factors {
		options = append(options, methodOption{Kind: factor.Kind, Label: store.MFAKindLabel(factor.Kind)})
		if factor.Kind == store.MFAKindPasskey {
			passkeys++
		}
	}
	data["Methods"] = options
	data["HasMethods"] = len(options) > 0
	data["MultiMethod"] = len(options) > 1
	data["Passkeys"] = passkeys
	data["ChooseMethod"] = challenge.Method == "" && len(options) > 0
	data["PasskeyReady"] = a.webAuthn != nil
	// Nothing enrolled and nothing usable is a real dead end: say so rather
	// than showing a form that cannot work.
	data["LockedOut"] = challenge.Method == "" && len(options) == 0
	return data
}

// secondFactorVerify advances a live step. Choosing another method and
// resending an emailed code act on the same row; entering a code verifies it.
func (a *App) secondFactorVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	account := strings.TrimSpace(r.FormValue("account"))
	hash := sha256.Sum256([]byte(token))
	challenge, found, err := a.challengeByToken(r.Context(), token)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load second factor step", "error", err)
	}
	if !found || challenge.Attempts >= mfaChallengeMaxAttempts {
		a.redirectToLogin(w, r, scopeFromPath(r.URL.Path))
		return
	}
	switch strings.TrimSpace(r.FormValue("action")) {
	case "method":
		a.chooseSecondFactor(w, r, challenge, token, account, hash[:], strings.TrimSpace(r.FormValue("method")))
		return
	case "resend":
		a.resendSecondFactorCode(w, r, challenge, token, account, hash[:])
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	copy := signInCopy(challenge.Scope)
	if code == "" {
		a.renderSecondFactor(w, r, challenge, token, account, "Enter the code to continue.", "", copy, http.StatusOK)
		return
	}
	if err := a.verifySecondFactor(r.Context(), challenge, code); err != nil {
		a.failSecondFactor(w, r, challenge, token, account, hash[:], copy, err)
		return
	}
	if err := a.completeSecondFactor(r, challenge, token); err != nil {
		a.logger.ErrorContext(r.Context(), "complete second factor", "scope", challenge.Scope, "error", err)
		a.renderStatus(w, loginTemplateFor(challenge.Scope), map[string]any{
			"AppName": a.cfg.AppName,
			"Error":   "Security error. Please sign in again.",
		}, http.StatusInternalServerError)
		return
	}
	a.finishSecondFactor(w, r, challenge)
}

// verifySecondFactor checks one code against the step's method.
func (a *App) verifySecondFactor(ctx context.Context, challenge store.MFAChallenge, code string) error {
	switch challenge.Method {
	case store.MFAKindTOTP:
		secret, err := a.stepTOTPSecret(ctx, challenge)
		if err != nil {
			return err
		}
		if !totp.Validate(code, secret) {
			return errSecondFactorCode
		}
		return nil
	case store.MFAKindEmail:
		if len(challenge.CodeHash) == 0 {
			return errors.New("no code was issued for this step")
		}
		if bcrypt.CompareHashAndPassword(challenge.CodeHash, []byte(code)) != nil {
			return errSecondFactorCode
		}
		return nil
	case store.MFAKindPasskey:
		return errors.New("this step needs a passkey, not a code")
	}
	return errors.New("no method is selected for this step")
}

// stepTOTPSecret resolves the shared secret to check a code against: the step's
// own cipher while enrolling, otherwise the enrolled factor's.
func (a *App) stepTOTPSecret(ctx context.Context, challenge store.MFAChallenge) (string, error) {
	cipher := challenge.SecretCipher
	if cipher == nil && challenge.FactorID != nil {
		factor, err := a.store.MFAFactorByID(ctx, *challenge.FactorID)
		if err != nil {
			return "", err
		}
		if factor == nil {
			return "", errors.New("authenticator factor missing")
		}
		cipher = factor.SecretCipher
	}
	if cipher == nil {
		return "", errors.New("authenticator secret missing")
	}
	return totp.DecryptSecret(a.totpKey, cipher)
}

// failSecondFactor records a wrong code, keeps the step alive while attempts
// remain, and discards it once the budget is spent.
func (a *App) failSecondFactor(w http.ResponseWriter, r *http.Request, challenge store.MFAChallenge, token, account string, tokenHash []byte, copy stepCopy, cause error) {
	a.limiter.fail(clientIP(r))
	if !errors.Is(cause, errSecondFactorCode) {
		a.logger.ErrorContext(r.Context(), "verify second factor", "scope", challenge.Scope, "method", challenge.Method, "error", cause)
		a.renderSecondFactor(w, r, challenge, token, account,
			"That code could not be checked. Try again or use another method.", "", copy, http.StatusOK)
		return
	}
	remaining, err := a.store.FailMFAChallenge(r.Context(), tokenHash, mfaChallengeMaxAttempts)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "record failed second factor", "scope", challenge.Scope, "error", err)
	}
	if remaining <= 0 {
		a.renderStatus(w, loginTemplateFor(challenge.Scope), map[string]any{
			"AppName": a.cfg.AppName,
			"Error":   "Too many incorrect codes. This sign-in step has expired. Please sign in again.",
		}, http.StatusUnauthorized)
		return
	}
	a.renderSecondFactor(w, r, challenge, token, account, invalidCodeCopy(remaining), "", copy, http.StatusUnauthorized)
}

// invalidCodeCopy is the retry message, with an honest count of what is left.
func invalidCodeCopy(remaining int) string {
	if remaining == 1 {
		return "Invalid authentication code. 1 attempt remaining."
	}
	return fmt.Sprintf("Invalid authentication code. %d attempts remaining.", remaining)
}

// renderSecondFactor renders the step page at a status code.
func (a *App) renderSecondFactor(w http.ResponseWriter, r *http.Request, challenge store.MFAChallenge, token, account, failure, notice string, copy stepCopy, status int) {
	a.renderStatus(w, "mfa_step.html", a.secondFactorRenderData(r, challenge, token, account, failure, notice, copy), status)
}

// chooseSecondFactor points the live step at another enrolled method, or opens
// the picker when method is empty.
func (a *App) chooseSecondFactor(w http.ResponseWriter, r *http.Request, challenge store.MFAChallenge, token, account string, tokenHash []byte, method string) {
	copy := signInCopy(challenge.Scope)
	if method == "" {
		blank := challenge
		blank.Method = ""
		blank.SecretCipher = nil
		blank.CodeHash = nil
		if err := a.store.SwitchMFAChallenge(r.Context(), tokenHash, "", nil, nil, nil, nil); err != nil {
			http.Error(w, "security error", http.StatusInternalServerError)
			return
		}
		a.renderSecondFactor(w, r, blank, token, account, "", "", copy, http.StatusOK)
		return
	}
	factors, err := a.usableFactors(r.Context(), challenge.Scope, challenge.SubjectID)
	if err != nil {
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	var chosen *store.MFAFactor
	for i := range factors {
		if factors[i].Kind == method {
			chosen = &factors[i]
			break
		}
	}
	if chosen == nil {
		a.renderSecondFactor(w, r, challenge, token, account,
			"That method is not set up for this account.", "", copy, http.StatusBadRequest)
		return
	}
	updated := challenge
	updated.Method = chosen.Kind
	updated.FactorID = &chosen.ID
	updated.SecretCipher = nil
	updated.CodeHash = nil
	if err := a.prepareChallenge(r.Context(), &updated); err != nil {
		a.logger.ErrorContext(r.Context(), "prepare second factor", "method", method, "error", err)
		a.renderSecondFactor(w, r, challenge, token, account,
			"That method is unavailable right now. Try another one.", "", copy, http.StatusBadGateway)
		return
	}
	if err := a.store.SwitchMFAChallenge(r.Context(), tokenHash, updated.Method, updated.FactorID, updated.SecretCipher, updated.CodeHash, nil); err != nil {
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	a.renderSecondFactor(w, r, updated, token, account, "", "", copy, http.StatusOK)
}

// issueStepCode mails a fresh code for a live emailed step and stores its hash,
// returning the hash so the caller can render without a second round trip.
func (a *App) issueStepCode(ctx context.Context, challenge store.MFAChallenge, tokenHash []byte) ([]byte, error) {
	if challenge.FactorID == nil {
		return nil, errors.New("email factor missing")
	}
	factor, err := a.store.MFAFactorByID(ctx, *challenge.FactorID)
	if err != nil {
		return nil, err
	}
	if factor == nil {
		return nil, errors.New("email factor missing")
	}
	code, err := randomDigits(mfaCodeLength)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if err := a.sendSecondFactorEmail(ctx, factor.Label, code); err != nil {
		return nil, err
	}
	if err := a.store.SetMFAChallengeCode(ctx, tokenHash, hash); err != nil {
		return nil, err
	}
	return hash, nil
}

// resendSecondFactorCode issues a fresh emailed code for the live step.
func (a *App) resendSecondFactorCode(w http.ResponseWriter, r *http.Request, challenge store.MFAChallenge, token, account string, tokenHash []byte) {
	copy := signInCopy(challenge.Scope)
	if challenge.Method != store.MFAKindEmail || challenge.FactorID == nil {
		a.renderSecondFactor(w, r, challenge, token, account, "There is no email code to resend for this step.", "", copy, http.StatusBadRequest)
		return
	}
	hash, err := a.issueStepCode(r.Context(), challenge, tokenHash)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "resend second factor code", "error", err)
		a.renderSecondFactor(w, r, challenge, token, account, "That code could not be sent. Try again or use another method.", "", copy, http.StatusBadGateway)
		return
	}
	notice := "A new code is on its way."
	if factor, err := a.store.MFAFactorByID(r.Context(), *challenge.FactorID); err == nil && factor != nil && factor.Label != "" {
		notice = "A new code is on its way to " + maskedEmail(factor.Label) + "."
	}
	updated := challenge
	updated.CodeHash = hash
	a.renderSecondFactor(w, r, updated, token, account, "", notice, copy, http.StatusOK)
}

// completeSecondFactor consumes the step, confirms a fresh enrollment, and
// records the use on the factor it verified.
func (a *App) completeSecondFactor(r *http.Request, challenge store.MFAChallenge, token string) error {
	hash := sha256.Sum256([]byte(token))
	if _, consumed, err := a.store.ConsumeMFAChallenge(r.Context(), hash[:]); err != nil || !consumed {
		return errors.New("this sign-in step has expired")
	}
	if challenge.SecretCipher != nil {
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
	if challenge.FactorID != nil {
		return a.store.TouchMFAFactor(r.Context(), *challenge.FactorID)
	}
	return nil
}

// finishSecondFactor issues the portal session once the step is complete.
func (a *App) finishSecondFactor(w http.ResponseWriter, r *http.Request, challenge store.MFAChallenge) {
	if challenge.Scope == store.MFAScopeMerchant {
		a.completeMerchantLogin(w, r, challenge.SubjectID)
		return
	}
	a.completeAdminLogin(w, r, challenge.SubjectID)
}
