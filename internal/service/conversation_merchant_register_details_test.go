package service

// Simulation coverage for the aggregated merchant KYB registration chain:
// the email code still verifies exactly as before, but the old
// name → category → description one-message-per-field chain is now one
// merchant_register_details message with friendly partial-capture re-asks.
// A garbled registration (short name, nonsense category, too-short
// description) must be rejected by name while preserving what was captured,
// and a complete aggregated message must create the pending registration
// row and land the customer back on the menu.
//
// TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestMerchantRegistrationDetails -v

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/store"
)

func TestMerchantRegistrationDetails(t *testing.T) {
	svc, msg, repo, _ := newOnboardingEmailTestService(t)
	// The register-merchant menu entry refuses to start the KYB chain unless
	// email confirmation is enabled; turn it on before walking onboarding.
	svc.cfg.EmailConfirmationEnabled = true
	ctx := context.Background()
	number := "+2348099001177"
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
			t.Fatalf("no messages sent")
		}
		return sent[len(sent)-1]
	}

	// Onboard: one message carries the name and the email (the aggregated
	// identity pair), then the emailed code verifies in chat.
	send("hi")
	send("Kola Merchant\nkola.merchant@example.com")
	code := chatEmailCodeFromMessages(t, msg.allSentText())
	send(code)

	// Finish onboarding: the aggregated Tier-1 details message, then the
	// number confirmation, lands the user on the menu.
	send("DOB: 1992-05-24\nGender: Female\n14 Admiralty Way, Lekki, Lagos")
	send("confirm")

	// Start the KYB chain: user has a verified email → straight to the code
	// step for the registration contact email (the demo-code fallback shows
	// the code in chat; the SMTP path says a 6-digit code was sent).
	send("register merchant")
	code = chatEmailCodeFromMessages(t, msg.allSentText())

	// Verify: the aggregated merchant details prompt follows.
	send(code)
	if !strings.Contains(lastSent(), "one message") || !strings.Contains(lastSent(), "Name:") {
		t.Fatalf("after code verify = %q, want the aggregated details prompt with an example", lastSent())
	}

	// Garbled attempt: invalid on all three fields. The reply must name the
	// business-name rule, echo the captured note, and re-prompt.
	send("A")
	if !strings.Contains(lastSent(), "business name") {
		t.Fatalf("garbled details reply = %q, want the business-name problem", lastSent())
	}

	// Partial capture: a valid name plus a too-short description keeps the
	// name (captured note) and re-asks only for what is missing.
	send("Name: Kola Foods\nDescription: short")
	sent := lastSent()
	if !strings.Contains(sent, "business name") {
		t.Fatalf("partial-capture reply = %q, want the captured business name echoed", sent)
	}
	if !strings.Contains(sent, "category") || !strings.Contains(sent, "description") {
		t.Fatalf("partial-capture reply = %q, want category and description re-asked", sent)
	}

	// Complete the form in one message: the registration row is created with
	// the merged values, the confirmation names it as awaiting approval, and
	// the session lands back on the menu.
	send("Name: Kola Foods\nCategory: Food\nDescription: Home-cooked meals delivered across Lagos mainland")
	confirmation := lastSent()
	if !strings.Contains(confirmation, "registration submitted") || !strings.Contains(confirmation, "Kola Foods") || !strings.Contains(confirmation, "Awaiting approval") {
		t.Fatalf("completion reply = %q, want the submitted confirmation with the business name", confirmation)
	}

	// The pending registration row exists for this user with the parsed
	// fields.
	regs, err := repo.ListMerchantRegistrations(ctx, 10)
	if err != nil {
		t.Fatalf("ListMerchantRegistrations: %v", err)
	}
	found := false
	for _, r := range regs {
		if r.BusinessName == "Kola Foods" && r.Category == "Food" && strings.Contains(r.Description, "Home-cooked") && r.Status == "awaiting_approval" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no pending Kola Foods registration row found in %d rows", len(regs))
	}

	// Reference format sanity (shared prefix with the rest of the platform).
	if !regexp.MustCompile(`^[A-Z0-9-]+$`).MatchString(regs[0].Reference) {
		t.Fatalf("reference %q does not look like a platform reference", regs[0].Reference)
	}
}
