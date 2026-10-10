package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// TestUserWithdrawalScreening covers the G2 gate on the wallet→bank rail:
// clear passes, ambiguous and blocked verdicts refuse the withdrawal while
// the allowance reservation stays counted, and the screening row lands in
// the review queue keyed by the reservation ref.
func TestUserWithdrawalScreening(t *testing.T) {
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
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	// A unique phone per run: the KYC profile (and its tier) is keyed by the
	// user, so a re-used identity would start mid-ladder and refuse the
	// L0 -> L1 advancement below.
	user, err := repository.GetOrCreateUser(ctx, fmt.Sprintf("+2348012%06d", time.Now().UnixNano()%1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(ctx, `DELETE FROM payment_screenings WHERE subject_id=$1`, user.ID)
		_, _ = repository.pool.Exec(ctx, `DELETE FROM allowance_usage WHERE subject_id=$1`, user.ID)
		_, _ = repository.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	})
	// The refusal path keeps the reservation, so the wallet must actually
	// hold the balance the movement would need. L1's ₦50k single ceiling
	// comfortably covers the ₦20k withdrawal below.
	if _, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureUserWallet(ctx, user.ID, "Screened Tester"); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, LedgerAccountCustomerFloat, 100_000_000, "seed-screen-1", "screen test funding"); err != nil {
		t.Fatal(err)
	}

	decisions := map[string]func(context.Context) (string, string, string, string, string, []string, error){}

	// 1) Clear: recorded, decision returned, withdrawal may proceed.
	decisions[kyc.ScreenClear] = func(context.Context) (string, string, string, string, string, []string, error) {
		return kyc.ScreenClear, "simulated", "sim-w-clear", "sim-1", "no match", nil, nil
	}
	refClear := "wallet-withdraw:" + uuid.New().String()
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirOut,
		Tier: kyc.TierL1, AmountKobo: 2_000_000, Ref: refClear,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := repository.ScreenUserWithdrawal(ctx, user.ID, refClear, 2_000_000, "Screened Tester", decisions[kyc.ScreenClear])
	if err != nil || decision != kyc.ScreenClear {
		t.Fatalf("clear screen = %q err=%v, want clear", decision, err)
	}

	// 2) Ambiguous: refused (decision != clear) and the reservation stays.
	refHold := "wallet-withdraw:" + uuid.New().String()
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirOut,
		Tier: kyc.TierL1, AmountKobo: 2_000_000, Ref: refHold,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = repository.ScreenUserWithdrawal(ctx, user.ID, refHold, 2_000_000, "Similar Sanctioned",
		func(context.Context) (string, string, string, string, string, []string, error) {
			return kyc.ScreenStrong, "simulated", "sim-w-hold", "sim-1", "fuzzy hit", []string{"Similar Sanctioned"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if decision == kyc.ScreenClear {
		t.Fatal("ambiguous verdict must not read as clear")
	}
	var reservations int
	if err := repository.pool.QueryRow(ctx,
		`SELECT count(*) FROM allowance_usage WHERE transaction_ref=$1`, refHold).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 {
		t.Fatal("a refused (held) withdrawal must keep its allowance reservation")
	}

	// 3) Blocked: same enforcement — reservation stays, row is in the queue.
	refBlock := "wallet-withdraw:" + uuid.New().String()
	if err := repository.ReserveAllowance(ctx, AllowanceReservation{
		AccountType: AccountIndividual, SubjectID: user.ID, Direction: kyc.DirOut,
		Tier: kyc.TierL1, AmountKobo: 2_000_000, Ref: refBlock,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = repository.ScreenUserWithdrawal(ctx, user.ID, refBlock, 2_000_000, "Watchlisted Corp",
		func(context.Context) (string, string, string, string, string, []string, error) {
			return kyc.ScreenBlocked, "simulated", "sim-w-block", "sim-1", "list hit", []string{"Watchlisted Corp"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if decision != kyc.ScreenBlocked {
		t.Fatalf("blocked verdict = %q, want blocked", decision)
	}

	// All three rows are visible to the review queue's history.
	rows, err := repository.ListPaymentScreenings(ctx, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scr := range rows {
		if scr.Rail == RailWalletWithdrawal {
			seen[scr.MovementRef] = true
		}
	}
	for _, ref := range []string{refClear, refHold, refBlock} {
		if !seen[ref] {
			t.Fatalf("screening row for %s missing from the queue", ref)
		}
	}

	// A re-attempt of the same reservation ref reuses the recorded decision.
	replay, err := repository.ScreenUserWithdrawal(ctx, user.ID, refHold, 2_000_000, "Similar Sanctioned", decisions[kyc.ScreenClear])
	if err != nil || replay != kyc.ScreenStrong {
		t.Fatalf("replay = %q err=%v, want the recorded strong verdict", replay, err)
	}
}
