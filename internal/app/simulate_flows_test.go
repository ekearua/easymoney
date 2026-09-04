package app

// TestSimulateMakePaymentAndPayIndividual is a live, end-to-end simulation of
// the two flagship customer flows as they run today on WhatsApp (web flows
// enabled): message 1 (link) -> browser wizard -> hosted checkout -> gateway
// requery -> message 2 (confirmation) -> ledger postings.
//
// It runs against a real PostgreSQL (TEST_DATABASE_URL) and drives the actual
// HTTP routes, store, payment service (with a stub gateway standing in for the
// Interswitch sandbox), and a recording messenger standing in for the WhatsApp
// Cloud API. Run it with:
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestSimulateMakePaymentAndPayIndividual -v
//
// The transcript is printed to stdout as the flow advances.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

// --- recording messenger -------------------------------------------------

type simMessage struct {
	kind  string // link | text | checkout | interactive | template | image
	to    string
	body  string
	url   string
	label string
}

type simMessenger struct {
	mu   sync.Mutex
	sent []simMessage
}

func (m *simMessenger) record(kind, to, body, link, label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, simMessage{kind: kind, to: to, body: body, url: link, label: label})
}

func (m *simMessenger) snapshot() []simMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]simMessage, len(m.sent))
	copy(out, m.sent)
	return out
}

func (m *simMessenger) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = nil
}

func (m *simMessenger) SendText(_ context.Context, to, body string) error {
	m.record("text", to, body, "", "")
	return nil
}

func (m *simMessenger) SendInteractive(_ context.Context, msg ports.InteractiveMessage) error {
	m.record("interactive", msg.To, msg.Body, "", "")
	return nil
}

func (m *simMessenger) SendCheckout(_ context.Context, to, body, link string) error {
	m.record("checkout", to, body, link, "")
	return nil
}

func (m *simMessenger) SendLink(_ context.Context, to, body, link, label string) error {
	m.record("link", to, body, link, label)
	return nil
}

func (m *simMessenger) SendTemplate(_ context.Context, to, name string, _ []string) error {
	m.record("template", to, name, "", "")
	return nil
}

func (m *simMessenger) SendImage(_ context.Context, to string, _ []byte, caption string) error {
	m.record("image", to, caption, "", "")
	return nil
}

// --- stub gateway (stands in for the Interswitch sandbox) ----------------

type simGateway struct {
	store *store.Store
}

func (g *simGateway) Initialize(_ context.Context, in ports.InitializePayment) (ports.Checkout, error) {
	return ports.Checkout{Reference: in.Reference, URL: "https://checkout.sim.example/x/" + in.Reference}, nil
}

func (g *simGateway) Verify(ctx context.Context, reference string) (ports.Verification, error) {
	payment, err := g.store.PaymentByReference(ctx, reference)
	if err != nil {
		return ports.Verification{}, err
	}
	// Mirror the real Interswitch requery: the transaction is confirmed with
	// the exact reference, amount, currency, and payment metadata.
	return ports.Verification{
		Reference:  reference,
		Status:     "success",
		AmountKobo: payment.AmountKobo,
		Currency:   payment.Currency,
		Domain:     "test",
		Channel:    "card",
		Metadata: map[string]string{
			"payment_id":  payment.ID.String(),
			"merchant_id": payment.MerchantID.String(),
		},
	}, nil
}

func (g *simGateway) ValidateWebhook(_ []byte, _ string) (ports.GatewayWebhook, error) {
	return ports.GatewayWebhook{}, nil
}

// --- transcript + HTTP driver --------------------------------------------

type simRun struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
}

func newSimRun(t *testing.T, srv *httptest.Server) *simRun {
	t.Helper()
	return &simRun{
		t:   t,
		srv: srv,
		client: &http.Client{
			// Never follow redirects automatically: the transcript shows each
			// hop, and the checkout POST bounces to the external gateway.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       30 * time.Second,
		},
	}
}

func (r *simRun) note(format string, args ...any) {
	fmt.Printf("    "+format+"\n", args...)
}

