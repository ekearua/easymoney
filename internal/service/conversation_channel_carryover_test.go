package service

// Regression coverage for the cross-channel identity carry-over fix: a
// customer whose GLOBAL identity already cleared the approved/money-out tier
// (individual account, KYC >= L2) must never be re-hijacked into per-channel
// onboarding (onboard_confirm_account) on a fresh channel. Instead they get a
// one-time non-blocking intro plus the menu, and the channel is durably
// stamped so the gate stays closed thereafter.
//
// The seed mirrors the real approved-individual sequence used in
// simulation tests: WhatsApp confirmation, identity profile (sets
// account_level='individual'), a clean screening, then the ladder promotion to
// L2 with channel + identity evidence. The fresh channel (telegram) is then
// attached to the same surviving row without a channel confirmation stamp -
// exactly the "approved user, brand-new channel" state the gate in Handle is
// meant to route to the intro instead of onboarding.
//
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestApprovedIndividualOnFreshChannel -v

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// carryoverMessenger records both outbound text and interactive bodies so a
// test can assert exactly which messages a channel received.
type carryoverMessenger struct {
	mu          sync.Mutex
	texts       []string
	interactive []string
}

func (m *carryoverMessenger) SendText(_ context.Context, _ string, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.texts = append(m.texts, body)
	return nil
}

func (m *carryoverMessenger) SendInteractive(_ context.Context, msg ports.InteractiveMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interactive = append(m.interactive, msg.Body)
	return nil
}

func (m *carryoverMessenger) SendCheckout(_ context.Context, _, _, _ string) error { return nil }
func (m *carryoverMessenger) SendLink(_ context.Context, _, _, _, _ string) error  { return nil }
func (m *carryoverMessenger) SendTemplate(_ context.Context, _, _ string, _ []string) error {
	return nil
}
func (m *carryoverMessenger) SendImage(_ context.Context, _ string, _ []byte, _ string) error {
	return nil
}

func (m *carryoverMessenger) allSentText() []string { return m.texts }
func (m *carryoverMessenger) allSentInteractive() []string {
	return m.interactive
}

