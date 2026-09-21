package app

// Stepped-form harness boots the real app (real templates, real stylesheet) and
// mints open web flows parked on the KYC and thrift steps a browser cannot reach
// without an emailed code or a saved group, then parks while a Playwright script
// screenshots each page and asserts the stepped shell renders.
//
//	TEST_DATABASE_URL=postgres://... XEGO_FORMS=1 \
//	go test ./internal/app/ -run TestWebFlowFormHarness -v
//
// Every URL it prints is absolute: the server is started before the app is
// built so cfg.BaseURL is the real httptest URL, which the harness asserts on
// both the flow links it hands out and the receipt link a wallet payment sends.
//
// The URLs are written to FORMS_META_FILE as "<name> <url>" lines.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	whatsappkyc "whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestWebFlowFormHarness(t *testing.T) {
	if os.Getenv("XEGO_FORMS") != "1" {
		t.Skip("stepped-form harness only")
	}
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL required")
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

	// The listener has to exist before the app is built: cfg.BaseURL is captured
	// by the services and the App at construction time, and httptest only
	// assigns srv.URL inside Start(). Starting first, behind a handler that
	// proxies to an app attached a moment later, gives the harness a real
	// BaseURL — so every link it prints and every chat message it captures is an
	// absolute URL instead of a bare path.
	var appHandler http.Handler
	var appMu sync.RWMutex
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The simulated hosted gateway page is served by the harness itself,
		// in front of the app: production has no such route, and the real
		// Interswitch page is off-host. Everything else goes to the app.
		if strings.HasPrefix(r.URL.Path, simGatewayPath) {
			simGatewayPage(w, r, repository)
			return
		}
		appMu.RLock()
		h := appHandler
		appMu.RUnlock()
		if h == nil {
			http.Error(w, "harness app not ready", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	srv.Start()

	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  srv.URL,
		WhatsAppPhoneNumber:      "2348000000000",
		WebFlowsEnabled:          true,
		SessionTTL:               30 * time.Minute,
		PaymentMinKobo:           10_000,
		PaymentMaxKobo:           10_000_000,
		RateLimitPublicPerMinute: 6000,
		// Production-shaped fees: the one-page rails carry data-fee-* attributes
		// (the live charge-label enhancement reads them) and the card button's
		// label quotes amount + fee exactly as production renders it.
		FeeCardBPS:           200,
		FeeCardFixedKobo:     10_000,
		FeeCardCapKobo:       200_000,
		FeeTransferBPS:       150,
		FeeTransferFixedKobo: 10_000,
		FeeTransferCapKobo:   200_000,
		FeeNIPPayoutFlatKobo: 5_000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Both rails park on the harness's own host page (see simGateway.baseURL).
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository, baseURL: srv.URL},
		service.ProviderBankTransfer: &simGateway{store: repository, baseURL: srv.URL},
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)
	data := service.NewDataService(repository, payments, stubDataProvider{})
	messenger := &simMessenger{}
	convo := service.NewConversationService(cfg, repository, payments, data,
		map[string]ports.Messenger{service.ChannelWhatsApp: messenger},
		nil, stubIdentityVerifier{}, stubSanctionsScreener{})

	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     func(s string) string { return s },
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int64) int64 { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
		"date": func(v any) string {
			if t, ok := v.(time.Time); ok {
				return t.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	a := &App{
		cfg: cfg, logger: logger, store: repository,
		payments: payments, data: data, conversation: convo,
		templates: templates, rateLimiter: ratelimit.NewMemory(),
	}
	appMu.Lock()
	appHandler = a.routes()
	appMu.Unlock()

	// A confirmed, screened L1 individual: able to create thrift groups, start
	// an individual upgrade, and pay from an active (empty) wallet. Each
	// concurrent pay flow needs its own customer — the conversation hands an
	// existing open flow back to the same customer instead of minting a second
	// one.
	newIndividual := func(number string) store.User {
		t.Helper()
		user, err := repository.GetOrCreateUser(ctx, number)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: user.ID, Provider: "harness", Decision: whatsappkyc.ScreenClear,
		}, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		// Reaching L1 also activates the pending individual wallet opened at
		// user creation, so the wallet rails are exercisable here.
		if _, err := repository.AdvanceKYCTierTo(ctx, user.ID, whatsappkyc.TierL1,
			[]string{whatsappkyc.EvChannelConfirmed}, nil); err != nil {
			t.Fatal(err)
		}
		return user
	}
	payer := newIndividual("+2348012340222")

	startFlowAs := func(user store.User, trigger string) string {
		t.Helper()
		messenger.reset()
		_ = repository.SaveSession(ctx, store.Session{UserID: user.ID, State: "menu", Data: map[string]string{}, ExpiresAt: time.Now().Add(time.Hour)})
		if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: user.WhatsAppNumber, Text: trigger}); err != nil {
			t.Fatalf("start %q: %v", trigger, err)
		}
		sent := messenger.snapshot()
		if len(sent) == 0 {
			t.Fatalf("start %q: no link message (%+v)", trigger, sent)
		}
		// Every link handed out here must be absolute: a bare path means the
		// app was built without a real BaseURL and the deep links it sends the
		// customer are broken.
		for _, msg := range sent {
			if msg.url != "" && !strings.HasPrefix(msg.url, cfg.BaseURL+"/") {
				t.Fatalf("start %q: message link %q is not absolute (base %q)", trigger, msg.url, cfg.BaseURL)
			}
		}
		token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
		if token == sent[0].url || len(token) < 32 {
			t.Fatalf("start %q: unexpected flow link %q", trigger, sent[0].url)
		}
		return token
	}
	startFlow := func(trigger string) string { return startFlowAs(payer, trigger) }

	pages := map[string]string{}

	// thrift_create, parked on the frequency step: radio cards in the shell.
	thriftToken := startFlow("create thrift")
	if err := repository.SaveWebFlowProgress(ctx, thriftToken, "frequency", map[string]string{
		"thrift_name": "Office Pool", "thrift_amount_kobo": "200000",
	}); err != nil {
		t.Fatal(err)
	}
	pages["thrift_frequency"] = cfg.BaseURL + "/w/" + thriftToken

	// individual_upgrade, parked on the profile step: multi-field form plus an
	// upload field, with the sticky CTA.
	upgradeToken := startFlow("become individual")
	if err := repository.SaveWebFlowProgress(ctx, upgradeToken, "profile", map[string]string{
		"email": "harness@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	pages["upgrade_profile"] = cfg.BaseURL + "/w/" + upgradeToken

	// onboard, parked on the code step: a primary action next to a retry.
	onboardToken := startFlow("complete profile")
	if err := repository.SaveWebFlowProgress(ctx, onboardToken, "code", map[string]string{
		"name": "Amina Bello", "email": "amina@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	pages["onboard_code"] = cfg.BaseURL + "/w/" + onboardToken

	// invoice_create, parked on the items summary with two items already on
	// the list: the computed total and the add-another secondary route.
	invoiceToken := startFlow("generate invoice")
	if err := repository.SaveWebFlowProgress(ctx, invoiceToken, "items_summary", map[string]string{
		"merchant_slug":  "lagos-lunchbox",
		"customer_phone": "+2348033334444",
		"customer_email": "buyer@example.com",
		"invoice_items":  `[{"Description":"Jollof tray","Quantity":2,"UnitPriceKobo":250000}]`,
	}); err != nil {
		t.Fatal(err)
	}
	pages["invoice_items_summary"] = cfg.BaseURL + "/w/" + invoiceToken

	// kyb_request needs an approved merchant owned by the payer; seed the
	// merchant_owners join directly and park the flow on its note step.
	if _, err := repository.RawExec(ctx, fmt.Sprintf(`INSERT INTO merchant_owners (merchant_id, user_id)
		SELECT m.id, '%s' FROM merchants m WHERE m.slug='lagos-lunchbox'
		ON CONFLICT DO NOTHING`, payer.ID)); err != nil {
		t.Fatal(err)
	}
	kybToken := startFlow("request kyb upgrade")
	pages["kyb_note"] = cfg.BaseURL + "/w/" + kybToken

	// pay, parked on the one-page start: AI bar, merchant select, amount
	// field, and payment rails together. A catalog service is seeded onto the
	// walk's merchant so the browser also exercises the item-handoff branch:
	// a rail tap for a catalog merchant routes to the item page, not a charge.
	if _, err := repository.RawExec(ctx, `INSERT INTO merchant_services (merchant_id, name, description, unit_price_kobo, is_active)
		SELECT id, 'Jollof Combo', 'journey fixture', 250000, true FROM merchants WHERE slug='lagos-lunchbox'
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	payToken := startFlow("pay")
	pages["pay_merchant"] = cfg.BaseURL + "/w/" + payToken

	// pay (wallet, short of funds), parked on the final review step with no
	// rail chosen yet: a browser taps "Pay from wallet", the draft is created
	// but the confirm fails on an active wallet holding nothing, and the review
	// re-renders with the reason. A reload then has to explain the wallet state
	// inline from the recorded rail.
	walletUser := newIndividual("+2348012340333")
	walletToken := startFlowAs(walletUser, "pay")
	if err := repository.SaveWebFlowProgress(ctx, walletToken, "review", map[string]string{
		"merchant_slug": "lagos-lunchbox",
		"item_name":     "Custom amount",
		"amount_kobo":   "250000",
	}); err != nil {
		t.Fatal(err)
	}
	pages["pay_review_wallet_short"] = cfg.BaseURL + "/w/" + walletToken

	// pay (ambiguous): a second merchant whose name collides with Kora Books
	// under prefix matching, so a typed "pay kora book …" cannot be resolved
	// without the customer choosing. The driver asserts the confirmation
	// banner and that the select stays unselected.
	if _, err := repository.RawExec(ctx, `INSERT INTO merchants (slug, name, category, description)
		VALUES ('kora-bookshop', 'Kora Bookshop', 'Books', 'Second branch for ambiguity testing.')
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, active = true`); err != nil {
		t.Fatal(err)
	}

	// A funded wallet pays to completion through the same review page, which
	// proves the receipt link the app sends back is absolute too — the other
	// half of the BaseURL wiring.
	receiptUser := newIndividual("+2348012340444")
	if err := repository.CreditUserWallet(ctx, receiptUser.ID, store.LedgerAccountOperatingBank, 500_000,
		"harness:wallet-float", "harness wallet float"); err != nil {
		t.Fatal(err)
	}
	receiptToken := startFlowAs(receiptUser, "pay")
	if err := repository.SaveWebFlowProgress(ctx, receiptToken, "review", map[string]string{
		"merchant_slug": "lagos-lunchbox",
		"item_name":     "Custom amount",
		"amount_kobo":   "100000",
	}); err != nil {
		t.Fatal(err)
	}
	paid, err := http.PostForm(srv.URL+"/w/"+receiptToken, url.Values{"action": {service.ProviderWallet}})
	if err != nil {
		t.Fatal(err)
	}
	_ = paid.Body.Close()
	if paid.StatusCode != http.StatusOK {
		t.Fatalf("wallet payment through the review page = %s, want 200", paid.Status)
	}
	receiptLink := cfg.BaseURL + "/receipts/"
	var receiptSeen bool
	for _, msg := range messenger.snapshot() {
		if strings.Contains(msg.body, receiptLink) || strings.HasPrefix(msg.url, receiptLink) {
			receiptSeen = true
		}
	}
	if !receiptSeen {
		t.Fatalf("wallet payment sent no absolute receipt link (%s…); captured %+v", receiptLink, messenger.snapshot())
	}

	// The full wallet loop, in the order a browser must walk it: a fresh
	// customer tops the wallet up through this browser flow (card checkout on
	// the simulated hosted page, which returns through the app's real
	// /payments/return requery), and then pays a merchant from the funded
	// wallet through a second flow. The driver walks the two links in order,
	// so the credit has to be committed before the wallet payment starts.
	loopUser := newIndividual("+2348012340555")
	topupToken := startFlowAs(loopUser, "fund wallet")
	if err := repository.SaveWebFlowProgress(ctx, topupToken, "amount", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	pages["wallet_topup"] = cfg.BaseURL + "/w/" + topupToken

	walletPayToken := startFlowAs(loopUser, "pay")
	if err := repository.SaveWebFlowProgress(ctx, walletPayToken, "review", map[string]string{
		"merchant_slug": "lagos-lunchbox",
		"item_name":     "Custom amount",
		"amount_kobo":   "250000",
	}); err != nil {
		t.Fatal(err)
	}
	pages["wallet_pay"] = cfg.BaseURL + "/w/" + walletPayToken

	// Everything printed below is fed straight to a browser driver, so it has
	// to be a real URL a driver can open without guessing the host.
	for name, page := range pages {
		if !strings.HasPrefix(page, cfg.BaseURL+"/w/") {
			t.Fatalf("printed %s = %q is not an absolute /w/ URL (base %q)", name, page, cfg.BaseURL)
		}
	}

	fmt.Printf("FORMS base=%s\n", srv.URL)
	for _, name := range []string{"thrift_frequency", "upgrade_profile", "onboard_code", "kyb_note", "invoice_items_summary", "pay_merchant", "pay_review_wallet_short", "wallet_topup", "wallet_pay"} {
		fmt.Printf("FORMS %s=%s\n", name, pages[name])
	}
	if out := os.Getenv("FORMS_META_FILE"); out != "" {
		raw, _ := json.Marshal(pages)
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			t.Logf("write meta file: %v", err)
		}
	}

	// The park window is the driver's whole budget: a long browser walk (the
	// wallet loop runs two flows back to back) must never race the server
	// shutting down mid-drive, so PARK_MINUTES is honored up to 30.
	park := 5 * time.Minute
	if v := os.Getenv("PARK_MINUTES"); v != "" {
		if mins, ok := atoi(v); ok && mins > 0 && mins <= 30 {
			park = time.Duration(mins) * time.Minute
		}
	}
	time.Sleep(park)
}

// simGatewayPath is where the harness's simulated hosted checkout page lives.
const simGatewayPath = "/sim-gateway/"

// simGatewayPage stands in for the hosted Interswitch checkout page: it shows
// the amount and reference the customer would see and its single button does
// what completing the real page does — hand the transaction reference back to
// the app's own return endpoint, which requeries the gateway and settles the
// payment. Only the gateway is simulated; the requery, the flow completion,
// the WhatsApp confirmation, and the receipt are all the app's real code.
func simGatewayPage(w http.ResponseWriter, r *http.Request, repository *store.Store) {
	reference := strings.TrimPrefix(r.URL.Path, simGatewayPath)
	amount := "—"
	if payment, err := repository.PaymentByReference(r.Context(), reference); err == nil {
		amount = domain.FormatNGN(payment.AmountKobo)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Secure checkout (simulated)</title><link rel="stylesheet" href="/static/styles.css"></head>
<body class="receipt-body"><main class="receipt">
  <p class="eyebrow">Simulated Interswitch checkout</p>
  <h1>Complete your payment</h1>
  <p class="receipt-amount">%s</p>
  <dl><div><dt>Reference</dt><dd class="mono">%s</dd></div></dl>
  <form method="post" action="/payments/return">
    <input type="hidden" name="txnref" value="%s">
    <button class="button" type="submit" id="sim-complete">Complete payment</button>
  </form>
</main></body></html>`, amount, reference, reference)
}
