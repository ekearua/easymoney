package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRemittanceLifecycle pins the remittance invariants at the store layer:
//
//   - opening a remittance ring-fences the amount on the per-remittance payable,
//     books the fee to revenue and the commission to the agent's float;
//   - the post-success hook replay is idempotent on the sender payment;
//   - an OTP reissue retires the previous active OTP (one active at a time) and
//     a code reissue retires the previous pending code;
//   - an expired remittance is refunded to the sender (amount + commission), the
//     fee is retained, and the sweep is safe to re-run.
func TestRemittanceLifecycle(t *testing.T) {
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

	sender, err := repository.GetOrCreateUser(ctx, "+2348000000001")
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
	if err := repository.SetAgentLimits(ctx, agent.ID, 50000, 50000, 200000, 50); err != nil {
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

	// Auto-assignment picks the region's active agent, and a region with no
	// agent resolves to ErrAgentNotFound.
	if got, err := repository.AssignAgentInRegion(ctx, region.ID); err != nil || got != agent.ID {
		t.Fatalf("AssignAgentInRegion = %s err=%v, want %s", got, err, agent.ID)
	}
	if _, err := repository.AssignAgentInRegion(ctx, uuid.New()); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("empty region assign: want ErrAgentNotFound, got %v", err)
	}

	const (
		amount     = int64(100000)
		fee        = int64(2000)
		commission = int64(500)
	)
	paymentID := uuid.New()
	salt, _ := NewRemittanceSalt()
	code, _ := NewRemittanceCode()

	params := CreateRemittanceParams{
		SenderUserID:    sender.ID,
		AgentMerchantID: agent.ID,
		RegionID:        region.ID,
		AmountKobo:      amount,
		FeeKobo:         fee,
		CommissionKobo:  commission,
		RecipientName:   "Ada Obi",
		RecipientPhone:  "+2348000000002",
		PaymentID:       paymentID,
		CodeHash:        RemittanceCodeHash(salt, code),
		CodeSalt:        salt,
		ExpiresAt:       time.Now().Add(72 * time.Hour),
	}
	r, created, err := repository.CreateRemittanceTx(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first CreateRemittanceTx reported created=false")
	}
	if r.Status != RemittanceAwaitingPickup || r.ID == uuid.Nil {
		t.Fatalf("created remittance = %+v", r)
	}

	payable := RemittancePayableAccountCode(r.ID)
	if got := balance(payable); got != amount {
		t.Fatalf("payable = %d, want %d", got, amount)
	}
	if got := balance(LedgerAccountSalesRevenue); got != fee {
		t.Fatalf("revenue = %d, want %d", got, fee)
	}
	if got := balance(AgentFloatAccountCode(agent.ID)); got != commission {
		t.Fatalf("agent float = %d, want %d", got, commission)
	}

	// Replaying the post-success hook must not double-post.
	replay, createdAgain, err := repository.CreateRemittanceTx(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if createdAgain {
		t.Fatal("replay reported created=true")
	}
	if replay.ID != r.ID {
		t.Fatalf("replay created a new remittance %s, want %s", replay.ID, r.ID)
	}
	if got := balance(payable); got != amount {
		t.Fatalf("replay changed payable: %d, want %d", got, amount)
	}

	if got, err := repository.RemittanceByPaymentID(ctx, paymentID); err != nil || got.ID != r.ID {
		t.Fatalf("RemittanceByPaymentID = %+v err=%v", got, err)
	}
	if _, err := repository.RemittanceByID(ctx, uuid.New()); !errors.Is(err, ErrRemittanceNotFound) {
		t.Fatalf("unknown remittance: want ErrRemittanceNotFound, got %v", err)
	}

	// One active OTP at a time: the second request supersedes the first.
	otpSalt1, _ := NewRemittanceSalt()
	if err := repository.RequestRemittanceOTP(ctx, r.ID,
		RemittanceCodeHash(otpSalt1, "111111"), otpSalt1, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	otpSalt2, _ := NewRemittanceSalt()
	if err := repository.RequestRemittanceOTP(ctx, r.ID,
		RemittanceCodeHash(otpSalt2, "222222"), otpSalt2, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	activeOTPs := countRows(t, ctx, repository, `SELECT count(*) FROM remittance_otps WHERE remittance_id=$1 AND status='active'`, r.ID)
	if activeOTPs != 1 {
		t.Fatalf("active OTPs = %d, want 1", activeOTPs)
	}

	// A code reissue leaves exactly one pending code.
	newSalt, _ := NewRemittanceSalt()
	newCode, _ := NewRemittanceCode()
	if err := repository.ReissueRemittancePickupCode(ctx, r.ID, RemittanceCodeHash(newSalt, newCode), newSalt); err != nil {
		t.Fatal(err)
	}
	pendingCodes := countRows(t, ctx, repository, `SELECT count(*) FROM remittance_pickups WHERE remittance_id=$1 AND status='pending'`, r.ID)
	if pendingCodes != 1 {
		t.Fatalf("pending codes = %d, want 1", pendingCodes)
	}

	// Expire the window and sweep.
	if _, err := repository.pool.Exec(ctx,
		`UPDATE remittances SET expires_at = now() - interval '1 hour' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	n, err := repository.ExpireRemittances(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d remittances, want 1", n)
	}
	expired, err := repository.RemittanceByID(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != RemittanceExpired {
		t.Fatalf("status = %q, want expired", expired.Status)
	}
	if got := balance(payable); got != 0 {
		t.Fatalf("payable after refund = %d, want 0", got)
	}
	if got := balance(AgentFloatAccountCode(agent.ID)); got != 0 {
		t.Fatalf("agent float after commission reversal = %d, want 0", got)
	}
	if got := balance(walletAccountCode(WalletOwnerUser, sender.ID)); got != amount+commission {
		t.Fatalf("sender wallet refund = %d, want %d", got, amount+commission)
	}
	if got := balance(LedgerAccountSalesRevenue); got != fee {
		t.Fatalf("fee should be retained: revenue = %d, want %d", got, fee)
	}

	// The sweep is idempotent: nothing left awaiting pickup.
	if n, err := repository.ExpireRemittances(ctx, 10); err != nil || n != 0 {
		t.Fatalf("second sweep n=%d err=%v, want 0 nil", n, err)
	}

	// An expired remittance can no longer mint an OTP.
	if err := repository.RequestRemittanceOTP(ctx, r.ID, "h", "s", time.Now().Add(time.Minute)); !errors.Is(err, ErrRemittanceNotClaimable) {
		t.Fatalf("OTP on expired remittance: want ErrRemittanceNotClaimable, got %v", err)
	}
}

func countRows(t *testing.T, ctx context.Context, repository *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := repository.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
