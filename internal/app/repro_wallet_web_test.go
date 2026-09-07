package app

// TestReproWalletWebPayment reproduces the production bug where paying a
// merchant from the wallet on the web "Review your payment" page failed
// silently with "Payment could not be completed. Please go back and try
// again." It drives the same /w/ path the demo runs: WhatsApp "make payment"
// link -> browser wizard -> review -> Pay from wallet.
//
// The test builds an L1 user with an ACTIVE, funded wallet and asserts the
// wallet step reports success AND that the payment reaches StatusSucceeded
// with the wallet debited. On failure it prints the raw logged error (the
// wfRoutePaymentFailed line is the only place the underlying cause is
// surfaced) so a red build pinpoints the failing store/service step.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestReproWalletWebPayment -v

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	dataprovider "whatsapp-payment-demo/internal/providers/data"
	identityprovider "whatsapp-payment-demo/internal/providers/identity"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestReproWalletWebPayment(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the simulation against a real PostgreSQL")
	}
	ctx := context.Background()

	databaseURL := simTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
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
		AppName:              "Xego",
		BaseURL:              "https://demo.xego.ng",
		WhatsAppPhoneNumber:  "2348000000000",
		WebFlowsEnabled:      true,
		MessageLogEnabled:    true,
		SessionTTL:           30 * time.Minute,
		PaymentMinKobo:       10_000,
		PaymentMaxKobo:       10_000_000,
		FeeCardBPS:           200,
		FeeCardFixedKobo:     10_000,
		FeeCardCapKobo:       350_000,
		FeeDVABPS:            150,
		FeeDVAFixedKobo:      0,
		FeeDVACapKobo:        150_000,
		FeeTransferBPS:       180,
		FeeTransferFixedKobo: 0,
		FeeTransferCapKobo:   250_000,
		FeeNIPPayoutFlatKobo: 10_000, WhatsAppTemplateName: "payment_status_update",
		WhatsAppTemplateLocale:     "en",
		RateLimitPublicPerMinute:   600,
		RateLimitWebhooksPerMinute: 600,
		RateLimitScanPerMinute:     600,
		RateLimitAPIKeysPerMinute:  600,
	}
	// Keep the log on stdout: wfRoutePaymentFailed is the only place that
	// surfaces the underlying ConfirmWalletPayment error.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	fmt.Printf("\n========== REPRO: WALLET WEB PAYMENT ==========\n")

	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)
	data := service.NewDataService(repository, payments, dataprovider.NewSimulator())

	messenger := &simMessenger{}
	convo := service.NewConversationService(cfg, repository, payments, data,
		map[string]ports.Messenger{service.ChannelWhatsApp: messenger},
		nil, identityprovider.NewSimulator(), screeningprovider.NewSimulator())

	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     func(s string) string { return s },
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int) int { return i + 1 },
		"collectionFeeKobo": func(p store.PaymentView) int64 {
			var meta struct {
				CollectionFeeKobo int64 `json:"collection_fee_kobo"`
			}
			if len(p.Metadata) > 0 {
				_ = json.Unmarshal(p.Metadata, &meta)
			}
			return meta.CollectionFeeKobo
		},
		"join": func(items []string, sep string) string { return strings.Join(items, sep) },
		"date": func(v any) string {
			if t, ok := v.(time.Time); ok {
				return t.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	a := &App{
		cfg: cfg, logger: logger, store: repository,
		payments: payments, data: data, conversation: convo,
		templates: templates, rateLimiter: ratelimit.NewMemory(),
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	run := newSimRun(t, srv)

	// Payer: confirmed individual at L1, exactly the account the wallet was
	// designed for — active wallet with a healthy balance.
	payer, err := repository.GetOrCreateUser(ctx, "+2348012349007")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, payer.ID, "Chidi Okonjo",
		time.Date(1990, 3, 11, 0, 0, 0, 0, time.UTC), "7 Admiralty Way, Lekki, Lagos", "Engineer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "simulated", Decision: kyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Status != store.WalletStatusActive {
		t.Fatalf("wallet status = %q, want active", wallet.Status)
	}
	if err := repository.CreditUserWallet(ctx, payer.ID, store.LedgerAccountOperatingBank, 1_000_000, "repro:fund", "fund wallet for repro"); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("  Customer %s — wallet %s (balance 1,000,000 kobo)\n", payer.WhatsAppNumber, wallet.Status)

	resetChatSession(t, ctx, repository, payer.ID)

	// 1. WhatsApp "make payment"
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "make payment"}); err != nil {
		t.Fatalf("handle 'make payment': %v", err)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected exactly one link message, got %+v", sent)
	}
	token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	if len(token) < 32 {
		t.Fatalf("unexpected web-flow token %q", token)
	}

	// 2. Browser wizard: merchant -> item -> amount -> review
	status, body, _ := run.get("/w/" + token)
	if status != http.StatusOK {
		t.Fatalf("web flow page: %d", status)
	}
	if status, _, loc := run.post("/w/"+token, url.Values{"merchant_slug": {"lagos-lunchbox"}}); status != http.StatusSeeOther || !strings.HasSuffix(loc, "/w/"+token) {
		t.Fatalf("merchant step: status=%d loc=%s", status, loc)
	}
	if status, _, _ = run.post("/w/"+token, url.Values{"item": {"custom"}}); status != http.StatusSeeOther {
		t.Fatalf("item step: %d", status)
	}
	if status, _, _ = run.post("/w/"+token, url.Values{"amount_kobo": {"2500"}}); status != http.StatusSeeOther {
		t.Fatalf("amount step: %d", status)
	}

	// 3. Review: pay from wallet, the failing path
	fmt.Printf("\n— Browser confirms: method=wallet, action=pay —\n")
	status, body, _ = run.post("/w/"+token, url.Values{"method": {service.ProviderWallet}, "action": {"pay"}})
	if status != http.StatusOK {
		t.Fatalf("wallet payment step: status=%d body=%s", status, run.page(body))
	}
	fmt.Printf("  Wallet step responds: %s\n", run.page(body))

	// 4. Assert the payment actually settled.
	views, err := repository.ListPayments(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.PaymentView
	for i := range views {
		if views[i].UserID == payer.ID && views[i].Status == domain.StatusSucceeded {
			found = &views[i]
			break
		}
	}
	if found == nil {
		// Surface every payment + status so the failing store step is obvious.
		t.Fatalf("no succeeded wallet payment for user; tried again with %q — see log line above", html.EscapeString(run.page(body)))
	}
	balance, err := repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 1_000_000-found.AmountKobo {
		t.Fatalf("wallet balance = %d, want %d (wallet was not debited by the payment)", balance, 1_000_000-found.AmountKobo)
	}
	fmt.Printf("  ✅ Wallet payment settled: %s for %s (wallet balance %s)\n",
		found.ID.String()[:8], domain.FormatNGN(found.AmountKobo), domain.FormatNGN(balance))
	fmt.Printf("  WhatsApp confirmation: %s\n", func() string {
		s := messenger.snapshot()
		for _, m := range s {
			if m.kind == "text" {
				return m.body
			}
		}
		return "(none)"
	}())
}