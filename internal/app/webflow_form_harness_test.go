package app

// Stepped-form harness boots the real app (real templates, real stylesheet) and
// mints open web flows parked on the KYC and thrift steps a browser cannot reach
// without an emailed code or a saved group, then parks while a Playwright script
// screenshots each page and asserts the stepped shell renders.
//
//	TEST_DATABASE_URL=postgres://... XEGO_FORMS=1 \
//	go test ./internal/app/ -run TestWebFlowFormHarness -v
//
// The URLs are written to FORMS_META_FILE as "<name> <url>" lines.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	whatsappkyc "whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	dataprovider "whatsapp-payment-demo/internal/providers/data"
	identityprovider "whatsapp-payment-demo/internal/providers/identity"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
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

	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()

	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  srv.URL,
		WhatsAppPhoneNumber:      "2348000000000",
		WebFlowsEnabled:          true,
		SessionTTL:               30 * time.Minute,
		PaymentMinKobo:           10_000,
		PaymentMaxKobo:           10_000_000,
		RateLimitPublicPerMinute: 6000,
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
	srv.Config.Handler = a.routes()
	srv.Start()

	// A confirmed, screened L2 individual: able to create thrift groups and
	// start an individual upgrade.
	payer, err := repository.GetOrCreateUser(ctx, "+2348012340222")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "harness", Decision: whatsappkyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, whatsappkyc.TierL1,
		[]string{whatsappkyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}

	startFlow := func(trigger string) string {
		t.Helper()
		messenger.reset()
		_ = repository.SaveSession(ctx, store.Session{UserID: payer.ID, State: "menu", Data: map[string]string{}, ExpiresAt: time.Now().Add(time.Hour)})
		if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: trigger}); err != nil {
			t.Fatalf("start %q: %v", trigger, err)
		}
		sent := messenger.snapshot()
		if len(sent) == 0 {
			t.Fatalf("start %q: no link message (%+v)", trigger, sent)
		}
		token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
		if len(token) < 32 {
			t.Fatalf("start %q: unexpected token %q", trigger, token)
		}
		return token
	}

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

	// kyb_request needs an approved merchant owned by the payer; seed the
	// merchant_owners join directly and park the flow on its note step.
	if _, err := repository.RawExec(ctx, fmt.Sprintf(`INSERT INTO merchant_owners (merchant_id, user_id)
		SELECT m.id, '%s' FROM merchants m WHERE m.slug='lagos-lunchbox'
		ON CONFLICT DO NOTHING`, payer.ID)); err != nil {
		t.Fatal(err)
	}
	kybToken := startFlow("request kyb upgrade")
	pages["kyb_note"] = cfg.BaseURL + "/w/" + kybToken

	fmt.Printf("FORMS base=%s\n", srv.URL)
	for _, name := range []string{"thrift_frequency", "upgrade_profile", "onboard_code", "kyb_note"} {
		fmt.Printf("FORMS %s=%s\n", name, pages[name])
	}
	if out := os.Getenv("FORMS_META_FILE"); out != "" {
		raw, _ := json.Marshal(pages)
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			t.Logf("write meta file: %v", err)
		}
	}

	park := 5 * time.Minute
	if v := os.Getenv("PARK_MINUTES"); v != "" {
		if mins, ok := atoi(v); ok && mins > 0 && mins < 9 {
			park = time.Duration(mins) * time.Minute
		}
	}
	time.Sleep(park)
}
