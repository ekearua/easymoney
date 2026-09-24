package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	pqtotp "github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
	"whatsapp-payment-demo/web"
)

// TestSecondFactorPageRendersScannableQR guards the empty-QR regression. The
// enrollment image source is a PNG data URI, and html/template's URL filter
// rewrites every scheme other than http, https, and mailto to #ZgotmplZ unless
// the value is typed template.URL. The rendered src must therefore be a
// decodable PNG data URI — not a defanged placeholder and not a broken image
// box — while the manual secret stays on the page for anyone who cannot scan.
func TestSecondFactorPageRendersScannableQR(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x2b}, 32)
	a := &App{
		cfg:     config.Config{AppName: "Xego"},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		totpKey: key,
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"date": func(any) string { return "" },
	}).ParseFS(web.Assets,
		"templates/mfa_step.html", "templates/security_panel.html",
		"templates/nav.html", "templates/merchant_nav.html")
	if err != nil {
		t.Fatalf("parse two-step templates: %v", err)
	}
	generated, err := totp.Generate("Xego", "owner@xego.test")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := totp.EncryptSecret(key, generated.Secret())
	if err != nil {
		t.Fatal(err)
	}

	for _, scope := range []string{"admin", "merchant"} {
		scope := scope
		t.Run(scope, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/login/2fa?token=tok", nil)
			challenge := store.MFAChallenge{
				Scope:        scope,
				Method:       store.MFAKindTOTP,
				SecretCipher: cipher,
			}
			data := a.secondFactorRenderData(request, challenge, "tok", "owner@xego.test", "", "", signInCopy(scope))
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, "mfa_step.html", data); err != nil {
				t.Fatalf("execute mfa_step.html: %v", err)
			}
			rendered := buf.String()
			if strings.Contains(rendered, "ZgotmplZ") {
				t.Fatalf("the QR data URI was filtered; the value must be a template.URL")
			}
			// The escaper rewrites "+" as "&#43;" inside the attribute, which the
			// browser decodes; unescape before decoding the payload.
			uri := html.UnescapeString(firstSource(t, rendered))
			if !strings.HasPrefix(uri, "data:image/png;base64,") {
				t.Fatalf("QR src = %q, want a PNG data URI", uri)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
			if err != nil {
				t.Fatalf("QR payload is not base64: %v", err)
			}
			if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
				t.Fatalf("QR payload is not a decodable PNG: %v", err)
			}
			if !strings.Contains(rendered, generated.Secret()) {
				t.Fatalf("the manual secret must be shown for people who cannot scan")
			}
		})
	}
}

