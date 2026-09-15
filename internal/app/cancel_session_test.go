package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// newCancelTestApp builds the same minimal App used by the channel webhook
// tests (real store + sim gateways + a capturing messenger) for WhatsApp.
func newCancelTestApp(t *testing.T, ctx context.Context, repository *store.Store, cfg config.Config) (*App, *simMessenger) {
	t.Helper()
	messenger := &simMessenger{}
	a, _ := newChannelWebhookApp(t, ctx, repository, map[string]ports.Messenger{
		service.ChannelWhatsApp: messenger,
	}, cfg)
	return a, messenger
}

// TestCancelResetsSessionAndAbandonsPayment verifies the global CANCEL
// interrupt: an in-flight (awaiting-confirmation) payment becomes abandoned,
// the conversation session returns to the empty/menu state, and the customer
// gets a quiet ack instead of the menu dump.
func TestCancelResetsSessionAndAbandonsPayment(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the cancel-session test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated cancel-session test in short mode")
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
		AppName:                    "Xego",
		BaseURL:                    "https://demo.xego.ng",
		WebFlowsEnabled:            false,
		MessageLogEnabled:          true,
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}
	a, messenger := newCancelTestApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000001")
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
		ProviderReference: "ref-cancel-" + uuid.NewString()[:6],
		Channel:           "whatsapp", ReceiptToken: "tok-cancel-" + uuid.NewString()[:6],
		Recipient: user.WhatsAppNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil {
		t.Fatal(err)
	}
	// Park the conversation on a live payment confirmation state.
	if err := repository.SaveSession(ctx, store.Session{
		UserID: user.ID, State: "confirm_payment",
		Data:      map[string]string{"payment_id": payment.ID.String(), "merchant_slug": "lagos-lunchbox", "amount_kobo": "250000"},
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.conversation.Handle(ctx, store.InboundMessage{
		ID: "cancel-1", Channel: service.ChannelWhatsApp,
		Sender: user.WhatsAppNumber, Recipient: user.WhatsAppNumber, Text: "cancel",
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := repository.LoadSession(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "menu" || len(loaded.Data) != 0 {
		t.Fatalf("after cancel session should be empty/menu, got state=%q data=%v", loaded.State, loaded.Data)
	}

	final, err := repository.PaymentByID(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.StatusAbandoned {
		t.Fatalf("in-flight payment should be abandoned on cancel, got %q", final.Status)
	}

	sent := messenger.snapshot()
	if len(sent) != 1 {
		t.Fatalf("cancel should send exactly the quiet ack, got %d messages", len(sent))
	}
	if !strings.Contains(sent[0].body, "Cancelled") {
		t.Fatalf("ack should mention Cancelled, got %q", sent[0].body)
	}
}

// TestCancelEscapesOnboardingWebFlow reproduces the "stuck on 'You're
// completing your profile in your browser'" report: a not-yet-onboarded
// customer parked on an onboarding web flow whose browser page was closed. The
// global CANCEL interrupt must reset the chat session AND cancel the stale
// open web flow so the link stops working.
func TestCancelEscapesOnboardingWebFlow(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the cancel web-flow test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated cancel web-flow test in short mode")
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
		WebFlowsEnabled: false, MessageLogEnabled: true,
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}
	a, messenger := newCancelTestApp(t, ctx, repository, cfg)

	user, err := repository.GetOrCreateUser(ctx, "+2348091000002")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", service.WebFlowOnboard, map[string]string{}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveSession(ctx, store.Session{
		UserID: user.ID, State: "web_flow_active",
		Data:      map[string]string{"web_flow_token": flow.Token},
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.conversation.Handle(ctx, store.InboundMessage{
		ID: "cancel-2", Channel: service.ChannelWhatsApp,
		Sender: user.WhatsAppNumber, Recipient: user.WhatsAppNumber, Text: "cancel",
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := repository.LoadSession(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "menu" || len(loaded.Data) != 0 {
		t.Fatalf("after cancel the session should be empty/menu, got state=%q data=%v", loaded.State, loaded.Data)
	}

	cancelled, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.WebFlowCancelled {
		t.Fatalf("open web flow should be cancelled on cancel, got status %q", cancelled.Status)
	}

	sent := messenger.snapshot()
	if len(sent) != 1 {
		t.Fatalf("cancel should send exactly the quiet ack, got %d messages", len(sent))
	}
	if !strings.Contains(sent[0].body, "Cancelled") {
		t.Fatalf("ack should mention Cancelled, got %q", sent[0].body)
	}
}