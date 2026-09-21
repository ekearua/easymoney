package app

// TestChangeMethodRewindsCheckout covers the "dead end after gateway hand-off"
// defect: a money flow parked on its "checkout"/"done" step must render the
// consistent in-progress page (not 500 on the unhandled step, which previously
// killed revisits), and the "Change payment method" action must rewind the
// flow to its review step with the abandoned method preselected and the
// abandoned payment attempt marked superseded. A rewind of an already-succeeded
// payment redirects to the receipt instead — money that moved is never
// re-exposed.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestChangeMethodRewindsCheckout -v

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

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// newWebFlowRetryApp builds an App wired like the channel-webhook tests but
// with the shared templates parsed so the /w/{token} routes can actually
// render.
func newWebFlowRetryApp(t *testing.T, ctx context.Context, repository *store.Store, cfg config.Config) (*App, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)
	data := service.NewDataService(repository, payments, stubDataProvider{})
	convo := service.NewConversationService(cfg, repository, payments, data,
		map[string]ports.Messenger{service.ChannelWhatsApp: &simMessenger{}},
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
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return a, srv
}

func TestChangeMethodRewindsCheckout(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the web-flow retry test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated web-flow retry test in short mode")
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
		AppName: "Xego", BaseURL: "https://demo.xego.ng",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            10_000,
		PaymentMaxKobo:            10_000_000,
		FeeCardBPS:                200,
		FeeCardFixedKobo:          10_000,
		FeeCardCapKobo:            350_000,
		SessionTTL:                30 * time.Minute,
		RateLimitPublicPerMinute:  6000,
	}
	a, srv := newWebFlowRetryApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000007")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := a.payments.CreateCollectionDraft(ctx, user, merchant, 49_255,
		service.ProviderInterswitch, service.ChannelWhatsApp, user.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	payment, err = a.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		t.Fatal(err)
	}
	if payment.CheckoutURL == "" {
		t.Fatal("test payment should have a checkout URL after InitializeCheckout")
	}

	payload := map[string]string{
		"merchant_slug": "lagos-lunchbox", "item_name": "Executive lunch",
		"amount_kobo": "49255", "payment_id": payment.ID.String(), "provider": service.ProviderInterswitch,
	}
	flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", service.WebFlowPay, payload, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Park the flow on its gateway checkout step exactly as wfRoutePayment does.
	if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", payload); err != nil {
		t.Fatal(err)
	}

	client := retryNoFollowClient()

	// GET on the parked checkout must render the in-progress page (this step
	// previously 500'd or rendered a blank "done" page).
	body := retryGET(t, client, srv.URL+"/w/"+flow.Token)
	if !strings.Contains(body, "Payment in progress") || !strings.Contains(body, "Change payment method") {
		t.Fatalf("parked checkout page missing the in-progress affordances\n%s", body[:min2(len(body), 600)])
	}
	if !strings.Contains(body, "Back to payment") || !strings.Contains(body, payment.CheckoutURL) {
		t.Fatalf("parked checkout page missing the back-to-payment link\n%s", body[:min2(len(body), 600)])
	}

	// A stale browser-back POST on the parked checkout must re-render, not 500.
	resp, err := client.PostForm(srv.URL+"/w/"+flow.Token, map[string][]string{"action": {"pay"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/w/"+flow.Token {
		t.Fatalf("stale checkout POST = %s → %q, want 303 to /w/{token}", resp.Status, resp.Header.Get("Location"))
	}

	// Change payment method rewinds to review.
	resp, err = client.PostForm(srv.URL+"/w/"+flow.Token, map[string][]string{"action": {"change_method"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/w/"+flow.Token {
		t.Fatalf("change_method POST = %s → %q, want 303 back to the flow GET", resp.Status, resp.Header.Get("Location"))
	}

	reopened, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Step != "" {
		t.Fatalf("after change_method the flow step = %q, want the one-page start (empty)", reopened.Step)
	}
	if reopened.Payload["payment_id"] != "" || reopened.Payload["provider"] != "" {
		t.Fatalf("reopened flow must clear payment_id/provider, got %#v", reopened.Payload)
	}
	if reopened.Payload["method"] != service.ProviderInterswitch {
		t.Fatalf("reopened flow should preselect the abandoned method, got method=%q", reopened.Payload["method"])
	}
	superseded, err := repository.WebFlowAttemptSuperseded(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !superseded {
		t.Fatalf("abandoned payment attempt should be superseded after change_method")
	}

	// The reopened one-page start is the payment step again: every rail
	// renders as its own option button, so the customer can retry the same
	// rail or switch, with their merchant and amount still prefilled.
	body = retryGET(t, client, srv.URL+"/w/"+flow.Token)
	if !strings.Contains(body, "Pay a merchant") {
		t.Fatalf("reopened flow did not render the one-page start\n%s", body[:min2(len(body), 600)])
	}
	for _, method := range []string{service.ProviderInterswitch, service.ProviderBankTransfer, service.ProviderWallet} {
		if !strings.Contains(body, `value="`+method+`"`) {
			t.Fatalf("reopened review is missing the %s option button\n%s", method, body[:min2(len(body), 900)])
		}
	}
}

// TestChangeMethodSucceededRedirectsToReceipt pins the guard that a payment
// which already succeeded is never rewound — change_method on a completed
// payment goes straight to the receipt.
func TestChangeMethodSucceededRedirectsToReceipt(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the web-flow retry test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated web-flow retry test in short mode")
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
		AppName: "Xego", BaseURL: "https://demo.xego.ng",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            10_000,
		PaymentMaxKobo:            10_000_000,
		FeeCardBPS:                200,
		FeeCardFixedKobo:          10_000,
		FeeCardCapKobo:            350_000,
		SessionTTL:                30 * time.Minute,
		RateLimitPublicPerMinute:  6000,
	}
	a, srv := newWebFlowRetryApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000008")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := a.payments.CreateCollectionDraft(ctx, user, merchant, 49_255,
		service.ProviderInterswitch, service.ChannelWhatsApp, user.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusSucceeded, "test", nil); err != nil {
		t.Fatal(err)
	}
	payment, err = repository.PaymentByID(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}

	payload := map[string]string{
		"merchant_slug": "lagos-lunchbox", "item_name": "Executive lunch",
		"amount_kobo": "49255", "payment_id": payment.ID.String(), "provider": service.ProviderInterswitch,
	}
	flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", service.WebFlowPay, payload, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", payload); err != nil {
		t.Fatal(err)
	}

	client := retryNoFollowClient()

	// A succeeded payment parked on checkout renders the receipt action...
	body := retryGET(t, client, srv.URL+"/w/"+flow.Token)
	if !strings.Contains(body, "View receipt") || !strings.Contains(body, "/receipts/"+payment.ReceiptToken) {
		t.Fatalf("succeeded payment page should show the receipt action\n%s", body[:min2(len(body), 600)])
	}

	// ...and change_method must land on the receipt, never rewind.
	resp, err := client.PostForm(srv.URL+"/w/"+flow.Token, map[string][]string{"action": {"change_method"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/receipts/") {
		t.Fatalf("change_method on a succeeded payment = %s → %q, want the receipt", resp.Status, resp.Header.Get("Location"))
	}
	after, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if after.Step != "checkout" {
		t.Fatalf("a succeeded payment must never rewind the flow; step = %q", after.Step)
	}
}

// TestParkedCheckoutRendersEveryMoneyFlow is the topup/data regression: every
// money flow parked on its gateway checkout step must render the in-progress
// page instead of 500'ing on the unhandled step.
func TestParkedCheckoutRendersEveryMoneyFlow(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the web-flow retry test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated web-flow retry test in short mode")
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
		AppName: "Xego", BaseURL: "https://demo.xego.ng",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            1_00,
		PaymentMaxKobo:            10_000_000,
		FeeCardBPS:                200,
		FeeCardFixedKobo:          10_000,
		FeeCardCapKobo:            350_000,
		SessionTTL:                30 * time.Minute,
		RateLimitPublicPerMinute:  6000,
	}
	_, srv := newWebFlowRetryApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000009")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 250_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: service.ProviderInterswitch,
		ProviderReference: "ref-parked-" + uuid.NewString()[:6],
		Channel:           "whatsapp",
		ReceiptToken:      "tok-parked-" + uuid.NewString()[:6],
		Recipient:         user.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	// pay_invoice loads the invoice by reference before dispatching on the
	// step, so a valid invoice must exist for that flow to reach checkout.
	invoice, err := repository.CreateInvoice(ctx, store.InvoiceSpec{
		MerchantID: merchant.ID, CreatedByUserID: user.ID,
		CustomerWhatsAppNumber: user.WhatsAppNumber, Reference: "XG-INV-RETRY",
		Items: []store.InvoiceItem{{Description: "Catering", Quantity: 1, UnitPriceKobo: 100_000}},
	})
	if err != nil {
		t.Fatal(err)
	}

	client := retryNoFollowClient()
	for _, flowType := range []string{
		service.WebFlowPayInvoice, service.WebFlowThriftContribute,
		service.WebFlowData, service.WebFlowTopup, service.WebFlowIndividualPay,
	} {
		payload := map[string]string{
			"amount_kobo": "1000", "data_plan": "mtn-500mb", "data_phone": "08031234567",
			"merchant_slug": "lagos-lunchbox", "thrift_name": "Ajo Fund",
			"invoice_reference": invoice.Reference,
			"payment_id":        payment.ID.String(), "provider": service.ProviderInterswitch,
		}
		flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", flowType, payload, "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", payload); err != nil {
			t.Fatal(err)
		}
		body := retryGET(t, client, srv.URL+"/w/"+flow.Token)
		if !strings.Contains(body, "Payment in progress") {
			t.Fatalf("flow %s parked on checkout must render the in-progress page\n%s", flowType, body[:min2(len(body), 400)])
		}
	}
}

// TestWalletReviewNoticeOnReopenedReview pins the inline wallet notice on the
// pay review. Once the wallet rail has been chosen the payload records it, so
// any later render of the review — a reload, a browser back, or the review a
// damaged checkout reopens on — has to explain that the wallet cannot pay yet
// instead of silently offering the rail again. A review with no recorded rail
// stays clean, so the notice never leaks onto a first visit.
func TestWalletReviewNoticeOnReopenedReview(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the web-flow retry test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated web-flow retry test in short mode")
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
		AppName: "Xego", BaseURL: "https://demo.xego.ng",
		InterswitchCheckoutRender: "hosted_fields",
		PaymentMinKobo:            10_000,
		PaymentMaxKobo:            10_000_000,
		SessionTTL:                30 * time.Minute,
		RateLimitPublicPerMinute:  6000,
	}
	_, srv := newWebFlowRetryApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000021")
	if err != nil {
		t.Fatal(err)
	}
	// L0 opens the pending individual wallet; activating it is what makes the
	// rail usable at all, so what the notice has to report here is the empty
	// balance rather than an inactive account.
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ActivateUserWallet(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	reviewPayload := map[string]string{
		"merchant_slug": "lagos-lunchbox",
		"item_name":     "Custom amount",
		"amount_kobo":   "250000",
	}
	parkReview := func(payload map[string]string) string {
		t.Helper()
		flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", service.WebFlowPay, payload, "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveWebFlowProgress(ctx, flow.Token, "review", payload); err != nil {
			t.Fatal(err)
		}
		return flow.Token
	}

	// First visit: no rail chosen yet, so the page carries no wallet copy.
	clean := retryGET(t, retryNoFollowClient(), srv.URL+"/w/"+parkReview(reviewPayload))
	if !strings.Contains(clean, "Review your payment") {
		t.Fatalf("review step did not render\n%s", clean[:min2(len(clean), 600)])
	}
	if strings.Contains(clean, "which is less than") {
		t.Fatalf("a review with no recorded rail must not show wallet copy\n%s", clean[:min2(len(clean), 600)])
	}

	// Reopened with the wallet rail recorded: the empty active wallet is named.
	walletPayload := map[string]string{}
	for k, v := range reviewPayload {
		walletPayload[k] = v
	}
	walletPayload["method"] = service.ProviderWallet
	body := retryGET(t, retryNoFollowClient(), srv.URL+"/w/"+parkReview(walletPayload))
	if !strings.Contains(body, "which is less than") {
		t.Fatalf("reopened review should explain the wallet balance\n%s", body[:min2(len(body), 900)])
	}
	// Every rail is still offered, so the customer can switch and retry.
	for _, method := range []string{service.ProviderInterswitch, service.ProviderBankTransfer, service.ProviderWallet} {
		if !strings.Contains(body, `value="`+method+`"`) {
			t.Fatalf("reopened review is missing the %s option button\n%s", method, body[:min2(len(body), 900)])
		}
	}
}

// retryNoFollowClient returns an HTTP client that never follows redirects so
// tests can assert the exact 303 destinations.
func retryNoFollowClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func retryGET(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(raw[:min2(len(raw), 200)])))
	}
	return string(raw)
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
