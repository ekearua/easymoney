package service

// Simulation coverage for the aggregated individual-upgrade chain: the whole
// profile form (legal name through occupation) in one message, the photo ID
// type+number in one, BVN+NIN in one, and the EDD set in one — with the
// liveness confirmation deliberately kept as its own turn. Drives the real
// FSM against a fresh database with stub screening and identity providers,
// asserting the tier, evidence rows and stored profile at every gate.
//
// TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestIndividualUpgradeAggregated -v

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// stubScreener clears everyone; the screening branch is covered by the
// existing chain tests.
type stubScreener struct{}

func (stubScreener) Screen(context.Context, ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	return ports.ScreeningDecision{Decision: kyc.ScreenClear}, nil
}

// stubIdentity verifies every 11-digit number, so the walk reaches L2 and L3.
type stubIdentity struct{}

func (stubIdentity) VerifyIdentity(_ context.Context, req ports.IdentityVerificationRequest) (ports.IdentityVerificationResult, error) {
	return ports.IdentityVerificationResult{Status: "verified", ProviderRef: "stub-ref", MatchName: true, MatchDOB: true}, nil
}

func TestIndividualUpgradeAggregated(t *testing.T) {
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
	svc := NewConversationService(config.Config{WebFlowsEnabled: false, AppName: "Xego", SessionTTL: 24 * time.Hour, EmailVerificationTTL: 10 * time.Minute, EmailDemoCodeInChat: true, EmailConfirmationEnabled: true}, repository, nil, nil, map[string]ports.Messenger{
		ChannelTelegram: msg,
	}, nil, stubIdentity{}, stubScreener{})

	number := "+2348099001160"
	send := func(text string) {
		t.Helper()
		if err := svc.Handle(ctx, store.InboundMessage{Channel: ChannelTelegram, Sender: number, Recipient: number, Text: text}); err != nil {
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

	// Telegram has no web flow, so the "hi" put the fresh user into chat
	// onboarding; switch to the individual-upgrade service (the FSM asks for
	// confirmation first) and continue there.
	send("hi")
	send("Ada Obi") // chat onboarding name -> email prompt
	send("ada.upgrade@example.com")
	code := chatEmailCodeFromMessages(t, msg.allSentText())
	send(code) // verified -> aggregated Tier-1 details prompt
	send("DOB: 1992-05-24\nGender: Female\n14 Admiralty Way, Lekki, Lagos")
	send("confirm") // onboarding completes at L1 (kyc_profiles row created)

	// The individual-upgrade service re-verifies identity and collects the
	// profile form. Telegram's fresh-user path already ran onboarding, so the
	// upgrade begins with the user's on-file email code.
	send("become individual") // switch prompt (onboarding is complete now)
	send("yes")               // confirm the switch -> startIndividualUpgrade
	send("RESEND")            // fresh code for the upgrade verification
	code = chatEmailCodeFromMessages(t, msg.allSentText())
	send(code) // verified -> aggregated profile prompt

	// One message for the whole profile form. It runs screening, upserts the
	// profile, advances to L1 and prompts the photo ID.
	send("Name: Ada Obi\nDOB: 1992-05-24\nGender: Female\nAddress: 14 Admiralty Way, Lekki, Lagos\nOccupation: Software engineer")
	sent := msg.allSentText()
	joined := strings.Join(sent, "\n")
	if !strings.Contains(joined, "CBN Tier 1") {
		t.Fatalf("after profile form = %q, want the Tier 1 approval", sent)
	}
	// GetOrCreateTelegramUser is an upsert on the chat ID, so calling it
	// again returns the row the walk created.
	user, err := repository.GetOrCreateTelegramUser(ctx, number, "", "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Tier != kyc.TierL1 {
		t.Fatalf("tier after profile form = %s, want L1", profile.Tier)
	}

	// A partial ID message re-asks naming the missing part.
	send("PASSPORT")
	reask := lastSent()
	if !strings.Contains(reask, "ID number") {
		t.Fatalf("partial ID re-ask = %q, want the missing number named", reask)
	}

	// The complete ID message records government_photo_id and prompts both
	// electronic numbers.
	send("ID type: Passport\nID number: A12345678")
	if !strings.Contains(lastSent(), "BVN") || !strings.Contains(lastSent(), "NIN") {
		t.Fatalf("after photo ID = %q, want the BVN+NIN prompt", lastSent())
	}

	// A missing NIN re-asks naming it while the BVN capture survives. (A
	// too-short NIN parses as no NIN at all, so this is the missing path.)
	send("BVN: 22222222222\nNIN: 12345")
	reask = lastSent()
	if !strings.Contains(reask, "Still need your 11-digit NIN") || !strings.Contains(reask, "Got your BVN") {
		t.Fatalf("bad NIN re-ask = %q", reask)
	}

	// Both numbers verified in one message -> L2 and the EDD prompt.
	send("BVN: 22222222222\nNIN: 12345678901")
	if !strings.Contains(lastSent(), "CBN Tier 2") {
		t.Fatalf("after BVN+NIN = %q, want the Tier 2 approval", lastSent())
	}
	profile, err = repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Tier != kyc.TierL2 {
		t.Fatalf("tier after BVN+NIN = %s, want L2", profile.Tier)
	}
	// The data-subject export carries the verification evidence; decode it
	// and assert every electronic-ID row was recorded.
	export, err := repository.ExportUserData(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var verifs []struct {
		VerificationType string `json:"verification_type"`
		Status           string `json:"status"`
	}
	if err := json.Unmarshal(export.Verifications, &verifs); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, v := range verifs {
		if v.Status == "completed" {
			have[v.VerificationType] = true
		}
	}
	for _, want := range []string{kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified} {
		if !have[want] {
			t.Fatalf("missing evidence %s; have %v", want, have)
		}
	}

	// One message for the EDD set records both evidence rows and prompts the
	// liveness confirm, which stays its own turn.
	send("Utility ref: 12345678901\nSource of funds: SALARY")
	if !strings.Contains(lastSent(), "Type YES") {
		t.Fatalf("after EDD details = %q, want the liveness confirm", lastSent())
	}
	send("YES")
	if !strings.Contains(lastSent(), "CBN Tier 3") {
		t.Fatalf("after liveness = %q, want the Tier 3 completion", lastSent())
	}
	profile, err = repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Tier != kyc.TierL3 {
		t.Fatalf("tier after EDD = %s, want L3", profile.Tier)
	}
}
