package app

// HostedFields harness boots the real app HTTP server with the real Interswitch
// gateway, mints an unpaid card payment, and serves the genuine hosted-fields
// checkout page at an httptest URL. A Playwright script then opens that URL and
// asserts the SDK mounts its secure-field iframes.
//
//	TEST_DATABASE_URL=postgres://... INTERSWITCH_HARNESS=1 \
//	INTERSWITCH_CLIENT_ID=... INTERSWITCH_CLIENT_SECRET=... \
//	go test ./internal/app/ -run TestHostedFieldsHarness -v

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
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestHostedFieldsHarness(t *testing.T) {
	if os.Getenv("INTERSWITCH_HARNESS") != "1" {
		t.Skip("harness only")
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

	cfg := config.Config{
		AppName:                   "Xego",
		BaseURL:                   "https://harness.local",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            10_000,
		PaymentMaxKobo:            10_000_000,
		FeeCardBPS:                200,
		FeeCardFixedKobo:          10_000,
		FeeCardCapKobo:            350_000,
		RateLimitPublicPerMinute:  6000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway := interswitch.New(interswitch.Options{
		ClientID:     os.Getenv("INTERSWITCH_CLIENT_ID"),
		ClientSecret: os.Getenv("INTERSWITCH_CLIENT_SECRET"),
		MerchantCode: "MX286980",
		PayItemID:    "Default_Payable_MX286980",
		BaseURL:      "https://sandbox.interswitchng.com",
		Mode:         "TEST",
	})
	gateways := map[string]ports.PaymentGateway{service.ProviderInterswitch: gateway}
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
		interswitch: gateway,
		templates:   templates, rateLimiter: ratelimit.NewMemory(),
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	// Point the app at its own test URL so the gateway return page lands back on
	// this server (the customer's browser must be able to reach it).
	a.cfg.BaseURL = srv.URL

	payer, err := repository.GetOrCreateUser(ctx, "+2348012340111")
	if err != nil {
		t.Fatal(err)
	}
	_ = repository.ConfirmUserNumber(ctx, payer.ID)
	_, _ = repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "harness", Decision: whatsappkyc.ScreenClear,
	}, 30*24*time.Hour)
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, whatsappkyc.TierL1,
		[]string{whatsappkyc.EvChannelConfirmed}, nil); err != nil {
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
	fmt.Printf("HARNESS checkout_url=%s\n", srv.URL+"/checkout/interswitch/"+payment.ProviderReference)

	// Park forever so the browser can drive the page; killed via timeout.
	select {}
}
