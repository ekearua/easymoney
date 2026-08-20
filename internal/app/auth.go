package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
)

type totpPending struct {
	Token        string
	Scope        string
	SubjectID    *uuid.UUID
	SecretCipher []byte
}

type loginAttempt struct {
	failures int
	until    time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login.html", map[string]any{"AppName": a.cfg.AppName})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	admin, err := a.store.AdminUserByEmail(r.Context(), email)
	if err != nil || admin == nil || !admin.Enabled ||
		bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)) != nil {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, "login.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid email or password."}, http.StatusUnauthorized)
		return
	}
	if a.cfg.TOTPEnabled {
		a.beginTOTPLogin(w, r, "admin", &admin.ID, admin.Email, "/admin/login")
		return
	}
	a.completeAdminLogin(w, r, admin.ID)
}

// beginTOTPLogin starts the second factor of a login. When no TOTP secret is
// enrolled for the scope/subject it starts enrollment instead. The redirect
// carries an opaque single-use token; the pending row holds the enrollment
// secret cipher so the QR page can render after a refresh.
func (a *App) beginTOTPLogin(w http.ResponseWriter, r *http.Request, scope string, subjectID *uuid.UUID, account, fallbackPath string) {
	pending, err := a.newTOTPPending(r.Context(), scope, subjectID)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "start totp login", "scope", scope, "error", err)
		http.Error(w, "security setup error", http.StatusInternalServerError)
		return
	}
	next := "/admin/login/totp"
	if scope == "merchant" {
		next = "/merchant/login/totp"
	}
	q := url.Values{"token": {pending.Token}, "scope": {scope}, "account": {account}}
	http.Redirect(w, r, next+"?"+q.Encode(), http.StatusSeeOther)
}

// completeAdminLogin issues the admin session cookie.
func (a *App) completeAdminLogin(w http.ResponseWriter, r *http.Request, adminID uuid.UUID) {
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateAdminSession(r.Context(), adminID, token, csrf, time.Now().Add(a.cfg.AuthSessionTTL)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: adminCookieName, Value: token, Path: "/admin", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/admin/metrics", http.StatusSeeOther)
}

// newTOTPPending creates a short-lived second-factor step. The secret cipher
// is stored in the pending row only when enrolling (no secret exists yet);
// otherwise verification loads the persisted secret by scope/subject.
func (a *App) newTOTPPending(ctx context.Context, scope string, subjectID *uuid.UUID) (totpPending, error) {
	existing, err := a.store.GetTOTPSecret(ctx, scope, subjectID)
	if err != nil {
		return totpPending{}, err
	}
	var secretCipher []byte
	if existing == nil {
		key, err := totp.Generate(a.cfg.AppName, "login")
		if err != nil {
			return totpPending{}, err
		}
		secretCipher, err = totp.EncryptSecret(a.totpKey, key.Secret())
		if err != nil {
			return totpPending{}, err
		}
	}
	token, err := randomToken(32)
	if err != nil {
		return totpPending{}, err
	}
	hash := sha256.Sum256([]byte(token))
	if err := a.store.CreateTOTPPendingLogin(ctx, hash[:], scope, subjectID, secretCipher, time.Now().Add(10*time.Minute)); err != nil {
		return totpPending{}, err
	}
	return totpPending{Token: token, Scope: scope, SubjectID: subjectID, SecretCipher: secretCipher}, nil
}

// totpPage renders the second-factor page: QR + secret during enrollment,
// code field only when a secret is already enrolled.
func (a *App) totpPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	loginPath := "/admin/login"
	if token == "" {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	hash := sha256.Sum256([]byte(token))
	scope, _, secretCipher, found, err := a.store.GetTOTPPendingLogin(r.Context(), hash[:])
	if err != nil || !found {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	if scope == "merchant" {
		loginPath = "/merchant/login"
	}
	data := map[string]any{
		"AppName":    a.cfg.AppName,
		"Title":      "Two-step login",
		"Token":      token,
		"Scope":      scope,
		"Account":    account,
		"VerifyPath": "/admin/login/totp",
		"CancelPath": loginPath,
	}
	if secretCipher != nil {
		secret, err := totp.DecryptSecret(a.totpKey, secretCipher)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "decrypt totp secret", "error", err)
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		keyURI := totp.KeyURI(a.cfg.AppName, account, secret)
		png, err := qrcode.Encode(keyURI, qrcode.Medium, 220)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "encode totp qr", "error", err)
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		data["Secret"] = secret
		data["QR"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	if scope == "merchant" {
		data["VerifyPath"] = "/merchant/login/totp"
		a.render(w, "merchant_totp.html", data)
		return
	}
	a.render(w, "admin_totp.html", data)
}

// totpVerify consumes the pending login and issues the session after a valid code.
func (a *App) totpVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	code := strings.TrimSpace(r.FormValue("code"))
	if token == "" || code == "" {
		http.Error(w, "missing token or code", http.StatusBadRequest)
		return
	}
	hash := sha256.Sum256([]byte(token))
	scope, subjectID, secretCipher, found, err := a.store.ConsumeTOTPPendingLogin(r.Context(), hash[:])
	loginTemplate := "login.html"
	if scope == "merchant" {
		loginTemplate = "merchant_login.html"
	}
	if err != nil || !found {
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "This login step has expired. Please sign in again."}, http.StatusUnauthorized)
		return
	}
	secret := ""
	if secretCipher != nil {
		secret, err = totp.DecryptSecret(a.totpKey, secretCipher)
	} else {
		var cipher []byte
		cipher, err = a.store.GetTOTPSecret(r.Context(), scope, subjectID)
		if err == nil && cipher == nil {
			err = errors.New("totp secret missing")
		}
		if err == nil {
			secret, err = totp.DecryptSecret(a.totpKey, cipher)
		}
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load totp secret", "scope", scope, "error", err)
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Security error. Please sign in again."}, http.StatusInternalServerError)
		return
	}
	if !totp.Validate(code, secret) {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid authentication code."}, http.StatusUnauthorized)
		return
	}
	if secretCipher != nil {
		if err := a.store.SetTOTPSecret(r.Context(), scope, subjectID, secretCipher); err != nil {
			a.logger.ErrorContext(r.Context(), "persist totp secret", "scope", scope, "error", err)
			a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Security error. Please sign in again."}, http.StatusInternalServerError)
			return
		}
		a.auditTOTPEnrollment(r, scope, subjectID)
	}
	if scope == "merchant" {
		a.completeMerchantLoginWithTOTP(w, r, subjectID)
		return
	}
	if subjectID == nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.completeAdminLogin(w, r, *subjectID)
}

