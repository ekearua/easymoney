package app

// TestStandingAccountCreditWebhook proves the reusable standing-account rail
// end to end through the real HTTP webhook route: a signed
// TRANSACTION.COMPLETED notification that carries a standing account number
// credits the owner's wallet exactly once; a retried delivery — first the
// byte-identical replay, then a fresh delivery id carrying the same transaction
// reference — never credits twice; and a notification for an account number
// nobody reserved is a no-op.
//
// The event deliberately carries no payment reference: a standing-account
// credit is not payment-scoped, so neither the payment requery nor the
// payment-owned web-flow completion may gate it.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestStandingAccountCreditWebhook -v

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// standingCreditWebhookSecret is the dashboard webhook secret the test client
// is configured with; every notification below is signed with it.
const standingCreditWebhookSecret = "test-interswitch-webhook-secret"

// newStandingCreditApp wires the real Interswitch client (with a webhook
// secret) against the real store and a simulated payment gateway, so the
// /webhooks/interswitch route validates signatures exactly as production does
// while payment requeries are served locally.
func newStandingCreditApp(t *testing.T, repository *store.Store, cfg config.Config) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)
	a := &App{
		cfg: cfg, logger: logger, store: repository,
		payments: payments,
		interswitch: interswitch.New(interswitch.Options{
			MerchantCode: "M1000", PayItemID: "pi",
			BaseURL:       "https://sandbox.interswitchng.com",
			Mode:          "TEST",
			WebhookSecret: standingCreditWebhookSecret,
		}),
		rateLimiter: ratelimit.NewMemory(),
	}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return srv
}

// standingCreditEvent is the notification body shape the standing-credit
// ingest reads: a success event plus the reserved account number and amount.
type standingCreditEvent struct {
	UUID              string `json:"uuid"`
	Event             string `json:"event"`
	Timestamp         int64  `json:"timestamp"`
	Reference         string `json:"reference"`
	ResolverReference string `json:"resolverReference"`
	Amount            int64  `json:"transactionAmount"`
}

