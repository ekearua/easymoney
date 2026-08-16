package store

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

// TestSettlementLifecycle covers the S1 settlement plane end to end: cut
// (dr 3100 / cr 3200), payout dispatch and completion (dr 3200 / cr 1100),
// idempotent replays, outbox facts, merchant balance unwinding, the failure /
// retry / reverse path, and the reconciliation payout leg.
func TestSettlementLifecycle(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE merchant_settlement_accounts,settlement_batches,settlement_lines,payouts,
		         business_event_outbox,merchant_webhook_deliveries,payments,users,merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012348801")
	if err != nil {
		t.Fatal(err)
	}

	createSucceeded := func(reference string, amount int64) uuid.UUID {
		t.Helper()
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: amount,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: reference, ReceiptToken: "tok-" + reference,
		})
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
			t.Fatalf("transition awaiting: changed=%v err=%v", changed, err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
			t.Fatalf("transition initialized: changed=%v err=%v", changed, err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusSucceeded, "test", nil); err != nil || !changed {
			t.Fatalf("succeed payment: changed=%v err=%v", changed, err)
		}
		return payment.ID
	}

	p1 := createSucceeded("ref-settle-1", 100_000)
	p2 := createSucceeded("ref-settle-2", 100_000)

	account, err := repository.CreateSettlementAccount(ctx, merchant.ID, "044", "0123456789", "Lagos Lunchbox Ltd")
	if err != nil {
		t.Fatal(err)
	}
	if !account.IsDefault || account.Status != SettlementAccountActive {
		t.Fatalf("first account should default active: %+v", account)
	}
	defaultAccount, err := repository.DefaultSettlementAccount(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if defaultAccount.ID != account.ID {
		t.Fatal("default account mismatch")
	}

	// Before settlement the merchant is owed the full collection.
	if got := merchantNet(ctx, t, repository, merchant.ID, LedgerAccountMerchantPayable); got != -200_000 {
		t.Fatalf("merchant payable before cut = %d, want -200000", got)
	}

	// Cut freezes 200,000 into a batch and moves the liability to 3200.
	batch, err := repository.CutSettlement(ctx, merchant.ID, "BATCH-001", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != SettlementBatchOpen || batch.TotalKobo != 200_000 || batch.LineCount != 2 {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	if batch.LedgerJournal != "STL:BATCH-001" {
		t.Fatalf("unexpected journal ref %q", batch.LedgerJournal)
	}
	if got := merchantNet(ctx, t, repository, merchant.ID, LedgerAccountMerchantPayable); got != 0 {
		t.Fatalf("merchant payable after cut = %d, want 0", got)
	}
	if got := merchantNet(ctx, t, repository, merchant.ID, LedgerAccountSettlementPayable); got != -200_000 {
		t.Fatalf("settlement payable after cut = %d, want -200000", got)
	}

	// Replaying the batch is a no-op.
	again, err := repository.CutSettlement(ctx, merchant.ID, "BATCH-001", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != batch.ID {
		t.Fatal("idempotent cut returned a different batch")
	}
	lines, err := repository.SettlementLinesByBatch(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 settlement lines, got %d", len(lines))
	}

	// Payout: queued -> processing (batch scheduled) -> completed.
	payout, err := repository.CreatePayout(ctx, batch.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if payout.Status != PayoutQueued || payout.AmountKobo != 200_000 {
		t.Fatalf("unexpected payout: %+v", payout)
	}
	dispatch, err := repository.ClaimPayoutForDispatch(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Batch.Status != SettlementBatchScheduled {
		t.Fatalf("batch should be scheduled while payout is in flight: %+v", dispatch.Batch)
	}
	if dispatch.Destination.ID != account.ID {
		t.Fatalf("dispatch destination mismatch: %+v", dispatch.Destination)
	}
	if err := repository.CompletePayout(ctx, payout.ID, "SIM-PAY-BATCH-001-1"); err != nil {
		t.Fatal(err)
	}
	done, err := repository.PayoutByID(ctx, payout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != PayoutCompleted || done.ExternalRef != "SIM-PAY-BATCH-001-1" {
		t.Fatalf("unexpected completed payout: %+v", done)
	}
	if done.LedgerJournal != "PAY:BATCH-001" {
		t.Fatalf("unexpected payout journal ref %q", done.LedgerJournal)
	}
	finalBatch, err := repository.SettlementBatchByID(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalBatch.Status != SettlementBatchProcessed {
		t.Fatalf("batch should be processed after payout: %+v", finalBatch)
	}
	// Ledger: 3200 and 1100 both return to zero; merchant is fully settled.
	if got := merchantNet(ctx, t, repository, merchant.ID, LedgerAccountSettlementPayable); got != 0 {
		t.Fatalf("settlement payable after payout = %d, want 0", got)
	}
	platformNet := func(account string) int64 {
		t.Helper()
		balances, err := repository.LedgerBalanceSummary(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range balances {
			if b.Account == account {
				return b.NetKobo
			}
		}
		return 0
	}
	if got := platformNet(LedgerAccountOperatingBank); got != 0 {
		t.Fatalf("operating bank net after full cycle = %d, want 0", got)
	}
	if got := platformNet(LedgerAccountSettlementPayable); got != 0 {
		t.Fatalf("settlement payable platform net after full cycle = %d, want 0", got)
	}

	// Outbox facts committed with the cut and the payout.
	events, err := repository.ClaimBusinessEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	topics := map[string]int{}
	for _, e := range events {
		topics[e.Topic]++
	}
	for _, want := range []string{domain.TopicSettlementBatchCreated, domain.TopicSettlementBatchProcessed, domain.TopicPayoutSucceeded} {
		if topics[want] != 1 {
			t.Fatalf("expected exactly one %s event, got %d (all: %v)", want, topics[want], topics)
		}
	}
	for _, e := range events {
		if err := repository.CompleteBusinessEvent(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}

	// Reconciliation payout leg is clean.
	run, items, err := repository.RunReconciliation(ctx, "auto", "test")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "clean" {
		t.Fatalf("expected clean reconciliation, got %s with %v", run.Status, items)
	}

	// Failure / retry / reverse path.
	p3 := createSucceeded("ref-settle-3", 50_000)
	batch2, err := repository.CutSettlement(ctx, merchant.ID, "BATCH-002", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	badAccount, err := repository.CreateSettlementAccount(ctx, merchant.ID, "011", "0999999999", "Fail Co")
	if err != nil {
		t.Fatal(err)
	}
	failPayout, err := repository.CreatePayout(ctx, batch2.ID, badAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimPayoutForDispatch(ctx, batch2.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.FailPayout(ctx, failPayout.ID, "recipient bank unavailable"); err != nil {
		t.Fatal(err)
	}
	failed, err := repository.PayoutByID(ctx, failPayout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != PayoutFailed || failed.Attempts != 1 {
		t.Fatalf("unexpected failed payout: %+v", failed)
	}
	if err := repository.RetryPayout(ctx, failPayout.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := repository.PayoutByID(ctx, failPayout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != PayoutQueued {
		t.Fatalf("retry should re-queue the payout: %+v", retried)
	}
	if _, err := repository.ClaimPayoutForDispatch(ctx, batch2.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.FailPayout(ctx, failPayout.ID, "declined again"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReversePayout(ctx, failPayout.ID, "wrong destination account"); err != nil {
		t.Fatal(err)
	}
	reversed, err := repository.PayoutByID(ctx, failPayout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reversed.Status != PayoutReversed {
		t.Fatalf("payout should be reversed: %+v", reversed)
	}
	reopened, err := repository.SettlementBatchByID(ctx, batch2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != SettlementBatchOpen {
		t.Fatalf("batch should reopen after reversal: %+v", reopened)
	}
	// A fresh payout to the good account settles the reopened batch.
	fresh, err := repository.CreatePayout(ctx, batch2.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == failPayout.ID {
		t.Fatal("expected a fresh payout row after reversal")
	}
	if _, err := repository.ClaimPayoutForDispatch(ctx, batch2.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompletePayout(ctx, fresh.ID, "SIM-PAY-BATCH-002-1"); err != nil {
		t.Fatal(err)
	}
	settled, err := repository.PayoutByID(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != PayoutCompleted {
		t.Fatalf("expected completed payout after reversal+redispatch: %+v", settled)
	}
	if got := merchantNet(ctx, t, repository, merchant.ID, LedgerAccountSettlementPayable); got != 0 {
		t.Fatalf("settlement payable after all cycles = %d, want 0", got)
	}
	_ = p1
	_ = p2
	_ = p3

	// Stale payout requeue: a processing payout older than the cutoff returns
	// to queued so the dispatcher can re-attempt it.
	if _, err := repository.pool.Exec(ctx, `
		UPDATE payouts SET status='processing', updated_at=now()-interval '2 hours' WHERE id=$1`, settled.ID); err != nil {
		t.Fatal(err)
	}
	requeued, err := repository.RequeueStalePayouts(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if requeued != 1 {
		t.Fatalf("expected 1 stale payout requeued, got %d", requeued)
	}
}

func merchantNet(ctx context.Context, t *testing.T, repository *Store, merchantID uuid.UUID, account string) int64 {
	t.Helper()
	balances, err := repository.MerchantLedgerBalance(ctx, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range balances {
		if b.Account == account {
			return b.NetKobo
		}
	}
	return 0
}
