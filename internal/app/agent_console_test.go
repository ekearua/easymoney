package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// TestAgentConsoleLoginAndRedemption drives the /agent console end to end: the
// session gate, sign-in, the per-payout redemption, the receipt, and the
// cross-agent receipt guard. It uses the same choke point the browser does, so a
// green here means the operator surface cannot bypass RedeemPickupTx.
func TestAgentConsoleLoginAndRedemption(t *testing.T) {
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

	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 1 {
		t.Skip("needs at least one seeded active merchant")
	}
	agent := merchants[0]
	if err := repository.SetMerchantType(ctx, agent.ID, store.MerchantTypeAgent); err != nil {
		t.Fatal(err)
	}
	region, err := repository.EnsureAgentRegion(ctx, store.AgentRegion{
		Code: "oyo-ibadan", Name: "Ibadan", State: "Oyo", LGA: "Ibadan North",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, store.AgentTierStandard); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentLimits(ctx, agent.ID, 500000, 100000, 200000, 50); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, store.AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	sender, err := repository.GetOrCreateUser(ctx, "+2348000000010")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := store.NewRemittanceCode()
	codeSalt, _ := store.NewRemittanceSalt()
	rem, _, err := repository.CreateRemittanceTx(ctx, store.CreateRemittanceParams{
		SenderUserID:    sender.ID,
		AgentMerchantID: agent.ID,
		RegionID:        region.ID,
		AmountKobo:      100000,
		FeeKobo:         2000,
		CommissionKobo:  500,
		RecipientName:   "Ada Obi",
		RecipientPhone:  "+2348000000011",
		PaymentID:       uuid.New(),
		CodeHash:        store.RemittanceCodeHash(codeSalt, code),
		CodeSalt:        codeSalt,
		ExpiresAt:       time.Now().Add(72 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	otp, _ := store.NewRemittanceCode()
	otpSalt, _ := store.NewRemittanceSalt()
	if err := repository.RequestRemittanceOTP(ctx, rem.ID,
		store.RemittanceCodeHash(otpSalt, otp), otpSalt, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pwHash, err := bcrypt.GenerateFromPassword([]byte("agent-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	const login = "agent-owner@xego.test"
	if _, err := repository.CreateAgentUser(ctx, store.AgentUser{
		AgentMerchantID: agent.ID, Name: "Ada Agent", Email: login, Phone: "+2348000000099",
		PasswordHash: string(pwHash),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		AppName:                  "Xego",
		Environment:              "test",
		AgentNetworkEnabled:      true,
		AgentIDRequired:          true,
		AgentIDHashPepper:        "test-pepper",
		AuthSessionTTL:           12 * time.Hour,
		RateLimitPublicPerMinute: 6000,
	}
	newApp := func(c config.Config) *App {
		return &App{
			cfg:             c,
			logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
			store:           repository,
			templates:       parseTOTPTestTemplates(t),
			limiter:         newLoginLimiter(),
			rateLimiter:     ratelimit.NewMemory(),
			agentRemittance: service.NewAgentRemittanceService(c, repository, nil),
		}
	}
	a := newApp(cfg)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// The console is closed until a session exists.
	gate := getResponse(t, client, srv.URL+"/agent/redeem")
	if gate.status != http.StatusSeeOther {
		t.Fatalf("unauthenticated /agent/redeem = %d, want a 303 redirect", gate.status)
	}

	// A wrong password is refused.
	bad := postForm(t, client, srv.URL+"/agent/login", url.Values{"email": {login}, "password": {"nope"}})
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", bad.StatusCode)
	}

	// A good login lands on the redemption page and issues the agent cookie.
	good := postForm(t, client, srv.URL+"/agent/login", url.Values{"email": {login}, "password": {"agent-pass"}})
	good.Body.Close()
	if good.StatusCode != http.StatusSeeOther || good.Header.Get("Location") != "/agent/redeem" {
		t.Fatalf("login status = %d location = %q, want 303 /agent/redeem", good.StatusCode, good.Header.Get("Location"))
	}
	if !hasCookie(good, agentCookieName) {
		t.Fatal("agent login must issue the agent session cookie")
	}

	page := getResponse(t, client, srv.URL+"/agent/redeem")
	if page.status != http.StatusOK || !strings.Contains(page.body, "Ada Obi") {
		t.Fatalf("redeem page = %d, want 200 listing the pickup (%s)", page.status, snippet(page.body))
	}
	csrf := securityCSRF(t, page.body)

	// A missing CSRF token is refused before the payout path runs.
	noCSRF := postForm(t, client, srv.URL+"/agent/redeem", url.Values{
		"code": {code}, "otp": {otp}, "id_type": {"nin"}, "id_number": {"12345678901"},
	})
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("redeem without csrf = %d, want 403", noCSRF.StatusCode)
	}

	// The happy path: code + OTP + ID settles and redirects to the receipt.
	redeem := postForm(t, client, srv.URL+"/agent/redeem", url.Values{
		"csrf_token": {csrf}, "code": {code}, "otp": {otp}, "id_type": {"nin"}, "id_number": {"12345678901"},
	})
	redeem.Body.Close()
	if redeem.StatusCode != http.StatusSeeOther || redeem.Header.Get("Location") != "/agent/receipt/"+rem.ID.String() {
		t.Fatalf("redeem status = %d location = %q, want 303 to the receipt", redeem.StatusCode, redeem.Header.Get("Location"))
	}

	receipt := getResponse(t, client, srv.URL+redeem.Header.Get("Location"))
	if receipt.status != http.StatusOK || !strings.Contains(receipt.body, "Cash pickup receipt") {
		t.Fatalf("receipt page = %d (%s)", receipt.status, snippet(receipt.body))
	}

	// A receipt that belongs to nobody (or another agent) is not disclosed.
	missing := getResponse(t, client, srv.URL+"/agent/receipt/"+uuid.New().String())
	if missing.status != http.StatusNotFound {
		t.Fatalf("foreign receipt = %d, want 404", missing.status)
	}

	// The kill switch turns off redemption without hiding the console.
	off := newApp(config.Config{AppName: "Xego", Environment: "test", AgentNetworkEnabled: false, RateLimitPublicPerMinute: 6000})
	offSrv := httptest.NewServer(off.routes())
	t.Cleanup(offSrv.Close)
	offJar, _ := cookiejar.New(nil)
	offClient := &http.Client{Jar: offJar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	offLogin := postForm(t, offClient, offSrv.URL+"/agent/login", url.Values{"email": {login}, "password": {"agent-pass"}})
	offLogin.Body.Close()
	if offLogin.StatusCode != http.StatusSeeOther {
		t.Fatalf("login while disabled = %d, want 303", offLogin.StatusCode)
	}
	offPage := getResponse(t, offClient, offSrv.URL+"/agent/redeem")
	if offPage.status != http.StatusOK || !strings.Contains(offPage.body, "Cash pickup is unavailable") {
		t.Fatalf("disabled redeem page = %d, want the unavailable notice (%s)", offPage.status, snippet(offPage.body))
	}
	offCSRF := securityCSRF(t, offPage.body)
	offRedeem := postForm(t, offClient, offSrv.URL+"/agent/redeem", url.Values{
		"csrf_token": {offCSRF}, "code": {code}, "otp": {otp}, "id_type": {"nin"}, "id_number": {"12345678901"},
	})
	offRedeem.Body.Close()
	if offRedeem.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("redeem while disabled = %d, want 503", offRedeem.StatusCode)
	}
}