func (r *simRun) get(path string) (int, string, string) {
	r.t.Helper()
	resp, err := r.client.Get(r.srv.URL + path)
	if err != nil {
		r.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	loc := resp.Header.Get("Location")
	r.note("GET %s  →  %d%s", path, resp.StatusCode, locSuffix(loc))
	return resp.StatusCode, string(body), loc
}

func (r *simRun) post(path string, form url.Values) (int, string, string) {
	r.t.Helper()
	resp, err := r.client.PostForm(r.srv.URL+path, form)
	if err != nil {
		r.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	loc := resp.Header.Get("Location")
	r.note("POST %s %s  →  %d%s", path, formKeys(form), resp.StatusCode, locSuffix(loc))
	return resp.StatusCode, string(body), loc
}

func (r *simRun) page(body string) string {
	// The visible page text (title + intro + first review lines), tags stripped
	// and HTML entities decoded (e.g. &#39; -> ').
	clean := htmlTagRe.ReplaceAllString(body, " ")
	clean = html.UnescapeString(clean)
	clean = strings.Join(strings.Fields(clean), " ")
	if len(clean) > 220 {
		clean = clean[:220] + "…"
	}
	return clean
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

func locSuffix(loc string) string {
	if loc == "" {
		return ""
	}
	return "  Location: " + loc
}

func formKeys(form url.Values) string {
	keys := make([]string, 0, len(form))
	for k, v := range form {
		keys = append(keys, k+"="+v[0])
	}
	return strings.Join(keys, " ")
}

// --- test ----------------------------------------------------------------

func TestSimulateMakePaymentAndPayIndividual(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the simulation against a real PostgreSQL")
	}
	ctx := context.Background()

	// The simulation owns a dedicated <database>_simulation database (mirroring
	// the service package's _service convention), so other suites truncating
	// the shared TEST_DATABASE_URL can never wipe its migrations and fixtures.
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

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

	// Payer: a confirmed, approved individual (KYC L2) — the account that can
	// pay merchants and send money to individuals.
	payer, err := repository.GetOrCreateUser(ctx, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	// Mirror the real individual-onboarding sequence exactly (ApplyIndividualProfile):
	// identity profile (which sets account_level='individual'), clean screening
	// result, then the ladder promotion to L2 with channel + identity evidence.
	if _, err := repository.UpsertIndividualProfile(ctx, payer.ID, "Ada Obi",
		time.Date(1992, 5, 24, 0, 0, 0, 0, time.UTC), "14 Admiralty Way, Lekki, Lagos", "Product designer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "simulated", Decision: kyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		t.Fatal(err)
	}

	// =====================================================================
	fmt.Printf("\n========== SIMULATION 1: MAKE PAYMENT ==========\n")
	simulateMakePayment(t, ctx, run, convo, repository, messenger, cfg, payer)
	// A unique recipient per run keeps the simulation self-contained (the
	// recipient's wallet is additive by design, so reruns must not reuse it).
	recipientPhone := fmt.Sprintf("0805555%05d", time.Now().UnixNano()%100000)
	// =====================================================================
	fmt.Printf("\n========== SIMULATION 2: PAY AN INDIVIDUAL ==========\n")
	simulatePayIndividual(t, ctx, run, convo, repository, messenger, cfg, payer, recipientPhone)

	// =====================================================================
	fmt.Printf("\n========== MESSAGING COST METER ==========\n")
	summary, err := repository.MessageStats(ctx, 1, 14, 84)
	if err != nil {
		t.Fatalf("message stats: %v", err)
	}
	fmt.Printf("  Since: %s\n  Total messages: %d (service %d)\n", summary.Since.Format("15:04"), summary.TotalMessages, summary.ServiceMessages)
	fmt.Printf("  Est. cost: NGN %d (~$%.2f) · per txn avg %.1f messages\n", summary.EstimatedCostNGN, float64(summary.EstimatedCostUSDC)/100, summary.AveragePerTxn)
	for _, f := range summary.PerFlow {
		fmt.Printf("    %-16s %-10s msgs=%-3d txns=%d avg=%.1f est=NGN %d\n", f.Flow, f.Channel, f.Messages, f.Transactions, f.AveragePerTxn, f.EstimatedCostNGN)
	}
}

func simulateMakePayment(t *testing.T, ctx context.Context, run *simRun, convo *service.ConversationService, repository *store.Store, messenger *simMessenger, cfg config.Config, payer store.User) {
	t.Helper()
	messenger.reset()
	resetChatSession(t, ctx, repository, payer.ID)

	fmt.Printf("\n— Customer on WhatsApp taps “Make payment” (menu row menu_pay) —\n")
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "make payment"}); err != nil {
		t.Fatalf("handle 'make payment': %v", err)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected exactly one link message (message 1), got %+v", sent)
	}
	fmt.Printf("  📲 WhatsApp → customer:\n     %q\n     [%s] %s\n", sent[0].body, sent[0].label, sent[0].url)
	token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	if len(token) < 32 {
		t.Fatalf("unexpected web-flow token %q", token)
	}

	fmt.Printf("\n— Browser: /w/%s… —\n", token[:8])
	status, body, _ := run.get("/w/" + token)
	if status != http.StatusOK {
		t.Fatalf("web flow page: %d", status)
	}
	fmt.Printf("  Step: %s\n", run.page(body))

	// 1. merchant
	status, body, loc := run.post("/w/"+token, url.Values{"merchant_slug": {"lagos-lunchbox"}})
	if status != http.StatusSeeOther || !strings.HasSuffix(loc, "/w/"+token) {
		t.Fatalf("merchant step: status=%d loc=%s", status, loc)
	}
	status, body, _ = run.get(loc)
	fmt.Printf("  Step: %s\n", run.page(body))

	// 2. item -> custom amount
	status, body, loc = run.post("/w/"+token, url.Values{"item": {"custom"}})
	if status != http.StatusSeeOther {
		t.Fatalf("item step: %d", status)
	}
	status, body, _ = run.get(loc)
	fmt.Printf("  Step: %s\n", run.page(body))

	// 3. amount (NGN 2,500 = 250,000 kobo)
	status, body, loc = run.post("/w/"+token, url.Values{"amount_kobo": {"2500"}})
	if status != http.StatusSeeOther {
		t.Fatalf("amount step: %d", status)
	}
	status, body, _ = run.get(loc)
	fmt.Printf("  Step: %s\n", run.page(body))

	// 4. review + card checkout
	fee := service.XegoCollectionFee(cfg, "card", 250_000).FeeKobo
	status, body, loc = run.post("/w/"+token, url.Values{"method": {service.ProviderInterswitch}, "action": {"pay"}})
	if status != http.StatusSeeOther {
		t.Fatalf("review step: %d body=%s", status, run.page(body))
	}
	fmt.Printf("  ✅ Payment drafted: base %s + card fee %s = charge %s\n",
		domain.FormatNGN(250_000), domain.FormatNGN(fee), domain.FormatNGN(250_000+fee))

	// 5. branded checkout page, then confirm -> gateway
	status, body, _ = run.get(loc)
	if status != http.StatusOK {
		t.Fatalf("checkout page: %d", status)
	}
	fmt.Printf("  Branded checkout page: %s\n", run.page(body))
	checkoutToken := strings.TrimPrefix(loc, "/checkout/")
	payment, err := repository.PaymentByCheckoutToken(ctx, checkoutToken)
	if err != nil {
		t.Fatalf("load payment by checkout token: %v", err)
	}
	status, _, gatewayURL := run.post("/checkout/"+checkoutToken+"/pay", nil)
	if status != http.StatusSeeOther || !strings.HasPrefix(gatewayURL, "https://") {
		t.Fatalf("checkout pay: status=%d loc=%q", status, gatewayURL)
	}
	fmt.Printf("  🔀 Browser leaves for the Interswitch hosted page: %s\n", gatewayURL)

	// 6. customer completes at the gateway; Xego requeries on the return hop
	fmt.Printf("\n— Interswitch calls back: GET /payments/return?reference=%s… —\n", payment.ProviderReference[:12])
	status, _, loc = run.get("/payments/return?reference=" + url.QueryEscape(payment.ProviderReference))
	if status != http.StatusSeeOther {
		t.Fatalf("payment return: %d", status)
	}
	fmt.Printf("  → %s\n", loc)

	verifyPaymentSucceeded(t, ctx, repository, payment.ID)
	verifyLedgerForPayment(t, ctx, repository, payment.ID, "Make payment")
	verifyWhatsAppCompletion(t, messenger, "Make payment")
}

func simulatePayIndividual(t *testing.T, ctx context.Context, run *simRun, convo *service.ConversationService, repository *store.Store, messenger *simMessenger, cfg config.Config, payer store.User, recipientPhone string) {
	t.Helper()
	messenger.reset()
	resetChatSession(t, ctx, repository, payer.ID)

	fmt.Printf("\n— Customer on WhatsApp taps “Pay an individual” (menu row menu_pay_individual) —\n")
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "pay individual"}); err != nil {
		t.Fatalf("handle 'pay individual': %v", err)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected exactly one link message (message 1), got %+v", sent)
	}
	fmt.Printf("  📲 WhatsApp → customer:\n     %q\n     [%s] %s\n", sent[0].body, sent[0].label, sent[0].url)
	token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	if len(token) < 32 {
		t.Fatalf("unexpected web-flow token %q", token)
	}

	fmt.Printf("\n— Browser: /w/%s… —\n", token[:8])
	steps := []struct {
		name  string
		form  url.Values
		check string
	}{
		{"recipient phone", url.Values{"recipient_phone": {recipientPhone}}, "Amount to send"},
		{"amount (NGN 5,000)", url.Values{"amount_kobo": {"5000"}}, "Recipient's bank"},
		{"bank code (GTBank 058)", url.Values{"bank_code": {"058"}}, "Recipient's account"},
		{"account number", url.Values{"account_number": {"0123456789"}}, "Review your transfer"},
	}
	for _, step := range steps {
		status, body, loc := run.post("/w/"+token, step.form)
		if status != http.StatusSeeOther {
			t.Fatalf("%s step: %d body=%s", step.name, status, run.page(body))
		}
		status, body, _ = run.get(loc)
		if status != http.StatusOK || !strings.Contains(run.page(body), step.check) {
			t.Fatalf("%s step: status=%d page=%s", step.name, status, run.page(body))
		}
		fmt.Printf("  ✅ %s → %s\n", step.name, run.page(body))
	}

	amount := int64(500_000)
	collectionFee := service.XegoCollectionFee(cfg, "transfer", amount).FeeKobo
	nipFee := service.XegoPayoutFee(cfg, amount)
	status, body, loc := run.post("/w/"+token, url.Values{"method": {service.ProviderBankTransfer}, "action": {"pay"}})
	if status != http.StatusSeeOther {
		t.Fatalf("review step: %d body=%s", status, run.page(body))
	}
	fmt.Printf("  ✅ Draft: send %s − NIP %s → recipient gets %s, you pay %s (incl. %s collection fee)\n",
		domain.FormatNGN(amount), domain.FormatNGN(nipFee), domain.FormatNGN(amount-nipFee),
		domain.FormatNGN(amount+collectionFee), domain.FormatNGN(collectionFee))

	status, _, _ = run.get(loc)
	if status != http.StatusOK {
		t.Fatalf("checkout page: %d", status)
	}
	fmt.Printf("  Branded checkout page rendered.\n")
	checkoutToken := strings.TrimPrefix(loc, "/checkout/")
	payment, err := repository.PaymentByCheckoutToken(ctx, checkoutToken)
	if err != nil {
		t.Fatalf("load payment by checkout token: %v", err)
	}
	status, _, gatewayURL := run.post("/checkout/"+checkoutToken+"/pay", nil)
	if status != http.StatusSeeOther || !strings.HasPrefix(gatewayURL, "https://") {
		t.Fatalf("checkout pay: status=%d loc=%q", status, gatewayURL)
	}
	fmt.Printf("  🔀 Browser leaves for the Interswitch hosted page: %s\n", gatewayURL)

	fmt.Printf("\n— Interswitch calls back: GET /payments/return?reference=%s… —\n", payment.ProviderReference[:12])
	status, _, loc = run.get("/payments/return?reference=" + url.QueryEscape(payment.ProviderReference))
	if status != http.StatusSeeOther {
		t.Fatalf("payment return: %d", status)
	}
	fmt.Printf("  → %s\n", loc)

	verifyPaymentSucceeded(t, ctx, repository, payment.ID)
	verifyLedgerForPayment(t, ctx, repository, payment.ID, "Pay an individual")

	// The recipient's share lands in their Xego wallet (money stays inside the
	// platform until the recipient cashes out).
	recipient, err := repository.GetOrCreateUser(ctx, domain.CanonicalE164Phone(recipientPhone))
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, recipient.ID)
	if err != nil {
		t.Fatalf("recipient wallet: %v", err)
	}
	balance, err := repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != amount-nipFee {
		t.Fatalf("recipient wallet balance = %d, want %d", balance, amount-nipFee)
	}
	fmt.Printf("  👛 Recipient %s wallet balance: %s (expected %s)\n", recipient.WhatsAppNumber,
		domain.FormatNGN(balance), domain.FormatNGN(amount-nipFee))

	verifyWhatsAppCompletion(t, messenger, "Pay an individual")
}