// postStandingCredit signs and posts one notification, returning the status.
func postStandingCredit(t *testing.T, srv *httptest.Server, event standingCreditEvent) int {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	mac := hmac.New(sha512.New, []byte(standingCreditWebhookSecret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/interswitch", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Interswitch-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStandingAccountCreditWebhook(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the standing-credit webhook test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated standing-credit webhook test in short mode")
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
	cfg := config.Config{
		AppName: "Xego", BaseURL: "https://demo.xego.ng",
		PaymentMinKobo: 10_000, PaymentMaxKobo: 1_000_000_000,
		RateLimitWebhooksPerMinute: 6000, RateLimitPublicPerMinute: 6000,
		// Bank credits must reach the same transaction-monitoring rules as
		// card payments: three sub-threshold credits raise a structuring alert.
		MonitorStructuringWindow: time.Hour, MonitorStructuringCount: 3, MonitorStructuringFloor: 100_000,
	}
	srv := newStandingCreditApp(t, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348095550001")
	if err != nil {
		t.Fatal(err)
	}
	account, err := repository.EnsureStandingAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(account.AccountNumber, "9029") {
		t.Fatalf("standing account should be a 9029-* reservation, got %q", account.AccountNumber)
	}
	wallet, err := repository.EnsureUserWallet(ctx, user.ID, "Standing credit payer")
	if err != nil {
		t.Fatal(err)
	}
	balance := func() int64 {
		t.Helper()
		got, err := repository.WalletBalance(ctx, wallet.ID)
		if err != nil {
			t.Fatalf("wallet balance: %v", err)
		}
		return got
	}
	if start := balance(); start != 0 {
		t.Fatalf("fresh wallet should start at 0, got %d", start)
	}

	// 1. A signed success notification for the reserved number credits the
	// wallet exactly once.
	credit := standingCreditEvent{
		UUID: uuid.NewString(), Event: "TRANSACTION.COMPLETED", Timestamp: time.Now().Unix(),
		Reference: "STD-CREDIT-0001", ResolverReference: account.AccountNumber, Amount: 250_000,
	}
	if status := postStandingCredit(t, srv, credit); status != http.StatusOK {
		t.Fatalf("credit notification: want 200, got %d", status)
	}
	if got := balance(); got != 250_000 {
		t.Fatalf("wallet should be credited 250000 once, got %d", got)
	}

	// 2. The byte-identical replay (same delivery) is ignored wholesale.
	if status := postStandingCredit(t, srv, credit); status != http.StatusOK {
		t.Fatalf("identical replay: want 200, got %d", status)
	}
	if got := balance(); got != 250_000 {
		t.Fatalf("identical replay must not credit again, balance %d", got)
	}

	// 3. A fresh delivery id (new uuid) carrying the same transaction reference
	// is still recognised as the same credit by the standing_credits guard.
	replayed := credit
	replayed.UUID = uuid.NewString()
	if status := postStandingCredit(t, srv, replayed); status != http.StatusOK {
		t.Fatalf("same-reference redelivery: want 200, got %d", status)
	}
	if got := balance(); got != 250_000 {
		t.Fatalf("same-reference redelivery must not credit again, balance %d", got)
	}

	// 4. A notification for an unreserved account number is a no-op: the route
	// still answers 200 (no retry storm) and no wallet is touched.
	unknown := standingCreditEvent{
		UUID: uuid.NewString(), Event: "TRANSACTION.COMPLETED", Timestamp: time.Now().Unix(),
		Reference: "STD-CREDIT-0002", ResolverReference: "9999000001", Amount: 100_000,
	}
	if status := postStandingCredit(t, srv, unknown); status != http.StatusOK {
		t.Fatalf("unknown-account notification: want 200, got %d", status)
	}
	if got := balance(); got != 250_000 {
		t.Fatalf("unknown account must not credit anyone, balance %d", got)
	}

	// 5. A notification that settles nothing (zero amount) is also a no-op.
	zero := credit
	zero.UUID = uuid.NewString()
	zero.Reference = "STD-CREDIT-0003"
	zero.Amount = 0
	if status := postStandingCredit(t, srv, zero); status != http.StatusOK {
		t.Fatalf("zero-amount notification: want 200, got %d", status)
	}
	if got := balance(); got != 250_000 {
		t.Fatalf("zero-amount notification must not credit, balance %d", got)
	}

	// 6. Credits run through the transaction-monitoring rules: three applied
	// bank credits in the window raise exactly one structuring alert, tagged
	// with the standing-credit rail, so a pattern cannot hide by switching
	// rails. A rescan of the same window must not duplicate it.
	for _, reference := range []string{"STD-CREDIT-0004", "STD-CREDIT-0005"} {
		next := credit
		next.UUID = uuid.NewString()
		next.Reference = reference
		if status := postStandingCredit(t, srv, next); status != http.StatusOK {
			t.Fatalf("credit %s: want 200, got %d", reference, status)
		}
	}
	if got := balance(); got != 750_000 {
		t.Fatalf("three bank credits should total 750000, got %d", got)
	}
	alerts, err := repository.ListTransactionAlerts(ctx, "open", 20)
	if err != nil {
		t.Fatalf("list transaction alerts: %v", err)
	}
	structuring, railTagged := 0, false
	for _, alert := range alerts {
		if alert.Rule != kyc.RuleStructuring {
			continue
		}
		structuring++
		if rails, ok := alert.Details["rails"].([]any); ok {
			for _, rail := range rails {
				if rail == kyc.RailStandingCredit {
					railTagged = true
				}
			}
		}
	}
	if structuring != 1 {
		t.Fatalf("want exactly one structuring alert from the bank credits, got %d: %+v", structuring, alerts)
	}
	if !railTagged {
		t.Fatalf("structuring alert must carry the standing_credit rail: %+v", alerts)
	}
	raised, err := repository.RunTransactionMonitor(ctx, time.Now().Add(-time.Hour), kyc.MonitorConfig{
		StructuringWindow: time.Hour, StructuringCount: 3, StructuringFloor: 100_000,
	})
	if err != nil {
		t.Fatalf("rescan transaction monitor: %v", err)
	}
	if raised != 0 {
		t.Fatalf("rescan raised %d duplicate alerts, want 0", raised)
	}
}
