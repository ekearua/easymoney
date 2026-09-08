package app

// TestAutoRefundSupersededWebFlowAttempt exercises the complete double-charge
// prevention path: a gateway attempt A is abandoned (the flow is reopened for
// retry, marking A superseded), a new attempt B succeeds and claims the flow,
// and then A's gateway callback arrives late as succeeded. The app must
// auto-refund A and send exactly one refund message, without claiming any flow
// or producing a second message-2.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestAutoRefundSupersededWebFlowAttempt -v

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
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

	"github.com/google/uuid"
)

func TestAutoRefundSupersededWebFlowAttempt(t *testing.T) {
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	fmt.Printf("\n========== SUPERSEDE → AUTO-REFUND ==========\n")

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
		"money": domain.FormatNGN, "maskPII": func(s string) string { return s },
		"statusClass": func(s any) string { return strings.ReplaceAll(fmt.Sprint(s), "_", "-") },
		"percent":     func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
		"sub":         func(a, b int64) int64 { return a - b }, "add": func(a, b int64) int64 { return a + b },
		"inc": func(i int) int { return i + 1 },
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
	} // we call internal methods directly, no HTTP server needed

	payer, err := repository.GetOrCreateUser(ctx, "+2348012349099")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, payer.ID, "Test Auto Refund",
		time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), "1 Test St, Lagos", "Tester"); err != nil {
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

	// 1. Mint an open web flow for a card payment
	flow, err := repository.MintWebFlow(ctx, payer.ID, "whatsapp", service.WebFlowPay,
		map[string]string{"merchant_slug": "lagos-lunchbox", "amount_kobo": "250000"},
		"review", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create payment A (the gateway attempt that will be abandoned).
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	paymentA, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: payer.ID, MerchantID: merchant.ID, AmountKobo: 250_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: service.ProviderInterswitch,
		ProviderReference: "ref-supersede-a-" + uuid.NewString()[:6],
		Channel:           "whatsapp", ReceiptToken: "tok-a-" + uuid.NewString()[:6],
		Recipient: payer.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, paymentA.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetCheckout(ctx, paymentA.ID, "https://checkout.sim.example/x/"+paymentA.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", map[string]string{
		"payment_id": paymentA.ID.String(), "merchant_slug": "lagos-lunchbox", "amount_kobo": "250000",
	}); err != nil {
		t.Fatal(err)
	}

	// 3. The customer never completed the hosted page — A is still initialized
	// when maybeComplete reopens the flow for retry and marks A superseded.
	initA, err := repository.PaymentByID(ctx, paymentA.ID)
	if err != nil {
		t.Fatal(err)
	}
	flowToken := a.maybeCompleteWebFlowForPayment(ctx, initA)
	if flowToken == "" {
		t.Fatal("expected reopened flow token for declined attempt")
	}
	superseded, err := repository.WebFlowAttemptSuperseded(ctx, paymentA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !superseded {
		t.Fatal("declined attempt should be marked superseded")
	}

	// 4. Create payment B and attach it to the flow, then claim with B succeeded.
	paymentB, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: payer.ID, MerchantID: merchant.ID, AmountKobo: 250_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: service.ProviderInterswitch,
		ProviderReference: "ref-supersede-b-" + uuid.NewString()[:6],
		Channel:           "whatsapp", ReceiptToken: "tok-b-" + uuid.NewString()[:6],
		Recipient: payer.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, paymentB.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetCheckout(ctx, paymentB.ID, "https://checkout.sim.example/x/"+paymentB.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", map[string]string{
		"payment_id": paymentB.ID.String(), "merchant_slug": "lagos-lunchbox", "amount_kobo": "250000",
	}); err != nil {
		t.Fatal(err)
	}
	// Message 2 is the app's job on the web rail (the flow claims and sends it),
	// so B succeeds without an outbox row here.
	if _, err := repository.TransitionPayment(ctx, paymentB.ID, domain.StatusSucceeded, "gateway.verify", nil); err != nil {
		t.Fatalf("B succeeded: %v", err)
	}
	successB, err := repository.PaymentByID(ctx, paymentB.ID)
	if err != nil {
		t.Fatal(err)
	}
	messenger.reset()
	flowToken2 := a.maybeCompleteWebFlowForPayment(ctx, successB)
	if flowToken2 != "" {
		t.Fatalf("success path should claim and return no token, got %q", flowToken2)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].body, "Payment received") {
		t.Fatalf("B's claim should send exactly one receipt, got %+v", sent)
	}
	claimed, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status == store.WebFlowOpen {
		t.Fatal("B's success should claim the flow, not leave it open")
	}
	if n := countRefs("auto-refund", t, repository, paymentA.ID); n != 0 {
		t.Fatalf("no refund should exist before A succeeds, got %d", n)
	}

	// 5. A's gateway callback arrives late — A is now succeeded.
	if _, err := repository.TransitionPayment(ctx, paymentA.ID, domain.StatusSucceeded, "gateway.verify-late", nil); err != nil {
		t.Fatalf("A late succeed: %v", err)
	}
	lateA, err := repository.PaymentByID(ctx, paymentA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lateA.Status != domain.StatusSucceeded {
		t.Fatalf("A should be succeeded, got %s", lateA.Status)
	}
	messenger.reset()
	a.autoRefundSupersededWebFlowAttempt(ctx, lateA)

	// 6. Assert: A is refunded, one refund row, one message, ledger nets to zero.
	if n := countRefs("auto-refund", t, repository, paymentA.ID); n != 1 {
		t.Fatalf("expected exactly 1 auto-refund row for A, got %d", n)
	}
	refundView, err := repository.PaymentByID(ctx, paymentA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refundView.Status != domain.StatusRefunded {
		t.Fatalf("A status = %q, want refunded", refundView.Status)
	}
	sent = messenger.snapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].body, "automatically refunded") {
		t.Fatalf("expected exactly 1 refund notification, got %+v", sent)
	}

	balances, err := repository.LedgerBalanceSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range balances {
		total += b.NetKobo
	}
	if total != 0 {
		t.Fatalf("ledger out of balance after auto-refund: total net = %d", total)
	}

	// 7. Idempotency: a second call is a no-op.
	messenger.reset()
	a.autoRefundSupersededWebFlowAttempt(ctx, lateA)
	if n := countRefs("auto-refund", t, repository, paymentA.ID); n != 1 {
		t.Fatalf("second auto-refund must be a no-op, got %d total", n)
	}
	if len(messenger.snapshot()) != 0 {
		t.Fatal("second call must not send another message")
	}
}

func countRefs(pattern string, t *testing.T, repository *store.Store, paymentID uuid.UUID) int {
	t.Helper()
	n, err := repository.CountRefundsForPayment(context.Background(), paymentID, pattern)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
