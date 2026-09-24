package app

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
)

type loginAttempt struct {
	failures int
	until    time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

// mfaChallengeMaxAttempts bounds how many wrong codes or assertions one
// second-factor step accepts before it is discarded and the subject must sign
// in again.
const mfaChallengeMaxAttempts = 5

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
	if a.secondFactorRequired(r.Context(), store.MFAScopeAdmin, admin.ID) {
		a.beginSecondFactor(w, r, store.MFAScopeAdmin, admin.ID, admin.Email)
		return
	}
	a.completeAdminLogin(w, r, admin.ID)
}

// totpAvailable reports whether authenticator codes can be checked on this
// deployment: the encryption key is what seals and opens a stored shared secret.
func (a *App) totpAvailable() bool {
	return len(a.totpKey) > 0
}

// secondFactorRequired reports whether a sign-in must pass a second step. The
// deployment flag arms the requirement for everyone; an account that has
// enrolled a factor is challenged even when the flag is off, so switching the
// flag cannot silently strip protection a subject already has.
func (a *App) secondFactorRequired(ctx context.Context, scope string, subjectID uuid.UUID) bool {
	if a.cfg.TOTPEnabled {
		return true
	}
	factors, err := a.usableFactors(ctx, scope, subjectID)
	if err != nil {
		a.logger.ErrorContext(ctx, "load second factors", "scope", scope, "error", err)
		return false
	}
	return len(factors) > 0
}

// issueSession creates the portal session for a subject, sets its cookie, and
// returns the page the subject lands on. Callers answering in HTML redirect;
// callers answering in JSON hand the path to the browser.
func (a *App) issueSession(w http.ResponseWriter, r *http.Request, scope string, subjectID uuid.UUID) (string, error) {
	ttl := a.cfg.AuthSessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return "", err
	}
	expiry := time.Now().Add(ttl)
	if scope == store.MFAScopeMerchant {
		ownerID, err := a.store.MerchantOwnerID(r.Context(), subjectID)
		if err != nil {
			return "", err
		}
		if err := a.store.CreateMerchantSession(r.Context(), subjectID, ownerID, token, csrf, expiry); err != nil {
			return "", err
		}
		http.SetCookie(w, &http.Cookie{
			Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
			Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode,
			MaxAge: int(ttl.Seconds()),
		})
		return "/merchant/scanner", nil
	}
	if err := a.store.CreateAdminSession(r.Context(), subjectID, token, csrf, expiry); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminCookieName, Value: token, Path: "/admin", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode,
		MaxAge: int(ttl.Seconds()),
	})
	return "/admin/metrics", nil
}

// completeAdminLogin issues the admin session cookie and lands on metrics.
func (a *App) completeAdminLogin(w http.ResponseWriter, r *http.Request, adminID uuid.UUID) {
	landing, err := a.issueSession(w, r, store.MFAScopeAdmin, adminID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.Redirect(w, r, landing, http.StatusSeeOther)
}

// completeMerchantLogin issues the merchant session cookie and lands on the
// scanner.
func (a *App) completeMerchantLogin(w http.ResponseWriter, r *http.Request, merchantID uuid.UUID) {
	landing, err := a.issueSession(w, r, store.MFAScopeMerchant, merchantID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.Redirect(w, r, landing, http.StatusSeeOther)
}

// auditMFAEvent records a second-factor lifecycle event. Enrollment runs during
// sign-in, before any session exists, so the subject comes from the step rather
// than from the request context.
func (a *App) auditMFAEvent(r *http.Request, scope string, subjectID uuid.UUID, action string, factorID int64) {
	entry := store.AuditLog{
		ActorType:  scope,
		ActorID:    uuid.NullUUID{UUID: subjectID, Valid: true},
		Action:     scope + "." + action,
		ResourceID: sql.NullString{String: subjectID.String(), Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
		Details:    map[string]any{"factor_id": factorID},
	}
	if email := a.subjectEmail(r.Context(), scope, subjectID); email != "" {
		entry.ActorEmail = sql.NullString{String: email, Valid: true}
	}
	if _, err := a.store.AppendAuditLog(r.Context(), entry); err != nil {
		a.logger.ErrorContext(r.Context(), "audit log write failed", "action", entry.Action, "error", err)
	}
}

// subjectEmail reads the address a portal account signs in with, which is also
// the destination of its emailed codes. It returns "" when the account has no
// address or cannot be read.
func (a *App) subjectEmail(ctx context.Context, scope string, subjectID uuid.UUID) string {
	if scope == store.MFAScopeMerchant {
		ownerID, err := a.store.MerchantOwnerID(ctx, subjectID)
		if err != nil {
			return ""
		}
		owner, err := a.store.UserByID(ctx, ownerID)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(owner.Email)
	}
	admin, err := a.store.AdminUserByID(ctx, subjectID)
	if err != nil || admin == nil {
		return ""
	}
	return strings.TrimSpace(admin.Email)
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
	if a.secondFactorRequired(r.Context(), store.MFAScopeMerchant, merchant.ID) {
		a.beginSecondFactor(w, r, store.MFAScopeMerchant, merchant.ID, email)
		return
	}
	a.completeMerchantLogin(w, r, merchant.ID)
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
