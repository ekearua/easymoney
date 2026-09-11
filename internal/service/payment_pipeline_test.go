package service

// Coverage-gap tests from reports/Payment_Pipeline_Test_Plan.xlsx:
//
//   - C2 (card declined): a gateway requery that reports "failed" must move the
//     initialized payment to a terminal failed state and must not post any
//     ledger entry (money-in is only ever booked on success).
//   - C5 (webhook-driven card success, idempotent): replaying the success path
//     (a duplicated TRANSACTION.COMPLETED webhook) must be a no-op: one
//     succeeded transition, one money-in posting, no double ledger postings.
//   - C6 (invoice payment via card): a public fill-in invoice contribution
//     resolved to a card payment must, on a successful requery, mark the
//     invoice paid and book both the money-in and the invoice allocation.
//
// Each test walks the same hosted-checkout state machine a real card charge
// does: draft -> awaiting_confirmation -> initialized (gateway minted) ->
// gateway requery. A scripted gateway stands in for the Interswitch sandbox.
//
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run 'TestCardPayment|TestInvoicePayment' -v

import (
	"context"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// scriptedGateway is a deterministic stand-in for the Interswitch gateway. The
// test sets verifyStatus before the requery it wants to simulate: "success" for
// an approved charge, "failed" for a decline. Initialize mints a checkout URL
// bound to the payment's provider reference like the real sandbox.
type scriptedGateway struct {
	store        *store.Store
	verifyStatus string
}

func (g *scriptedGateway) Initialize(_ context.Context, in ports.InitializePayment) (ports.Checkout, error) {
	return ports.Checkout{Reference: in.Reference, URL: "https://checkout.scripted.example/x/" + in.Reference}, nil
}

func (g *scriptedGateway) Verify(ctx context.Context, reference string, _ int64) (ports.Verification, error) {
	payment, err := g.store.PaymentByReference(ctx, reference)
	if err != nil {
		return ports.Verification{}, err
	}
	return ports.Verification{
		Reference:  reference,
		Status:     g.verifyStatus,
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

func (g *scriptedGateway) ValidateWebhook(_ []byte, _ string) (ports.GatewayWebhook, error) {
	return ports.GatewayWebhook{}, nil
}

// pipelineHarness owns the seed DB plus a real PaymentService wired to the
// scripted gateway, mirroring New*Service wiring in the app sim harness.
type pipelineHarness struct {
	t          *testing.T
	repository *store.Store
	payments   *PaymentService
	gateway    *scriptedGateway
}

func newPipelineHarness(t *testing.T) *pipelineHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BaseURL:                "https://demo.xego.ng",
		SessionTTL:             30 * time.Minute,
		PaymentMinKobo:         1_000,
		PaymentMaxKobo:         10_000_000,
		FeeCardBPS:             200,
		FeeCardFixedKobo:       10_000,
		FeeCardCapKobo:         350_000,
		WhatsAppTemplateName:   "payment_status_update",
		WhatsAppTemplateLocale: "en",
	}
	logger := testLogger()
	gateway := &scriptedGateway{store: repository, verifyStatus: "success"}
	gateways := map[string]ports.PaymentGateway{ProviderInterswitch: gateway}
	payments := NewPaymentService(cfg, repository, gateways, NewProviderRouter(gateways, logger), logger)
	return &pipelineHarness{t: t, repository: repository, payments: payments, gateway: gateway}
}

func (h *pipelineHarness) newPayer(t *testing.T, phone string) store.User {
	t.Helper()
	user, err := h.repository.GetOrCreateUser(context.Background(), phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repository.ConfirmUserNumber(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	return user
}

func (h *pipelineHarness) merchant(t *testing.T) store.Merchant {
	t.Helper()
	merchant, err := h.repository.MerchantBySlug(context.Background(), "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	return merchant
}

// newCardPayment runs the real hosted-checkout mint: a plain merchant
// collection draft (base + card fee) is created, then the gateway is
// initialized so the payment lands on "initialized" ready for the requery.
func (h *pipelineHarness) newCardPayment(t *testing.T, payer store.User, merchant store.Merchant) store.PaymentView {
	t.Helper()
	payment, err := h.payments.CreateCollectionDraft(context.Background(), payer, merchant, 5_000,
		ProviderInterswitch, ChannelWhatsApp, payer.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	payment, err = h.payments.InitializeCheckout(context.Background(), payment)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Status != domain.StatusInitialized {
		t.Fatalf("expected payment initialized before requery, got %s", payment.Status)
	}
	return payment
}

// ledgerForPayment returns every ledger row journaled for this payment, which
// is how money-in (source "payment"), the invoice allocation
// ("invoice_payment"), and collection splits ("payment_split") are identified.
func (h *pipelineHarness) ledgerForPayment(t *testing.T, payment store.PaymentView) []store.LedgerEntry {
	t.Helper()
	entries, err := h.repository.ListLedgerEntries(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]store.LedgerEntry, 0, 4)
	for _, e := range entries {
		if e.JournalRef == payment.ID.String() {
			out = append(out, e)
		}
	}
	return out
}

// C2: a declined card charge must terminate the payment and book no money.
func TestCardPaymentDeclined_C2(t *testing.T) {
	h := newPipelineHarness(t)
	payer := h.newPayer(t, "+2348012340666")
	merchant := h.merchant(t)
	payment := h.newCardPayment(t, payer, merchant)
	h.gateway.verifyStatus = "failed"

	updated, changed, err := h.payments.VerifyAndApply(context.Background(), payment.ProviderReference, "interswitch.webhook")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a declined verification must transition the payment")
	}
	if updated.Status != domain.StatusFailed {
		t.Fatalf("expected the payment to fail, got %s", updated.Status)
	}
	if reloaded, err := h.repository.PaymentByReference(context.Background(), payment.ProviderReference); err != nil {
		t.Fatal(err)
	} else if reloaded.Status != domain.StatusFailed {
		t.Fatalf("reloaded payment status = %s, want failed", reloaded.Status)
	}
	if entries := h.ledgerForPayment(t, payment); len(entries) != 0 {
		t.Fatalf("a declined card charge must not post ledger entries, got %d: %+v", len(entries), entries)
	}
}

// C5: a replayed success webhook must be idempotent — one succeeded transition
// and one money-in posting, no double ledger postings from the replay.
func TestCardPaymentWebhookIdempotent_C5(t *testing.T) {
	h := newPipelineHarness(t)
	payer := h.newPayer(t, "+2348012340777")
	merchant := h.merchant(t)
	payment := h.newCardPayment(t, payer, merchant)

	first, changedFirst, err := h.payments.VerifyAndApply(context.Background(), payment.ProviderReference, "interswitch.webhook")
	if err != nil {
		t.Fatal(err)
	}
	if !changedFirst || first.Status != domain.StatusSucceeded {
		t.Fatalf("first verification should succeed, changed=%t status=%s", changedFirst, first.Status)
	}
	posts := h.ledgerForPayment(t, first)
	moneyIn := 0
	for _, e := range posts {
		if e.SourceType == "payment" {
			moneyIn++
		}
	}
	if moneyIn != 2 {
		t.Fatalf("expected exactly one money-in pair, got %d entries", moneyIn)
	}

	again, changedAgain, err := h.payments.VerifyAndApply(context.Background(), first.ProviderReference, "interswitch.webhook")
	if err != nil {
		t.Fatal(err)
	}
	if changedAgain {
		t.Fatal("a replayed success webhook must be a no-op (changed=false)")
	}
	if again.Status != domain.StatusSucceeded {
		t.Fatalf("replayed payment status = %s, want succeeded", again.Status)
	}
	after := h.ledgerForPayment(t, again)
	if len(after) != len(posts) {
		t.Fatalf("replayed webhook must not double-post ledger entries: before %d, after %d", len(posts), len(after))
	}
	if !h.payments.IsGatewaySuccessEvent(ProviderInterswitch, "TRANSACTION.COMPLETED") {
		t.Fatal("TRANSACTION.COMPLETED must be the terminal interswitch success event")
	}
}

// C6: a public invoice contribution paid by card settles the invoice and books
// both the money-in and the invoice allocation.
func TestInvoicePaymentViaCard_C6(t *testing.T) {
	h := newPipelineHarness(t)
	ctx := context.Background()
	merchant := h.merchant(t)
	creator := h.newPayer(t, "+2348012340888")
	payer := h.newPayer(t, "+2348012340999")

	invoice, err := h.repository.CreateInvoice(ctx, store.InvoiceSpec{
		MerchantID:             merchant.ID,
		CreatedByUserID:        creator.ID,
		CustomerWhatsAppNumber: payer.WhatsAppNumber,
		Reference:              "XG-INV-CARDTEST-1",
		DeliveryFeeKobo:        1_000,
		Items: []store.InvoiceItem{
			{Description: "Plantain chips (crate)", Quantity: 1, UnitPriceKobo: 24_000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if invoice.TotalKobo != 25_000 {
		t.Fatalf("unexpected invoice total %d", invoice.TotalKobo)
	}

	payment, err := h.payments.ResolveInvoicePayment(ctx, invoice, merchant, payer.WhatsAppNumber, invoice.TotalKobo, ProviderInterswitch)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Provider != ProviderInterswitch || payment.Channel != ChannelCheckout {
		t.Fatalf("unexpected invoice payment rails: provider=%s channel=%s", payment.Provider, payment.Channel)
	}
	if linked, err := h.repository.CountInvoicePayments(ctx, invoice.ID); err != nil {
		t.Fatal(err)
	} else if linked != 1 {
		t.Fatalf("expected one linked invoice payment, got %d", linked)
	}

	payment, err = h.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := h.payments.VerifyAndApply(ctx, payment.ProviderReference, "interswitch.webhook")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.Status != domain.StatusSucceeded {
		t.Fatalf("invoice card charge should succeed, changed=%t status=%s", changed, updated.Status)
	}

	settled, err := h.repository.InvoiceByReference(ctx, invoice.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if settled.AmountPaidKobo != invoice.TotalKobo {
		t.Fatalf("invoice paid %d, want %d", settled.AmountPaidKobo, invoice.TotalKobo)
	}
	if settled.Status != "paid" {
		t.Fatalf("invoice status = %q, want paid", settled.Status)
	}

	entries := h.ledgerForPayment(t, payment)
	if len(entries) != 4 {
		t.Fatalf("expected money-in + invoice allocation (4 entries), got %d: %+v", len(entries), entries)
	}
	allocation := 0
	for _, e := range entries {
		if e.SourceType == "invoice_payment" && e.Account == store.LedgerAccountMerchantPayable {
			allocation++
		}
	}
	if allocation != 1 {
		t.Fatalf("expected one merchant payable allocation for the invoice, got %d", allocation)
	}
}