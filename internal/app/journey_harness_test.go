package app

// Journey harness boots the real app with the simulated gateway, mints a
// "make payment" web flow for a funded payer, and prints the flow URL so a
// Playwright script can drive the shortened journey end to end with screenshots.
//
// Set JOURNEY_MODE=transfer to exercise the bank-transfer (DVA) path instead
// of the default card path.
//
//	TEST_DATABASE_URL=postgres://... XEGO_JOURNEY=1 \
//	go test ./internal/app/ -run TestJourneyHarness -v

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	whatsappkyc "whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestJourneyHarness(t *testing.T) {
	if os.Getenv("XEGO_JOURNEY") != "1" {
		t.Skip("journey harness only")
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

	// lagos-lunchbox ships with an empty catalog, which would auto-skip the
	// item step (single-option). Seed one fixed-price service so the drive
	// exercises the item+qty fold page exactly as the wireframes pin it.
	if _, err := repository.RawExec(ctx, `INSERT INTO merchant_services (merchant_id, name, description, unit_price_kobo, is_active)
		SELECT id, 'Jollof Combo', 'journey fixture', 250000, true FROM merchants WHERE slug='lagos-lunchbox'
		AND NOT EXISTS (SELECT 1 FROM merchant_services s JOIN merchants m ON m.id=s.merchant_id WHERE m.slug='lagos-lunchbox' AND s.is_active)`); err != nil {
		t.Fatal(err)
	}

	// Bind the server port FIRST so cfg.BaseURL can carry the live URL from
	// construction time: PaymentService composes DVA checkout URLs from its
	// own config copy, so a post-hoc fix-up would leave it pointing at a dead
	// placeholder host. NewUnstartedServer creates the listener immediately.
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()

	transferMode := os.Getenv("JOURNEY_MODE") == "transfer"
	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  srv.URL,
		WebFlowsEnabled:          true,
		SessionTTL:               30 * time.Minute,
		PaymentMinKobo:           10_000,
		PaymentMaxKobo:           10_000_000,
		FeeCardBPS:               200,
		FeeCardFixedKobo:         10_000,
		FeeCardCapKobo:           350_000,
		RateLimitPublicPerMinute: 6000,
		BankTransferMode:         "interswitch",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Same-origin checkout: the real Interswitch client's PayPageURL points
	// the browser back at this platform's own hosted-fields page
	// (/checkout/interswitch/{ref}), so the journey drive can screenshot the
	// genuine gateway page instead of a dead external simulator host.
	var serverBase atomic.Value // string, set once the server is up
	journeyGW := &journeyGateway{store: repository, base: &serverBase}
	journeyTransferGW := &journeyTransferGateway{store: repository, base: &serverBase}
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  journeyGW,
		service.ProviderBankTransfer: journeyTransferGW,
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
	srv.Config.Handler = a.routes()
	srv.Start()
	serverBase.Store(srv.URL)

	// Payer: confirmed, screened, L2 — able to pay and use the wallet.
	payer, err := repository.GetOrCreateUser(ctx, "+2348012340999")
	if err != nil {
		t.Fatal(err)
	}
	_ = repository.ConfirmUserNumber(ctx, payer.ID)
	_, _ = repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "journey", Decision: whatsappkyc.ScreenClear,
	}, 30*24*time.Hour)
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, whatsappkyc.TierL2,
		[]string{whatsappkyc.EvChannelConfirmed, whatsappkyc.EvIdentityOnFile}, nil); err != nil {
		t.Fatal(err)
	}
	// Fund a wallet so the journey can ALSO prove the wallet rail end to end.
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, payer.ID, store.LedgerAccountOperatingBank, 1_000_000, "journey:fund", "journey funding"); err != nil {
		t.Fatal(err)
	}
	_ = wallet

	// Start the "make payment" flow exactly as WhatsApp does.
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "make payment"}); err != nil {
		t.Fatal(err)
	}
	sent := messenger.snapshot()
	if len(sent) == 0 || sent[0].kind != "link" {
		t.Fatalf("expected a link message, got %+v", sent)
	}
	flowURL := srv.URL + "/w/" + strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	fmt.Printf("JOURNEY flow_url=%s\n", flowURL)
	fmt.Printf("JOURNEY wallet_balance_kobo=%d\n", 1_000_000)
	fmt.Printf("JOURNEY mode=%s\n", map[bool]string{true: "transfer", false: "card"}[transferMode])
	// Write the URL where a waiting driver script can pick it up instantly
	// (the flow's expiry window is short, so polling logs is too slow).
	if out := os.Getenv("JOURNEY_URL_FILE"); out != "" {
		if err := os.WriteFile(out, []byte(flowURL), 0o644); err != nil {
			t.Logf("write journey url file: %v", err)
		}
	}

	// Park while the browser drives the app. The parking deadline comes from
	// PARK_MINUTES (default 5) because `go test` panics any test still running
	// at 10 minutes, which would kill the server mid-drive.
	park := 5 * time.Minute
	if v := os.Getenv("PARK_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 && mins < 9 {
			park = time.Duration(mins) * time.Minute
		}
	}
	time.Sleep(park)
}

