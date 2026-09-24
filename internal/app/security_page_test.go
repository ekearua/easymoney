package app

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
)

// recordingMailer captures emailed sign-in codes so enrollment can be driven
// end to end without a real SMTP server.
type recordingMailer struct {
	mu     sync.Mutex
	mails  []string
	codes  []string
	refuse bool
}

func (m *recordingMailer) Send(_ context.Context, to, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuse {
		return context.DeadlineExceeded
	}
	m.mails = append(m.mails, to)
	// The body carries the code as its longest digit run.
	code := ""
	run := ""
	for _, r := range body {
		if r >= '0' && r <= '9' {
			run += string(r)
			if len(run) > len(code) {
				code = run
			}
			continue
		}
		run = ""
	}
	m.codes = append(m.codes, code)
	return nil
}

func (m *recordingMailer) lastCode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.codes) == 0 {
		return ""
	}
	return m.codes[len(m.codes)-1]
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.mails)
}

// TestEmailFactorEnrollmentAndSignin drives the emailed second factor end to
// end: enroll from the security page with a code that actually arrives, then
// sign in with the factor and resend a code before finishing.
func TestEmailFactorEnrollmentAndSignin(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated test in short mode")
	}
	ctx := context.Background()
	repository, err := store.Open(ctx, simTestDBURL(t, base))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	const email = "mfa-email-admin@xego.test"
	hash, err := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureAdminUser(ctx, email, string(hash)); err != nil {
		t.Fatal(err)
	}
	admin, err := repository.AdminUserByEmail(ctx, email)
	if err != nil || admin == nil {
		t.Fatalf("admin lookup: %v", err)
	}
	clearMFAState(t, ctx, repository, "admin", admin.ID.String())

	mailer := &recordingMailer{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{
		cfg: config.Config{
			AppName:     "Xego",
			Environment: "test",
			// TOTP_ENABLED is off on purpose: enrollment must work from the
			// security page alone, and the factor itself must then require a
			// second step at sign-in.
			TOTPEnabled:              false,
			AuthSessionTTL:           12 * time.Hour,
			RateLimitPublicPerMinute: 6000,
		},
		logger:      logger,
		store:       repository,
		templates:   parseTOTPTestTemplates(t),
		limiter:     newLoginLimiter(),
		rateLimiter: ratelimit.NewMemory(),
		email:       mailer,
	}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// First password-only login issues the admin session (no factors yet).
	resp := postForm(t, client, srv.URL+"/admin/login", url.Values{"email": {email}, "password": {"admin-pass"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first login status = %d, want 303", resp.StatusCode)
	}

	// Open the security page and start emailed-code enrollment.
	page := getResponse(t, client, srv.URL+"/admin/security")
	if !strings.Contains(page.body, "Sign-in security") {
		t.Fatalf("security page did not render, got %s", snippet(page.body))
	}
	csrf := securityCSRF(t, page.body)
	enroll := postForm(t, client, srv.URL+"/admin/security/email", url.Values{
		"csrf_token": {csrf},
	})
	enrollBody := drainBody(t, enroll.Body)
	enroll.Body.Close()
	if enroll.StatusCode != http.StatusOK {
		t.Fatalf("email enrollment start status = %d (%s)", enroll.StatusCode, snippet(enrollBody))
	}
	if mailer.count() != 1 {
		t.Fatalf("one code must be mailed at enrollment start, sent %d", mailer.count())
	}
	token := stepToken(t, enrollBody)

	// A wrong code is retryable; the right one confirms the factor.
	bad := postForm(t, client, srv.URL+"/admin/security/email/verify", url.Values{
		"csrf_token": {csrf}, "token": {token}, "code": {"000000"},
	})
	bad.Body.Close()
	if bad.StatusCode == http.StatusSeeOther {
		t.Fatal("a wrong code must not confirm the factor")
	}
	good := postForm(t, client, srv.URL+"/admin/security/email/verify", url.Values{
		"csrf_token": {csrf}, "token": {token}, "code": {mailer.lastCode()},
	})
	goodBody := drainBody(t, good.Body)
	good.Body.Close()
	if good.StatusCode != http.StatusSeeOther {
		t.Fatalf("code verify status = %d, want 303 (%s)", good.StatusCode, snippet(goodBody))
	}

	// The factor now exists and forces a second step at sign-in.
	factors, err := repository.MFAFactors(ctx, store.MFAScopeAdmin, admin.ID)
	if err != nil || len(factors) != 1 || factors[0].Kind != store.MFAKindEmail {
		t.Fatalf("email factor must be enrolled and confirmed, got %v err=%v", factors, err)
	}
	second := postForm(t, client, srv.URL+"/admin/login", url.Values{"email": {email}, "password": {"admin-pass"}})
	defer second.Body.Close()
	if second.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in with a factor must step up, got %d", second.StatusCode)
	}
	location := second.Header.Get("Location")
	parsed := parseLocation(t, location)
	stepToken := parsed.Query().Get("token")
	if stepToken == "" {
		t.Fatal("the second-factor redirect must carry a step token")
	}

	// The step opened on the email factor (no TOTP is enrolled) and mailed a
	// fresh code.
	if mailer.count() != 2 {
		t.Fatalf("sign-in step must mail a code, sent %d total", mailer.count())
	}
	resend := postForm(t, client, srv.URL+"/admin/login/2fa", url.Values{
		"token": {stepToken}, "account": {email}, "action": {"resend"},
	})
	resend.Body.Close()
	if resend.StatusCode != http.StatusOK {
		t.Fatalf("resend status = %d, want 200", resend.StatusCode)
	}
	if mailer.count() != 3 {
		t.Fatalf("resend must mail a fresh code, sent %d total", mailer.count())
	}
	finish := postForm(t, client, srv.URL+"/admin/login/2fa", url.Values{
		"token": {stepToken}, "account": {email}, "code": {mailer.lastCode()},
	})
	finishBody := drainBody(t, finish.Body)
	finish.Body.Close()
	if finish.StatusCode != http.StatusSeeOther || finish.Header.Get("Location") != "/admin/metrics" {
		t.Fatalf("finish status = %d location = %q body=%s", finish.StatusCode, finish.Header.Get("Location"), snippet(finishBody))
	}
	if !hasCookie(finish, adminCookieName) {
		t.Fatal("finishing the step must issue the admin session cookie")
	}
}

// TestFactorRevocationClearsLiveSteps enforces that revoking a factor clears
// any live step for the subject, so a stolen step token dies with the factor.
func TestFactorRevocationClearsLiveSteps(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated test in short mode")
	}
	ctx := context.Background()
	repository, err := store.Open(ctx, simTestDBURL(t, base))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	subject := uuid.New()
	factorID, err := repository.UpsertMFAFactor(ctx, store.MFAFactor{
		Scope: store.MFAScopeAdmin, SubjectID: subject, Kind: store.MFAKindEmail, Label: "a@b.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmMFAFactor(ctx, factorID); err != nil {
		t.Fatal(err)
	}
	token := "revoke-step-token"
	sum := sha256.Sum256([]byte(token))
	if err := repository.CreateMFAChallenge(ctx, sum[:], store.MFAChallenge{
		Scope: store.MFAScopeAdmin, SubjectID: subject, Method: store.MFAKindEmail,
		FactorID: &factorID, ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeleteMFAFactorsForSubject(ctx, store.MFAScopeAdmin, subject); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteMFAChallengesForSubject(ctx, store.MFAScopeAdmin, subject); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.MFAChallengeByToken(ctx, sum[:]); err != nil || found {
		t.Fatalf("revocation must clear live steps, found=%v err=%v", found, err)
	}
}

// TestPasskeysUnconfiguredWithoutOrigin guards the graceful degradation: a
// deployment without an origin BaseURL leaves webAuthn nil and the security
// page says so instead of offering a broken button.
func TestPasskeysUnconfiguredWithoutOrigin(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{cfg: config.Config{AppName: "Xego"}, logger: logger}
	if a.webAuthn != nil {
		t.Fatal("no BaseURL must leave passkeys unconfigured")
	}
	tmpl := parseTOTPTestTemplates(t)
	data := map[string]any{
		"Factors": []factorView{}, "Paths": securityRoutesFor("admin"),
		"TotpAvailable": false, "EmailAvailable": false, "PasskeysAvailable": false,
		"Enforced": false, "Enrolled": false, "Notice": "",
		"MFAMethods": []factorView{}, "MFAEnrolled": false, "MFAEnforced": false, "MFAPath": "/admin/security",
		"CSRF": "t",
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "security_panel", data); err != nil {
		t.Fatalf("render security panel: %v", err)
	}
	if strings.Contains(buf.String(), "Add a passkey") {
		t.Fatal("passkey enrollment must not be offered when unconfigured")
	}
	if !strings.Contains(buf.String(), "Passkeys need a deployment base URL") {
		t.Fatal("the unconfigured state must say why passkeys are off")
	}
	_ = ports.EmailSender(nil)
}

// --- helpers --------------------------------------------------------------

func securityCSRF(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("security page carries no csrf token")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("security page csrf token is unterminated")
	}
	return rest[:j]
}

func stepToken(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("step page carries no token")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("step token is unterminated")
	}
	return rest[:j]
}

func parseLocation(t *testing.T, location string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse location %q: %v", location, err)
	}
	return parsed
}