func TestApprovedIndividualOnFreshChannelGetsIntroNotOnboarding(t *testing.T) {
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

	// Build the approved individual: confirmed WhatsApp primary, individual
	// account label (from UpsertIndividualProfile), clean screening, L2 ladder.
	user, err := repository.GetOrCreateUser(ctx, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, user.ID, "Ada Obi",
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
	// Reload so in-memory fields reflect the approved state written above.
	user, err = repository.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg := &carryoverMessenger{}
	cfg := config.Config{WebFlowsEnabled: false, AppName: "Xego"}
	svc := NewConversationService(cfg, repository, nil, nil, map[string]ports.Messenger{
		ChannelTelegram: msg,
	}, nil, nil, nil)
	if !svc.userIsApprovedIndividual(context.Background(), user) {
		t.Fatal("seed user should be an approved individual (individual + L2)")
	}

	// Attach a fresh telegram handle to the surviving primary row WITHOUT a
	// channel confirmation stamp: the exact "approved user, new channel" state
	// the gate routes to the one-time intro.
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	chatID := "8899445577"
	if _, err := pool.Exec(ctx, `
		UPDATE users
		SET telegram_chat_id=$2, telegram_user_id=$2, telegram_username='ada_tg',
		    telegram_verified_at=now(), updated_at=now()
		WHERE id=$1`, user.ID, chatID); err != nil {
		t.Fatal(err)
	}

	// Drive Handle with a plain telegram text.
	err = svc.Handle(ctx, store.InboundMessage{
		Channel:   ChannelTelegram,
		Sender:    chatID,
		Recipient: chatID,
		Username:  "ada_tg",
		Text:      "hi",
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	// Forward a second message: the one-time intro must not be repeated now
	// that the channel is durably stamped.
	err = svc.Handle(ctx, store.InboundMessage{
		Channel:   ChannelTelegram,
		Sender:    chatID,
		Recipient: chatID,
		Username:  "ada_tg",
		Text:      "hello again",
	})
	if err != nil {
		t.Fatalf("second Handle returned error: %v", err)
	}

	intros := 0
	for _, body := range msg.allSentText() {
		if strings.Contains(body, "It's the same you") {
			intros++
		}
	}
	if intros != 1 {
		t.Fatalf("expected exactly one non-blocking intro, got %d (texts: %q)", intros, msg.allSentText())
	}
	for _, body := range msg.allSentText() {
		if strings.Contains(body, "Confirm this account") {
			t.Fatalf("approved user was hijacked into account confirmation: %q", body)
		}
	}
	for _, body := range msg.allSentInteractive() {
		if strings.Contains(body, "Confirm this account") {
			t.Fatalf("approved user was sent account-confirmation buttons: %q", body)
		}
	}
	anyMenu := false
	for _, body := range msg.allSentInteractive() {
		if strings.Contains(body, "Welcome back to Xego") {
			anyMenu = true
		}
	}
	if !anyMenu {
		t.Fatalf("expected the menu after the intro, interactives: %q", msg.allSentInteractive())
	}

	// The gate must now be closed: the telegram channel is durably stamped and
	// the user's global approval data is untouched.
	afterMSG, err := repository.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !afterMSG.TelegramConfirmedAt.Valid {
		t.Fatal("telegram channel was not durably stamped")
	}
	if !svc.onboardingCompleteForChannel(afterMSG, ChannelTelegram) {
		t.Fatal("onboardingCompleteForChannel(telegram) should be true after the intro")
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kyc.Order(profile.Tier) < kyc.Order(kyc.TierL2) {
		t.Fatalf("KYC tier was downgraded by the channel stamp: %s", profile.Tier)
	}
	if afterMSG.AccountLevel != "individual" {
		t.Fatalf("account_level was clobbered: %q", afterMSG.AccountLevel)
	}
}

// TestUnapprovedUserOnFreshChannelStillOnboards guards the negative side: a
// user who has NOT reached the approved global tier still goes through
// per-channel onboarding (the intro+menu path is reserved for approved users).
func TestUnapprovedUserOnFreshChannelStillOnboards(t *testing.T) {
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

	// A confirmed WhatsApp user who has NOT been advanced to an approved tier.
	chatID := "5544332211"
	user, err := repository.GetOrCreateUser(ctx, "+2348099887766")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		UPDATE users
		SET telegram_chat_id=$2, telegram_user_id=$2, telegram_username='dave_tg',
		    telegram_verified_at=now(), updated_at=now()
		WHERE id=$1`, user.ID, chatID); err != nil {
		t.Fatal(err)
	}

	msg := &carryoverMessenger{}
	svc := NewConversationService(config.Config{WebFlowsEnabled: false, AppName: "Xego"}, repository, nil, nil, map[string]ports.Messenger{
		ChannelTelegram: msg,
	}, nil, nil, nil)

	if err := svc.Handle(ctx, store.InboundMessage{
		Channel: ChannelTelegram, Sender: chatID, Recipient: chatID,
		Username: "dave_tg", Text: "hi",
	}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if svc.userIsApprovedIndividual(context.Background(), user) {
		t.Fatal("unapproved user should not qualify for the intro path")
	}
	for _, body := range msg.allSentText() {
		if strings.Contains(body, "It's the same you") {
			t.Fatalf("unapproved user was handed the approved-user intro: %q", body)
		}
	}
	// The per-channel onboarding gate must still fire for this user.
	onboardingShown := false
	for _, body := range msg.allSentInteractive() {
		if strings.Contains(body, "Confirm this account") {
			onboardingShown = true
		}
	}
	if !onboardingShown {
		t.Fatalf("unapproved user should be routed through account confirmation, interactives: %q", msg.allSentInteractive())
	}
}
