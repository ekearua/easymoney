package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// TestPayIndividualNonsenseBankReject ensures a nonsense bank query with no
// fuzzy matches at all still gets the FSM's friendly "couldn't find that bank"
// re-ask — not an error, not a picker. The media smoke walk pins the trigram
// matcher's *echoing picker* for near-miss queries ("moonbeam trust"); this
// test pins the zero-match branch so both re-ask styles stay covered.
func TestPayIndividualNonsenseBankReject(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated test in short mode")
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
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		AIEnabled:                  true,
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}
	messenger := &simMessenger{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	payments := service.NewPaymentService(cfg, repository, map[string]ports.PaymentGateway{
		service.ProviderBankTransfer: &simGateway{store: repository},
	}, service.NewProviderRouter(nil, logger), logger)
	svc := service.NewConversationService(cfg, repository, payments, nil,
		map[string]ports.Messenger{service.ChannelWhatsApp: messenger},
		nil, stubIdentityVerifier{}, stubSanctionsScreener{})

	// An approved L2 individual payer can reach the bank step.
	user, err := repository.GetOrCreateUser(ctx, "+2348010000099")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, user.ID, "Ada Smoke",
		time.Date(1992, 5, 24, 0, 0, 0, 0, time.UTC), "14 Admiralty Way, Lekki, Lagos", "Product designer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		t.Fatal(err)
	}

	// Drive the FSM straight to the bank step, then send a nonsense query.
	// "zzqxj vvvv" scores below the trigram matcher's 0.3 word-similarity
	// floor for every directory row (verified against the seeded banks), so
	// ResolveBank returns zero candidates — unlike near-miss queries such as
	// "moonbeam trust", which fuzzy-match and get the echoing picker.
	session := store.Session{
		UserID: user.ID, State: "pay_individual_bank_code",
		Data: map[string]string{
			"recipient_phone": "+2348039999900", "amount_kobo": "500000",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repository.SaveSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := svc.Handle(ctx, store.InboundMessage{
		ID: "nonsense-bank-1", Channel: service.ChannelWhatsApp, Sender: "+2348010000099",
		Recipient: "+2348010000099", Text: "zzqxj vvvv",
	}); err != nil {
		t.Fatal(err)
	}

	after, err := repository.LoadSession(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "pay_individual_bank_code" {
		t.Fatalf("nonsense bank must stay on the bank step, state is %q", after.State)
	}
	replies := messenger.snapshot()
	if len(replies) == 0 {
		t.Fatal("nonsense bank must produce a friendly re-ask, got no reply")
	}
	last := replies[len(replies)-1]
	if last.kind == "interactive" {
		t.Fatalf("zero-match query must not render the picker, got %q", last.body)
	}
	if !strings.Contains(last.body, "couldn't find that bank") {
		t.Fatalf("zero-match query must get the 'couldn't find that bank' re-ask, got %q", last.body)
	}
}
