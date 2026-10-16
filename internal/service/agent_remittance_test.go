package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// fixedScreener is a hermetic G2 stand-in: every recipient screens to one verdict.
type fixedScreener struct {
	decision string
	matched  []string
}

func (f fixedScreener) Screen(context.Context, ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	return ports.ScreeningDecision{Decision: f.decision, MatchedNames: f.matched, ProviderRef: "fixed"}, nil
}

func (f fixedScreener) ProviderName() string { return "fixed" }

// TestAgentRemittanceRedeem exercises the agent-facing redemption wrapper: it
// screens the recipient, records that decision, delegates authorisation to the
// store choke point, and renders the receipt. It also pins the two refusals the
// wrapper owns — the feature kill switch and a blocked screen (which must move
// no money).
func TestAgentRemittanceRedeem(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := store.Open(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RawExec(ctx, `
		TRUNCATE remittance_payouts, remittance_otps, remittance_pickups, remittances,
		         agent_users, agent_profiles, agent_regions, payments, ledger_entries,
		         payment_screenings, allowance_usage, wallet_accounts, business_kyb_profiles,
		         kyc_profiles, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 1 {
		t.Skip("needs at least one seeded active merchant")
	}
	agent := merchants[0]
	sender, err := repository.GetOrCreateUser(ctx, "+2348000000020")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantType(ctx, agent.ID, store.MerchantTypeAgent); err != nil {
		t.Fatal(err)
	}
	region, err := repository.EnsureAgentRegion(ctx, store.AgentRegion{
		Code: "oyo-ibadan", Name: "Ibadan", State: "Oyo", LGA: "Ibadan North",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, store.AgentTierStandard); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentLimits(ctx, agent.ID, 500000, 200000, 500000, 50); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, store.AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	const amount = int64(100000)
	open := func() (store.Remittance, string, string) {
		code, _ := store.NewRemittanceCode()
		salt, _ := store.NewRemittanceSalt()
		r, _, err := repository.CreateRemittanceTx(ctx, store.CreateRemittanceParams{
			SenderUserID:    sender.ID,
			AgentMerchantID: agent.ID,
			RegionID:        region.ID,
			AmountKobo:      amount,
			FeeKobo:         2000,
			CommissionKobo:  500,
			RecipientName:   "Ada Obi",
			RecipientPhone:  "+2348000000021",
			PaymentID:       uuid.New(),
			CodeHash:        store.RemittanceCodeHash(salt, code),
			CodeSalt:        salt,
			ExpiresAt:       time.Now().Add(72 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		otp, _ := store.NewRemittanceCode()
		otpSalt, _ := store.NewRemittanceSalt()
		if err := repository.RequestRemittanceOTP(ctx, r.ID,
			store.RemittanceCodeHash(otpSalt, otp), otpSalt, time.Now().Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		return r, code, otp
	}

	cfg := config.Config{AgentNetworkEnabled: true, AgentIDRequired: true, AgentIDHashPepper: "svc-pepper"}

	// Happy path: no vendor wired records an explicit clear decision.
	r1, code1, otp1 := open()
	svc := NewAgentRemittanceService(cfg, repository, nil)
	receipt, err := svc.Redeem(ctx, RedeemRequest{
		AgentMerchantID: agent.ID, Code: code1, OTP: otp1,
		ReceiverIDType: "nin", ReceiverIDNumber: "12345678901",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Remittance.Status != store.RemittancePaid || receipt.Payout.ID == uuid.Nil {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.AgentName == "" || receipt.ReceiptText == "" {
		t.Fatalf("receipt missing agent name or text: %+v", receipt)
	}
	scr, found, err := repository.PaymentScreeningForRef(ctx, store.RailCashPickup, "remittance:"+r1.ID.String())
	if err != nil || !found {
		t.Fatalf("cash-pickup screening not recorded: found=%v err=%v", found, err)
	}
	if scr.Decision != kyc.ScreenClear {
		t.Fatalf("recorded decision = %q, want clear", scr.Decision)
	}

	// Kill switch: no redemption while the network is disabled.
	_, code2, otp2 := open()
	svcOff := NewAgentRemittanceService(config.Config{AgentNetworkEnabled: false}, repository, nil)
	if _, err := svcOff.Redeem(ctx, RedeemRequest{
		AgentMerchantID: agent.ID, Code: code2, OTP: otp2,
		ReceiverIDType: "nin", ReceiverIDNumber: "12345678902",
	}); err == nil {
		t.Fatal("redeem succeeded with the agent network disabled")
	}

	// A blocked screen refuses and moves no money.
	before, err := repository.AgentFloatBalance(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	svcBlocked := NewAgentRemittanceService(cfg, repository, fixedScreener{decision: kyc.ScreenBlocked, matched: []string{"Ada S."}})
	if _, err := svcBlocked.Redeem(ctx, RedeemRequest{
		AgentMerchantID: agent.ID, Code: code2, OTP: otp2,
		ReceiverIDType: "nin", ReceiverIDNumber: "12345678902",
	}); !errors.Is(err, store.ErrRecipientBlocked) {
		t.Fatalf("blocked screen: want ErrRecipientBlocked, got %v", err)
	}
	after, err := repository.AgentFloatBalance(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("blocked payout moved money: float %d -> %d", before, after)
	}
}