// journeyGateway mirrors the real Interswitch client's checkout shape: the
// browser is sent to the platform's OWN hosted-fields page
// (/checkout/interswitch/{reference}) rather than an external host, so the
// rendered gateway page is the genuine production page. Verify delegates to
// the shared simGateway so the wallet/confirmation machinery still works.
type journeyGateway struct {
	store *store.Store
	base  *atomic.Value
}

func (g *journeyGateway) Initialize(_ context.Context, in ports.InitializePayment) (ports.Checkout, error) {
	base, _ := g.base.Load().(string)
	if base == "" {
		return ports.Checkout{}, errors.New("journey gateway: server base not ready")
	}
	return ports.Checkout{Reference: in.Reference, URL: base + "/checkout/interswitch/" + url.PathEscape(in.Reference)}, nil
}

func (g *journeyGateway) Verify(ctx context.Context, reference string, amountKobo int64) (ports.Verification, error) {
	sim := simGateway{store: g.store}
	return sim.Verify(ctx, reference, amountKobo)
}

func (g *journeyGateway) ValidateWebhook(body []byte, signature string) (ports.GatewayWebhook, error) {
	sim := simGateway{store: g.store}
	return sim.ValidateWebhook(body, signature)
}

// journeyTransferGateway extends journeyGateway with TransferGateway support
// so the bank-transfer (DVA) path renders the same-origin /transfer/:ref page
type journeyTransferGateway struct {
	store *store.Store
	base  *atomic.Value
}

func (g *journeyTransferGateway) Initialize(_ context.Context, in ports.InitializePayment) (ports.Checkout, error) {
	base, _ := g.base.Load().(string)
	if base == "" {
		return ports.Checkout{}, errors.New("journey transfer gateway: server base not ready")
	}
	return ports.Checkout{Reference: in.Reference, URL: base + "/transfer/" + url.PathEscape(in.Reference)}, nil
}

func (g *journeyTransferGateway) Verify(ctx context.Context, reference string, amountKobo int64) (ports.Verification, error) {
	sim := simGateway{store: g.store}
	return sim.Verify(ctx, reference, amountKobo)
}

func (g *journeyTransferGateway) ValidateWebhook(body []byte, signature string) (ports.GatewayWebhook, error) {
	sim := simGateway{store: g.store}
	return sim.ValidateWebhook(body, signature)
}

func (g *journeyTransferGateway) Transfer(_ context.Context, in ports.InitializePayment) (ports.TransferInstruction, error) {
	return ports.TransferInstruction{
		AccountNumber: "3012345678",
		AccountName:   "Xego Journey",
		BankName:      "Wema Bank",
		Reference:     in.Reference,
		ExpiresAt:     time.Now().Add(30 * time.Minute),
		ValidityMins:  30,
	}, nil
}
