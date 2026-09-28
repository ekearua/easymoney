package service

// Simulation coverage for the aggregated CBN Tier 1 details step in chat
// onboarding (one message for DOB, gender and address, replacing the old
// one-message-per-field chain). Drives the real FSM against a fresh
// database: partial captures survive the friendly re-ask, invalid values are
// rejected by name, and the completed set advances to L1 exactly as before.
//
// TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestOnboardingTier1Details -v

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

func TestOnboardingTier1DetailsAggregated(t *testing.T) {
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
	svc := NewConversationService(config.Config{WebFlowsEnabled: false, AppName: "Xego", SessionTTL: 24 * time.Hour, EmailVerificationTTL: 10 * time.Minute, EmailDemoCodeInChat: true}, repository, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp: msg,
	}, nil, nil, nil)

	number := "+2348099001150"
	send := func(text string) {
		t.Helper()
		if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: text}); err != nil {
			t.Fatalf("Handle(%q) failed: %v", text, err)
		}
	}
	lastSent := func() string {
		t.Helper()
		sent := msg.allSentText()
		if len(sent) == 0 {
			t.Fatal("no messages sent")
		}
		return sent[len(sent)-1]
	}

	send("hi")              // FirstContact -> onboarding name prompt
	send("Ada Obi")         // name saved -> email prompt
	send("ada@example.com") // email saved -> demo code in chat
	code := chatEmailCodeFromMessages(t, msg.allSentText())
	send(code) // verified -> aggregated Tier-1 details prompt

	// The aggregated prompt arrives as one message listing all three fields.
	prompt := lastSent()
	if !strings.Contains(prompt, "Date of birth") || !strings.Contains(prompt, "Gender") || !strings.Contains(prompt, "address") {
		t.Fatalf("aggregated prompt = %q, want all three fields named", prompt)
	}

	// A partial message (DOB only) gets a re-ask naming exactly what is
	// missing and echoing the capture, not a restart.
	send("1992-05-24")
	reask := lastSent()
	if !strings.Contains(reask, "Got your date of birth") {
		t.Fatalf("partial re-ask = %q, want captured DOB echoed", reask)
	}
	if !strings.Contains(reask, "gender") || !strings.Contains(reask, "address") {
		t.Fatalf("partial re-ask = %q, want missing fields named", reask)
	}

	// An invalid gender is rejected by name; the captured DOB survives.
	send("Gender: maybe\n14 Admiralty Way, Lekki, Lagos")
	reask = lastSent()
	if !strings.Contains(reask, "MALE or FEMALE") {
		t.Fatalf("bad-gender re-ask = %q", reask)
	}
	if !strings.Contains(reask, "1992-05-24") {
		t.Fatalf("bad-gender re-ask lost the captured DOB: %q", reask)
	}

	// An under-18 DOB is rejected by name.
	send("Gender: FEMALE\nDOB: 2015-01-01\n14 Admiralty Way, Lekki, Lagos")
	reask = lastSent()
	if !strings.Contains(reask, "at least 18") {
		t.Fatalf("under-18 re-ask = %q", reask)
	}

	// The complete aggregated message advances to the confirm step: the last
	// text announces it and the interactive confirm prompt follows.
	send("DOB: 1992-05-24\nGender: Female\n14 Admiralty Way, Lekki, Lagos")
	all := strings.Join(append(append([]string{}, msg.allSentText()...), msg.allSentInteractive()...), "\n")
	if !strings.Contains(strings.ToLower(all), "confirm this account") {
		t.Fatalf("after complete details, messages = %q, want the confirm prompt", all)
	}
	send("confirm")

	user, err := repository.FindUserByWhatsAppNumber(ctx, normalizePhone(number))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kyc.Order(profile.Tier) < kyc.Order(kyc.TierL1) {
		t.Fatalf("onboarding must reach L1, got tier %s", profile.Tier)
	}
	iprofile, err := repository.IndividualProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("expected an individual profile: %v", err)
	}
	if iprofile.Gender != "female" || iprofile.Address != "14 Admiralty Way, Lekki, Lagos" {
		t.Fatalf("individual profile = %+v, want the aggregated values", iprofile)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatalf("expected a wallet after onboarding: %v", err)
	}
	if wallet.Status != store.WalletStatusActive {
		t.Fatalf("wallet must be active, got %s", wallet.Status)
	}
}