// simTestDBURL returns a database URL dedicated to the app package's
// simulation so it never shares tables with the store/service suites when the
// whole test run happens in parallel. The database is created on first use.
func simTestDBURL(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	mainDB := cfg.ConnConfig.Database
	if mainDB == "" {
		mainDB = "postgres"
	}
	simDB := mainDB + "_simulation"

	// Build DSNs from the parsed config so both the URL form and the keyword
	// form work. The URL form keeps every query parameter (sslmode, etc.) and
	// only swaps the database path; the keyword form strips the database key.
	adminDSN, simDSN := "", ""
	if parsed, err := url.Parse(strings.TrimSpace(base)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		admin := *parsed
		admin.Path = "/postgres"
		adminDSN = admin.String()
		sim := *parsed
		sim.Path = "/" + simDB
		simDSN = sim.String()
	} else {
		adminDSN = regexp.MustCompile(`(^|\s)database=\S+`).ReplaceAllString(strings.TrimSpace(base), "")
		simDSN = adminDSN + " database=" + simDB
	}

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, simDB).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE "`+simDB+`"`); err != nil {
			t.Fatal(err)
		}
	}
	return simDSN
}

// resetChatSession parks the customer back at the main menu and closes any
// stale open web flows, so a rerun (or an earlier failed run) cannot resume a
// half-finished browser flow or swallow the next "make payment" message.
func resetChatSession(t *testing.T, ctx context.Context, repository *store.Store, userID uuid.UUID) {
	t.Helper()
	if err := repository.SaveSession(ctx, store.Session{
		UserID: userID, State: "menu", Data: map[string]string{},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for _, flowType := range []string{
		service.WebFlowPay, service.WebFlowIndividualPay, service.WebFlowTopup, service.WebFlowPayInvoice,
		service.WebFlowInvoiceCreate, service.WebFlowThriftCreate, service.WebFlowThriftJoin, service.WebFlowThriftContribute,
		service.WebFlowData, service.WebFlowOnboard, service.WebFlowIndividualUpgrade,
		service.WebFlowMerchantRegister, service.WebFlowKYBRequest,
	} {
		if flow, found, err := repository.OpenWebFlowForUser(ctx, userID, flowType); err == nil && found {
			_, _, _ = repository.CompleteWebFlow(ctx, flow.Token)
		}
	}
}

// verifyPaymentSucceeded checks the terminal transition and prints the status.
func verifyPaymentSucceeded(t *testing.T, ctx context.Context, repository *store.Store, paymentID uuid.UUID) {
	t.Helper()
	view, err := repository.PaymentByID(ctx, paymentID)
	if err != nil {
		t.Fatalf("load payment: %v", err)
	}
	if view.Status != domain.StatusSucceeded {
		t.Fatalf("payment %s status = %q, want %q", paymentID.String()[:8], view.Status, domain.StatusSucceeded)
	}
	fmt.Printf("  ✅ Payment %s… status=%s (reference %s…)\n", paymentID.String()[:8], view.Status, view.ProviderReference[:12])
}

// verifyLedgerForPayment prints the double-entry postings for the payment's
// journal reference and asserts the book still balances.
func verifyLedgerForPayment(t *testing.T, ctx context.Context, repository *store.Store, paymentID uuid.UUID, label string) {
	t.Helper()
	entries, err := repository.ListLedgerEntries(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("  Ledger postings (%s):\n", label)
	found := 0
	for _, e := range entries {
		// Money-in posts under the payment id; the individual-pay settlement
		// (fees + recipient wallet credit) posts under "individual-pay:<id>".
		if e.JournalRef != paymentID.String() &&
			!strings.HasPrefix(e.JournalRef, "individual-pay:"+paymentID.String()) {
			continue
		}
		found++
		fmt.Printf("    %-6s %-28s %10s  %s\n", e.EntryType, e.Account, domain.FormatNGN(e.AmountKobo), e.Description)
	}
	if found == 0 {
		t.Fatalf("no ledger entries for payment %s", paymentID.String())
	}
	// The double-entry invariant is that the book nets to zero across all
	// accounts (Σ debits = Σ credits); individual asset/liability accounts
	// legitimately hold balances, so only the grand total is asserted.
	balances, err := repository.LedgerBalanceSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range balances {
		total += b.NetKobo
	}
	if total != 0 {
		t.Fatalf("ledger out of balance: total net = %d kobo", total)
	}
	fmt.Printf("  ✅ Ledger nets to zero across all accounts (Σ debit = Σ credit).\n")
}

// verifyWhatsAppCompletion asserts the two-message pattern and prints it.
func verifyWhatsAppCompletion(t *testing.T, messenger *simMessenger, label string) {
	t.Helper()
	sent := messenger.snapshot()
	if len(sent) != 2 {
		t.Fatalf("%s: expected exactly 2 WhatsApp messages (link + confirmation), got %d: %+v", label, len(sent), sent)
	}
	if sent[0].kind != "link" || sent[1].kind != "text" {
		t.Fatalf("%s: unexpected message kinds %q → %q", label, sent[0].kind, sent[1].kind)
	}
	fmt.Printf("  📲 WhatsApp → customer (message 2):\n     %q\n", strings.ReplaceAll(sent[1].body, "\n", " ⏎ "))
	fmt.Printf("  ✅ %s: exactly 2 service messages (link + confirmation).\n", label)
}
