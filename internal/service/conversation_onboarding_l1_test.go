package service

// Coverage for the "enforce KYC L1 at onboarding" fix and the chat "My
// details" block.
//
// TestOnboardingConfirmAdvancesToL1 regresses the wallet-topup bug: completing
// onboarding stamped onboarding_complete but never advanced the KYC tier, so
// the wallet stayed pending (L0) and wallet options dead-ended. Confirm must
// now advance to L1 (channel + email evidence), which activates the wallet.
//
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestOnboarding -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func TestOnboardingConfirmAdvancesToL1AndActivatesWallet(t *testing.T) {
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

	msg := &carryoverMessenger{}
	svc := NewConversationService(config.Config{WebFlowsEnabled: false, AppName: "Xego", SessionTTL: 24 * time.Hour}, repository, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp: msg,
	}, nil, nil, nil)

	number := "+2348099001122"
	// First message creates the user (FirstContact) and starts chat onboarding.
	if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: "hi"}); err != nil {
		t.Fatalf("first Handle failed: %v", err)
	}
	if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: "Ada Obi"}); err != nil {
		t.Fatalf("name Handle failed: %v", err)
	}
	if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: "ada@example.com"}); err != nil {
		t.Fatalf("email Handle failed: %v", err)
	}
	if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: "confirm"}); err != nil {
		t.Fatalf("confirm Handle failed: %v", err)
	}

	user, err := repository.FindUserByWhatsAppNumber(ctx, normalizePhone(number))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kyc.Order(profile.Tier) < kyc.Order(kyc.TierL1) {
		t.Fatalf("onboarding confirm must reach L1, got tier %s", profile.Tier)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatalf("expected a wallet after onboarding: %v", err)
	}
	if wallet.Status != store.WalletStatusActive {
		t.Fatalf("wallet must be active after reaching L1, got %s", wallet.Status)
	}
	foundLevelMsg := false
	for _, body := range msg.allSentText() {
		if strings.Contains(body, "Level 1") {
			foundLevelMsg = true
		}
	}
	if !foundLevelMsg {
		t.Fatalf("confirmation message should mention Level 1, got %q", msg.allSentText())
	}
}

func TestMyDetailsChatBlockOnTelegram(t *testing.T) {
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

	msg := &carryoverMessenger{}
	svc := NewConversationService(config.Config{WebFlowsEnabled: false, AppName: "Xego"}, repository, nil, nil, map[string]ports.Messenger{
		ChannelTelegram: msg,
	}, nil, nil, nil)

	user, err := repository.GetOrCreateUser(ctx, "+2348099001123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}
	// Telegram has no web flow, so "my details" stays a chat block.
	if err := svc.handleMyDetails(ctx, ChannelTelegram, "123456789", user, store.Session{State: "menu"}); err != nil {
		t.Fatalf("handleMyDetails failed: %v", err)
	}
	for _, body := range msg.allSentText() {
		for _, want := range []string{"Your Xego details", "Verification tier", "Wallet", "No standing account number"} {
			if !strings.Contains(body, want) {
				t.Fatalf("telegram details block missing %q in: %q", want, body)
			}
		}
	}
}
