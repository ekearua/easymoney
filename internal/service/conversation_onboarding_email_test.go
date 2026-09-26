package service

// Simulation coverage for the chat onboarding email-code step (the state
// that used to accept any input). Drives the real FSM against a fresh
// database: the emailed code is required (surfaced in chat only through the
// non-production EMAIL_DEMO_CODE_IN_CHAT fallback), a wrong code gets the
// friendly re-ask, RESEND issues a fresh code on cooldown, and the code is
// verified through the same bcrypt-hashed, attempt-limited store path the
// browser flow uses.
//
// TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestOnboardingEmailCode -v

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// chatEmailCodeFromMessages extracts the 6-digit demo code from the sent
// messages (EMAIL_DEMO_CODE_IN_CHAT puts it there in non-production).
func chatEmailCodeFromMessages(t *testing.T, bodies []string) string {
	t.Helper()
	re := regexp.MustCompile(`\b(\d{6})\b`)
	for i := len(bodies) - 1; i >= 0; i-- {
		if strings.Contains(bodies[i], "Code:") {
			if m := re.FindStringSubmatch(bodies[i]); m != nil {
				return m[1]
			}
		}
	}
	t.Fatalf("no demo email code found in sent messages: %q", bodies)
	return ""
}

func newOnboardingEmailTestService(t *testing.T) (*ConversationService, *carryoverMessenger, *store.Store, string) {
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
	msg := &carryoverMessenger{}
	svc := NewConversationService(config.Config{
		WebFlowsEnabled: false, AppName: "Xego", SessionTTL: 24 * time.Hour, EmailVerificationTTL: 10 * time.Minute, EmailDemoCodeInChat: true,
	}, repository, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp: msg,
	}, nil, nil, nil)
	return svc, msg, repository, databaseURL
}

func TestOnboardingEmailCodeVerification(t *testing.T) {
	svc, msg, _, _ := newOnboardingEmailTestService(t)
	ctx := context.Background()
	number := "+2348099001140"
	send := func(text string) {
		t.Helper()
		if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: text}); err != nil {
			t.Fatalf("Handle(%q) failed: %v", text, err)
		}
	}

	send("hi")                     // FirstContact → onboarding name prompt
	send("Ada Verify")             // name saved → email prompt
	send("ada.verify@example.com") // email saved → code prompt (demo code in chat)
	code := chatEmailCodeFromMessages(t, msg.allSentText())

	// A wrong code gets the friendly re-ask and stays in the code state.
	send("000000")
	sent := msg.allSentText()
	last := ""
	if len(sent) > 0 {
		last = sent[len(sent)-1]
	}
	if !strings.Contains(last, "incorrect or has expired") || !strings.Contains(last, "RESEND") {
		t.Fatalf("wrong-code reply = %q, want the re-ask naming RESEND", last)
	}

	// RESEND issues a fresh code (the cooldown is per-email creation, short
	// enough here because the demo path re-sends immediately in tests when
	// allowed; if the cooldown bites the reply says so and the original
	// code is still valid).
	send("RESEND")
	code = chatEmailCodeFromMessages(t, msg.allSentText())

	// The correct code advances to the DOB step (two messages: the
	// confirmation and the DOB prompt).
	send(code)
	sent = msg.allSentText()
	tail := sent[max(0, len(sent)-2):]
	verified := false
	for _, m := range tail {
		if strings.Contains(m, "Email verified") {
			verified = true
		}
	}
	if !verified {
		t.Fatalf("after correct code the last messages = %q, want 'Email verified'", tail)
	}

	// Continue: the rest of onboarding is unchanged, and completing it
	// reaches L1 with an active wallet.
	send("1992-05-24")
	send("female")
	send("14 Admiralty Way, Lekki, Lagos")
	send("confirm")
	user, err := svc.store.FindUserByWhatsAppNumber(ctx, normalizePhone(number))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := svc.store.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Tier != "L1" {
		t.Fatalf("tier after onboarding = %s, want L1", profile.Tier)
	}
	// The verified email is stamped on the user row (VerifyEmailCode sets
	// email_verified_at), which the old pass-through never did.
	fresh, err := svc.store.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.EmailVerifiedAt.Valid {
		t.Error("email_verified_at must be set after the chat code step verifies")
	}
}
