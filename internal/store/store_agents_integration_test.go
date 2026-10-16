package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestAgentProfileAndFloatLifecycle pins the agent-network invariants at the
// store layer, which is where they actually have to hold:
//
//   - a merchant can be classified an agent, and the classification is what the
//     profile builds on;
//   - a freshly created agent is pending with zero (uncapped) limits, so it can
//     never pay out until an operator sets limits and activates it;
//   - an idempotent ensure cannot clobber limits an operator already set;
//   - the float balance is summed from the journal and a replayed funding
//     reference cannot double it;
//   - an agent cannot be settled beyond what it is owed, and settlement is
//     idempotent.
func TestAgentProfileAndFloatLifecycle(t *testing.T) {
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
		TRUNCATE remittance_payouts, remittance_pickups, remittances, agent_users,
		         agent_profiles, agent_regions, payments, ledger_entries, allowance_usage,
		         wallet_accounts, business_kyb_profiles, kyc_profiles, users, merchants
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

	if err := repository.SetMerchantType(ctx, agent.ID, MerchantTypeAgent); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.MerchantType(ctx, agent.ID); err != nil || got != MerchantTypeAgent {
		t.Fatalf("merchant type = %q err=%v, want %q", got, err, MerchantTypeAgent)
	}

	region, err := repository.EnsureAgentRegion(ctx, AgentRegion{
		Code: "lag-ikeja", Name: "Ikeja", State: "Lagos", LGA: "Ikeja",
	})
	if err != nil {
		t.Fatal(err)
	}
	if region.Status != AgentRegionActive {
		t.Fatalf("new region status = %q, want active", region.Status)
	}

	profile, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, AgentTierStandard)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Status != AgentStatusPending {
		t.Fatalf("new agent status = %q, want pending", profile.Status)
	}
	if profile.MaxOutstandingFloatKobo != 0 || profile.PerPayoutLimitKobo != 0 || profile.DailyPayoutLimitKobo != 0 {
		t.Fatalf("new agent should ship with zero limits, got %+v", profile)
	}

	// A pending agent refuses every payout regardless of the (uncapped) numbers.
	if err := agentPayoutAllowed(profile, 5000, 0, 0); !errors.Is(err, ErrAgentNotActive) {
		t.Fatalf("pending agent payout: want ErrAgentNotActive, got %v", err)
	}

	// An operator sets limits and activates the agent.
	if err := repository.SetAgentLimits(ctx, agent.ID, 50000, 50000, 200000, 50); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	// Re-ensuring must not reset the operator's limits.
	reEnsured, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, AgentTierStandard)
	if err != nil {
		t.Fatal(err)
	}
	if reEnsured.MaxOutstandingFloatKobo != 50000 || reEnsured.CommissionBps != 50 || reEnsured.Status != AgentStatusActive {
		t.Fatalf("ensure clobbered operator settings: %+v", reEnsured)
	}

	// Funding is summed from the journal and is replay-safe.
	if err := repository.FundAgentFloat(ctx, agent.ID, LedgerAccountOperatingBank, 30000, "agent-fund-1", "Agent float funding"); err != nil {
		t.Fatal(err)
	}
	balance, err := repository.AgentFloatBalance(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 30000 {
		t.Fatalf("float balance = %d, want 30000", balance)
	}
	if err := repository.FundAgentFloat(ctx, agent.ID, LedgerAccountOperatingBank, 30000, "agent-fund-1", "Agent float funding"); err != nil {
		t.Fatal(err)
	}
	if balance, _ = repository.AgentFloatBalance(ctx, agent.ID); balance != 30000 {
		t.Fatalf("replayed funding doubled the float: %d, want 30000", balance)
	}

	// Outstanding cap is enforced against the real balance.
	current, err := repository.AgentProfileByMerchant(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentPayoutAllowed(current, 25000, balance, 0); !errors.Is(err, ErrAgentFloatCapExceeded) {
		t.Fatalf("outstanding cap: want ErrAgentFloatCapExceeded, got %v", err)
	}

	// Cannot settle more than is owed.
	if err := repository.SettleAgentFloat(ctx, agent.ID, 40000, "agent-settle-x", "Agent settlement"); !errors.Is(err, ErrInsufficientAgentFloat) {
		t.Fatalf("over-settlement: want ErrInsufficientAgentFloat, got %v", err)
	}

	// A partial settlement reduces the float, and a replay is a no-op.
	if err := repository.SettleAgentFloat(ctx, agent.ID, 10000, "agent-settle-1", "Agent settlement"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SettleAgentFloat(ctx, agent.ID, 10000, "agent-settle-1", "Agent settlement"); err != nil {
		t.Fatal(err)
	}
	if balance, _ = repository.AgentFloatBalance(ctx, agent.ID); balance != 20000 {
		t.Fatalf("float balance after settle = %d, want 20000", balance)
	}

	if _, err := repository.AgentProfileByMerchant(ctx, uuid.New()); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown agent: want ErrAgentNotFound, got %v", err)
	}
}
