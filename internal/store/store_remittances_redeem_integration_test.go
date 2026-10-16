package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// TestRemittanceRedemption pins the Phase-3 choke point (POS_AGENT_NETWORK_PLAN.md
// §7): RedeemPickupTx is the one function that can authorise a cash payout, and
// it enforces every rule inside the money-moving transaction.
func TestRemittanceRedemption(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE remittance_payouts, remittance_otps, remittance_pickups, remittances,
		         agent_users, agent_profiles, agent_regions, payments, ledger_entries,
		         allowance_usage, wallet_accounts, business_kyb_profiles, kyc_profiles,
		         users, merchants
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

	sender, err := repository.GetOrCreateUser(ctx, "+2348000000010")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantType(ctx, agent.ID, MerchantTypeAgent); err != nil {
		t.Fatal(err)
	}
	region, err := repository.EnsureAgentRegion(ctx, AgentRegion{
		Code: "oyo-ibadan", Name: "Ibadan", State: "Oyo", LGA: "Ibadan North",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, AgentTierStandard); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentLimits(ctx, agent.ID, 500000, 100000, 200000, 50); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	balance := func(account string) int64 {
		b, err := walletBalanceQ(ctx, repository.pool, account)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	const amount = int64(100000)

	open := func() (Remittance, string, string) {
		code, _ := NewRemittanceCode()
		salt, _ := NewRemittanceSalt()
		r, _, err := repository.CreateRemittanceTx(ctx, CreateRemittanceParams{
			SenderUserID:    sender.ID,
			AgentMerchantID: agent.ID,
			RegionID:        region.ID,
			AmountKobo:      amount,
			FeeKobo:         2000,
			CommissionKobo:  500,
			RecipientName:   "Ada Obi",
			RecipientPhone:  "+2348000000011",
			PaymentID:       uuid.New(),
			CodeHash:        RemittanceCodeHash(salt, code),
			CodeSalt:        salt,
			ExpiresAt:       time.Now().Add(72 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		otp, _ := NewRemittanceCode()
		otpSalt, _ := NewRemittanceSalt()
		if err := repository.RequestRemittanceOTP(ctx, r.ID,
			RemittanceCodeHash(otpSalt, otp), otpSalt, time.Now().Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		return r, code, otp
	}
	redeem := func(code, otp, id string) RedeemPickupParams {
		return RedeemPickupParams{
			AgentMerchantID:      agent.ID,
			Code:                 code,
			OTP:                  otp,
			RequireOTP:           true,
			RequireID:            true,
			ReceiverIDType:       "nin",
			ReceiverIDNumber:     id,
			IDHashPepper:         "test-pepper",
			ScreenDecision:       kyc.ScreenClear,
			CustomerDailyCapKobo: 0,
			CustomerWeeklyCapKobo: 0,
		}
	}
	wrong := func(code string) string {
		if code == "000000" {
			return "000001"
		}
		return "000000"
	}

	// A code that matches nothing is refused before any money moves.
	r1, code1, otp1 := open()
	if _, err := repository.RedeemPickupTx(ctx, redeem(wrong(code1), otp1, "12345678901")); !errors.Is(err, ErrRemittanceCodeInvalid) {
		t.Fatalf("wrong code: want ErrRemittanceCodeInvalid, got %v", err)
	}

	// A wrong OTP is refused, and the OTP attempt counter persists.
	if _, err := repository.RedeemPickupTx(ctx, redeem(code1, wrong(otp1), "12345678901")); !errors.Is(err, ErrRemittanceOTPInvalid) {
		t.Fatalf("wrong OTP: want ErrRemittanceOTPInvalid, got %v", err)
	}
	if got := countRows(t, ctx, repository,
		`SELECT attempts FROM remittance_otps WHERE remittance_id=$1 AND status='active'`, r1.ID); got != 1 {
		t.Fatalf("OTP attempts = %d, want 1", got)
	}

	// A blocked recipient screen stops the payout outright.
	_, codeBlocked, otpBlocked := open()
	blocked := redeem(codeBlocked, otpBlocked, "12345678902")
	blocked.ScreenDecision = kyc.ScreenBlocked
	if _, err := repository.RedeemPickupTx(ctx, blocked); !errors.Is(err, ErrRecipientBlocked) {
		t.Fatalf("blocked screen: want ErrRecipientBlocked, got %v", err)
	}

	// A government ID is mandatory when policy requires it.
	_, codeNoID, otpNoID := open()
	noID := redeem(codeNoID, otpNoID, "")
	if _, err := repository.RedeemPickupTx(ctx, noID); !errors.Is(err, ErrReceiverIDRequired) {
		t.Fatalf("missing ID: want ErrReceiverIDRequired, got %v", err)
	}

	// The recipient's per-customer daily ceiling is enforced via receiver_id_hash.
	_, codeCap, otpCap := open()
	capped := redeem(codeCap, otpCap, "12345678903")
	capped.CustomerDailyCapKobo = 50000
	if _, err := repository.RedeemPickupTx(ctx, capped); !errors.Is(err, ErrCustomerCapExceeded) {
		t.Fatalf("customer cap: want ErrCustomerCapExceeded, got %v", err)
	}

	// The happy path: correct code + OTP + ID settles the payout atomically.
	before := balance(AgentFloatAccountCode(agent.ID))
	res, err := repository.RedeemPickupTx(ctx, redeem(code1, otp1, "12345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Remittance.Status != RemittancePaid {
		t.Fatalf("remittance status = %q, want paid", res.Remittance.Status)
	}
	if res.Payout.ID == uuid.Nil || res.Payout.ReceiverName != "Ada Obi" {
		t.Fatalf("payout = %+v", res.Payout)
	}
	if got := balance(RemittancePayableAccountCode(r1.ID)); got != 0 {
		t.Fatalf("payable after redeem = %d, want 0", got)
	}
	if got := balance(AgentFloatAccountCode(agent.ID)); got != before+amount {
		t.Fatalf("agent float after redeem = %d, want %d", got, before+amount)
	}
	// The captured ID is sealed/hashed, never stored as a bare queryable ID.
	var idHash string
	if err := repository.pool.QueryRow(ctx,
		`SELECT receiver_id_hash FROM remittance_payouts WHERE remittance_id=$1`, r1.ID).Scan(&idHash); err != nil {
		t.Fatal(err)
	}
	if idHash == "" || idHash == "12345678901" {
		t.Fatalf("receiver_id_hash = %q, want a non-plaintext digest", idHash)
	}

	// The pickup code is single-use: a second redeem finds nothing pending.
	if _, err := repository.RedeemPickupTx(ctx, redeem(code1, otp1, "12345678901")); !errors.Is(err, ErrRemittanceCodeInvalid) {
		t.Fatalf("replay: want ErrRemittanceCodeInvalid, got %v", err)
	}

	// The agent's per-payout cap is enforced.
	if err := repository.SetAgentLimits(ctx, agent.ID, 500000, 50000, 200000, 50); err != nil {
		t.Fatal(err)
	}
	_, codePer, otpPer := open()
	if _, err := repository.RedeemPickupTx(ctx, redeem(codePer, otpPer, "12345678904")); !errors.Is(err, ErrAgentPayoutLimitExceeded) {
		t.Fatalf("agent per-payout cap: want ErrAgentPayoutLimitExceeded, got %v", err)
	}
}
