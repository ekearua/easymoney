package app

// TestCheckoutRenderToggle pins the INTERSWITCH_CHECKOUT_RENDER switch: the
// /checkout/interswitch/{ref} page must serve the Hosted Fields SDK template in
// hosted_fields mode and the auto-submitting Web Checkout redirect form (which
// posts to the newwebpay-sandbox gateway host) in legacy mode. The decision is
// read from the config on every request, so the same payment is served under
// both modes.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestCheckoutRenderToggle -v

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"whatsapp-payment-demo/internal/providers/interswitch"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestCheckoutRenderToggle(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run against a real PostgreSQL")
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
		AppName:                   "Xego",
		BaseURL:                   "https://demo.xego.ng",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            10_000,
		PaymentMaxKobo:            10_000_000,
		FeeCardBPS:                200,
		FeeCardFixedKobo:          10_000,
		FeeCardCapKobo:            350_000,
		RateLimitPublicPerMinute:  6000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)
	data := service.NewDataService(repository, payments, dataprovider.NewSimulator())
	convo := service.NewConversationService(cfg, repository, payments, data,
		map[string]ports.Messenger{service.ChannelWhatsApp: &simMessenger{}},
		nil, identityprovider.NewSimulator(), screeningprovider.NewSimulator())

	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     func(s string) string { return s },
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int64) int64 { return i + 1 },
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
		interswitch: interswitch.New(interswitch.Options{
			MerchantCode: "M1000", PayItemID: "pi",
			BaseURL: "https://sandbox.interswitchng.com", Mode: "TEST",
		}),
		templates: templates, rateLimiter: ratelimit.NewMemory(),
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	payer, err := repository.GetOrCreateUser(ctx, "+2348012340111")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
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
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := payments.CreateCollectionDraft(ctx, payer, merchant, 49_255,
		service.ProviderInterswitch, service.ChannelWhatsApp, payer.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	payment, err = payments.InitializeCheckout(ctx, payment)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := "/checkout/interswitch/" + payment.ProviderReference

	body := renderCheckout(t, client, srv.URL+path)
	if !strings.Contains(body, `id="cardNumber-container"`) || !strings.Contains(body, "/static/hosted_fields.js") {
		t.Fatalf("hosted_fields mode did not render the Hosted Fields SDK page\n%s", body[:min(len(body), 600)])
	}
	if strings.Contains(body, `id="interswitch-form"`) {
		t.Fatalf("hosted_fields mode unexpectedly rendered the redirect form")
	}

	a.cfg.InterswitchCheckoutRender = "legacy"
	body = renderCheckout(t, client, srv.URL+path)
	if !strings.Contains(body, `id="interswitch-form"`) || !strings.Contains(body, "newwebpay-sandbox.interswitchng.com") {
		t.Fatalf("legacy mode did not render the auto-submitting newwebpay redirect form\n%s", body[:min(len(body), 600)])
	}
	if strings.Contains(body, "/static/hosted_fields.js") {
		t.Fatalf("legacy mode unexpectedly rendered the Hosted Fields page")
	}
}

func renderCheckout(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	}
	return string(raw)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}