// TestSecondFactorLoginFlow drives the real routes of both portals: password
// first, the enrollment QR and manual secret shown once, a mistyped code
// retried against the same live step, then the session issued. It pins the two
// defects fixed together — an unrenderable QR, and a wrong code that destroyed
// the pending step and with it every retry.
func TestSecondFactorLoginFlow(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the two-step login test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated two-step login test in short mode")
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

	key := bytes.Repeat([]byte{0x5a}, 32)
	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  "https://xego.test",
		Environment:              "test", // non-production: session cookie is not Secure
		TOTPEnabled:              true,
		TOTPEncryptionKey:        hex.EncodeToString(key),
		AuthSessionTTL:           12 * time.Hour,
		SessionTTL:               30 * time.Minute,
		RateLimitPublicPerMinute: 6000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{
		cfg:         cfg,
		logger:      logger,
		store:       repository,
		templates:   parseTOTPTestTemplates(t),
		limiter:     newLoginLimiter(),
		rateLimiter: ratelimit.NewMemory(),
		totpKey:     key,
	}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	const adminEmail = "totp-flow-admin@xego.test"
	adminHash, err := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureAdminUser(ctx, adminEmail, string(adminHash)); err != nil {
		t.Fatal(err)
	}
	admin, err := repository.AdminUserByEmail(ctx, adminEmail)
	if err != nil || admin == nil {
		t.Fatalf("admin lookup: %v", err)
	}
	clearMFAState(t, ctx, repository, "admin", admin.ID.String())

	t.Run("admin", func(t *testing.T) {
		twoStepLogin(t, client, srv.URL, "/admin/login", "/admin/login/2fa",
			adminEmail, "admin-pass", adminCookieName, "/admin/metrics")
	})

	t.Run("merchant", func(t *testing.T) {
		merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
		if err != nil {
			t.Fatalf("seeded merchant: %v", err)
		}
		ownerID, err := repository.MerchantOwnerID(ctx, merchant.ID)
		if err != nil {
			t.Fatalf("merchant owner: %v", err)
		}
		owner, err := repository.UserByID(ctx, ownerID)
		if err != nil {
			t.Fatalf("owner user: %v", err)
		}
		if owner.Email == "" {
			t.Skip("the seeded merchant owner has no email address to sign in with")
		}
		password, err := bcrypt.GenerateFromPassword([]byte("merchant-pass"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		// MerchantByEmail returns the OLDEST owner match, and several seeded
		// merchants share the owner email; set the password on every row the
		// lookup can land on so the walk signs into whichever it gets.
		sameEmail, err := repository.MerchantsIDsWithOwnerEmail(ctx, owner.Email)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range sameEmail {
			if err := repository.UpdateMerchantPassword(ctx, id, string(password)); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range sameEmail {
			clearMFAState(t, ctx, repository, "merchant", id.String())
		}
		twoStepLogin(t, client, srv.URL, "/merchant/login", "/merchant/login/2fa",
			owner.Email, "merchant-pass", merchantCookieName, "/merchant/scanner")
	})
}

// twoStepLogin walks one portal's second-factor journey end to end.
func twoStepLogin(t *testing.T, client *http.Client, base, loginPath, verifyPath, email, password, cookieName, landing string) {
	t.Helper()
	resp := postForm(t, client, base+loginPath, url.Values{"email": {email}, "password": {password}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303 (%s)", resp.StatusCode, snippet(drainBody(t, resp.Body)))
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, verifyPath+"?") {
		t.Fatalf("login redirect = %q, want %s", location, verifyPath)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	account := parsed.Query().Get("account")
	if token == "" {
		t.Fatal("the login redirect must carry a single-use step token")
	}

	page := getResponse(t, client, base+location)
	if page.status != http.StatusOK {
		t.Fatalf("two-step page status = %d, want 200", page.status)
	}
	if !strings.Contains(page.body, `src="data:image/png;base64,`) {
		t.Fatalf("the enrollment step must render a scannable QR, got %s", snippet(page.body))
	}
	if strings.Contains(page.body, "ZgotmplZ") {
		t.Fatal("the QR data URI was defanged by the template URL filter")
	}
	secret := secretFromPage(t, page.body)

	// A mistyped code must be retryable: the step stays live and the form
	// keeps its token so the customer can simply retype the digits. The login
	// limiter's failure counter is shared per test server, so each portal walk
	// runs against a client the limiter has not seen yet.
	bad := postForm(t, client, base+verifyPath, url.Values{
		"token": {token}, "account": {account}, "code": {nonMatchingCode(t, secret)},
	})
	badBody := drainBody(t, bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-code status = %d, want 401 (%s)", bad.StatusCode, snippet(badBody))
	}
	if !strings.Contains(badBody, "Invalid authentication code.") {
		t.Fatalf("a wrong code must be reported as invalid, got %s", snippet(badBody))
	}
	if strings.Contains(badBody, "expired") {
		t.Fatal("a mistyped code must not expire the login step")
	}
	if !strings.Contains(badBody, `name="token"`) {
		t.Fatal("the retry page must keep the step token so the code can be retyped")
	}

	code, err := pqtotp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ok := postForm(t, client, base+verifyPath, url.Values{
		"token": {token}, "account": {account}, "code": {code},
	})
	okBody := drainBody(t, ok.Body)
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("verify status = %d, want 303 (%s)", ok.StatusCode, snippet(okBody))
	}
	if got := ok.Header.Get("Location"); got != landing {
		t.Fatalf("verify redirect = %q, want %q", got, landing)
	}
	if !hasCookie(ok, cookieName) {
		t.Fatalf("verify must issue the %s session cookie", cookieName)
	}

	// The step is single-use: replaying the same token cannot mint a second
	// session. The handler bounces a dead token back to the sign-in page
	// without rendering the step or issuing a cookie.
	replayClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	replay := postForm(t, replayClient, base+verifyPath, url.Values{
		"token": {token}, "account": {account}, "code": {code},
	})
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusSeeOther {
		t.Fatalf("replayed step status = %d, want 303", replay.StatusCode)
	}
	if got := replay.Header.Get("Location"); got != "/admin/login" && got != "/merchant/login" {
		t.Fatalf("replayed step redirect = %q, want the portal sign-in page", got)
	}
	if hasCookie(replay, cookieName) {
		t.Fatal("a replayed step must not issue a session cookie")
	}
}

// clearMFAState removes any enrollment and pending step left by an earlier run
// so the flow always starts at enrollment.
func clearMFAState(t *testing.T, ctx context.Context, repository *store.Store, scope, subjectID string) {
	t.Helper()
	for _, statement := range []string{
		fmt.Sprintf(`DELETE FROM mfa_challenges WHERE scope='%s' AND subject_id='%s'`, scope, subjectID),
		fmt.Sprintf(`DELETE FROM mfa_factors WHERE scope='%s' AND subject_id='%s'`, scope, subjectID),
		fmt.Sprintf(`DELETE FROM totp_secrets WHERE scope='%s' AND subject_id='%s'`, scope, subjectID),
		fmt.Sprintf(`DELETE FROM totp_pending_logins WHERE scope='%s' AND subject_id='%s'`, scope, subjectID),
	} {
		if _, err := repository.RawExec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}

func parseTOTPTestTemplates(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     func(s string) string { return s },
		"statusClass": func(any) string { return "" },
		"percent":     func(float64) string { return "" },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int) int { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
		"date":        func(any) string { return "" },
		"collectionFeeKobo": func(store.PaymentView) int64 {
			return 0
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return tmpl
}

type httpResult struct {
	status int
	body   string
}

func getResponse(t *testing.T, client *http.Client, url string) httpResult {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return httpResult{status: resp.StatusCode, body: drainBody(t, resp.Body)}
}

func postForm(t *testing.T, client *http.Client, url string, values url.Values) *http.Response {
	t.Helper()
	resp, err := client.Post(url, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func drainBody(t *testing.T, body io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(raw)
}

func hasCookie(resp *http.Response, name string) bool {
	for _, cookie := range resp.Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return true
		}
	}
	return false
}

// firstSource returns the first img src attribute value in a rendered page.
func firstSource(t *testing.T, rendered string) string {
	t.Helper()
	const marker = `src="`
	i := strings.Index(rendered, marker)
	if i < 0 {
		t.Fatal("rendered page has no img src")
	}
	rest := rendered[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("rendered page has an unterminated img src")
	}
	return rest[:j]
}

var secretPattern = regexp.MustCompile(`font-mono break-all">([A-Z2-7]+)</p>`)

// secretFromPage reads the base32 secret the enrollment page displays.
func secretFromPage(t *testing.T, body string) string {
	t.Helper()
	match := secretPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("enrollment page has no manual secret, got %s", snippet(body))
	}
	return match[1]
}

// nonMatchingCode returns a six-digit code that does not validate against the
// secret, so a "wrong code" assertion can never pass by accident.
func nonMatchingCode(t *testing.T, secret string) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("%06d", i)
		if !totp.Validate(candidate, secret) {
			return candidate
		}
	}
	t.Fatal("no non-matching code found")
	return ""
}

func snippet(body string) string {
	const limit = 400
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "…"
}