// completeMerchantLoginWithTOTP issues the merchant session cookie after the
// second factor passed for the given merchant subject.
func (a *App) completeMerchantLoginWithTOTP(w http.ResponseWriter, r *http.Request, merchantID *uuid.UUID) {
	if merchantID == nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	ownerID, err := a.store.MerchantOwnerID(r.Context(), *merchantID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateMerchantSession(r.Context(), *merchantID, ownerID, token, csrf, time.Now().Add(a.cfg.AuthSessionTTL)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/merchant/scanner", http.StatusSeeOther)
}

// totpDisable removes the enrolled secret, turning TOTP off for the subject.
func (a *App) totpDisable(w http.ResponseWriter, r *http.Request, scope string, subjectID *uuid.UUID, redirectTo string) {
	if scope == "admin" && r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if scope == "merchant" && r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.DeleteTOTPSecret(r.Context(), scope, subjectID); err != nil {
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	a.audit(r, scope+".totp_disabled", scope, subjectID.String(), nil)
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// auditTOTPEnrollment records a first-time TOTP enrollment. It runs during the
// login flow, before any session exists, so the subject is taken from the
// pending login rather than the request context.
func (a *App) auditTOTPEnrollment(r *http.Request, scope string, subjectID *uuid.UUID) {
	if subjectID == nil {
		return
	}
	entry := store.AuditLog{
		ActorType:  scope,
		ActorID:    uuid.NullUUID{UUID: *subjectID, Valid: true},
		Action:     scope + ".totp_enrolled",
		ResourceID: sql.NullString{String: subjectID.String(), Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
		Details:    map[string]any{},
	}
	if scope == "admin" {
		if admin, err := a.store.AdminUserByID(r.Context(), *subjectID); err == nil && admin != nil {
			entry.ActorEmail = sql.NullString{String: admin.Email, Valid: true}
		}
	}
	if _, err := a.store.AppendAuditLog(r.Context(), entry); err != nil {
		a.logger.ErrorContext(r.Context(), "audit log write failed", "action", entry.Action, "error", err)
	}
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(adminCookieName); err == nil {
		_ = a.store.DeleteAdminSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Path: "/admin", MaxAge: -1, HttpOnly: true, Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (a *App) merchantLogin(w http.ResponseWriter, r *http.Request) {
	a.render(w, "merchant_login.html", map[string]any{"AppName": a.cfg.AppName})
}

func (a *App) merchantLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	merchant, err := a.store.MerchantByEmail(r.Context(), email)
	if err != nil || merchant.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(merchant.PasswordHash), []byte(password)) != nil {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, "merchant_login.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid email or password."}, http.StatusUnauthorized)
		return
	}
	if a.cfg.TOTPEnabled {
		a.beginTOTPLogin(w, r, "merchant", &merchant.ID, email, "/merchant/login")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	ownerID, err := a.store.MerchantOwnerID(r.Context(), merchant.ID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateMerchantSession(r.Context(), merchant.ID, ownerID, token, csrf, time.Now().Add(a.cfg.AuthSessionTTL)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/merchant/scanner", http.StatusSeeOther)
}

func (a *App) merchantLogout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(merchantCookieName); err == nil {
		_ = a.store.DeleteMerchantSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: merchantCookieName, Path: "/merchant", MaxAge: -1, HttpOnly: true, Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/merchant/login", http.StatusSeeOther)
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string]loginAttempt{}}
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[ip]
	attempt.failures++
	if attempt.failures >= 5 {
		attempt.until = time.Now().Add(15 * time.Minute)
	}
	l.attempts[ip] = attempt
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func (l *loginLimiter) retryAfter(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[ip]
	if time.Now().After(attempt.until) {
		if !attempt.until.IsZero() {
			delete(l.attempts, ip)
		}
		return 0
	}
	return time.Until(attempt.until)
}
