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

func TestRefundLifecycle(t *testing.T) {
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
		         refunds,disputes,
		         business_event_outbox,merchant_webhook_deliveries,payment_events,payments,users,merchants
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
		for _, status := range []domain.PaymentStatus{domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusSucceeded} {
			if changed, err := repository.TransitionPayment(ctx, payment.ID, status, "test", nil); err != nil || !changed {
				t.Fatalf("transition %s: changed=%v err=%v", status, changed, err)
			}
		}
		return payment.ID
	}

	p1 := createSucceeded("refund-ref-1", 100_000)
	p2 := createSucceeded("refund-ref-2", 50_000)

	t.Run("refund succeeds", func(t *testing.T) {
		refund, err := repository.RefundPayment(ctx, p1, "customer complaint", "admin", nil)
		if err != nil {
			t.Fatalf("refund: %v", err)
		}
		if refund.Status != RefundPending {
			t.Fatalf("expected pending, got %s", refund.Status)
		}
		if refund.AmountKobo != 100_000 {
			t.Fatalf("expected 100000, got %d", refund.AmountKobo)
		}

		// Payment is now refunded.
		p, err := repository.PaymentByID(ctx, p1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != domain.StatusRefunded {
			t.Fatalf("expected refunded, got %s", p.Status)
		}

		// Ledger is reversed: both entries for this payment are now reversed.
		rows, err := repository.pool.Query(ctx, `
			SELECT id, COALESCE(reversal_of,0) FROM ledger_entries WHERE journal_ref=$1 ORDER BY id`, p1.String())
		if err != nil {
			t.Fatal(err)
		}
		var foundReversal bool
		for rows.Next() {
			var id int64
			var reversalOf int64
			if err := rows.Scan(&id, &reversalOf); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if reversalOf != 0 {
				foundReversal = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !foundReversal {
			t.Fatal("expected at least one reversed ledger entry")
		}

		// Complete the refund.
		if err := repository.CompleteRefund(ctx, refund.ID, "SIM-REF-abc"); err != nil {
			t.Fatal(err)
		}
		completed, err := repository.RefundByID(ctx, refund.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status != RefundSucceeded || completed.ProviderRefundID != "SIM-REF-abc" {
			t.Fatalf("expected succeeded, got %+v", completed)
		}
	})

	t.Run("duplicate refund rejected", func(t *testing.T) {
		_, err := repository.RefundPayment(ctx, p1, "double refund", "admin", nil)
		if err == nil {
			t.Fatal("expected error for duplicate refund")
		}
	})

	t.Run("already-refunded payment rejected", func(t *testing.T) {
		_, err := repository.RefundPayment(ctx, p1, "refund again", "admin", nil)
		if err == nil {
			t.Fatal("expected error for already-refunded payment")
		}
	})

	t.Run("non-succeeded payment rejected", func(t *testing.T) {
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 10_000,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: "refund-draft-ref", ReceiptToken: "tok-refund-draft",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.RefundPayment(ctx, payment.ID, "draft refund", "admin", nil)
		if err == nil {
			t.Fatal("expected error for non-succeeded payment")
		}
	})

	t.Run("refund in processed batch rejected", func(t *testing.T) {
		// p2 is still succeeded. Cut it into a batch.
		batch, err := repository.CutSettlement(ctx, merchant.ID, "REFUND-BATCH-1", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		// Mark batch as processed (simulates payout completion).
		if _, err := repository.pool.Exec(ctx, `UPDATE settlement_batches SET status='processed' WHERE id=$1`, batch.ID); err != nil {
			t.Fatal(err)
		}
		_, err = repository.RefundPayment(ctx, p2, "processed batch refund", "admin", nil)
		if err == nil {
			t.Fatal("expected error for payment in processed batch")
		}
	})

	t.Run("refund in open batch removes line", func(t *testing.T) {
		// Create a fresh succeeded payment.
		p3 := createSucceeded("refund-ref-3", 75_000)
		// Cut into a batch.
		batch, err := repository.CutSettlement(ctx, merchant.ID, "REFUND-BATCH-2", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if batch.TotalKobo != 75_000 {
			t.Fatalf("expected 75000, got %d", batch.TotalKobo)
		}
		// Refund the payment — should remove the line.
		_, err = repository.RefundPayment(ctx, p3, "open batch refund", "admin", nil)
		if err != nil {
			t.Fatalf("refund in open batch: %v", err)
		}
		// Batch should now have 0 lines.
		updated, err := repository.SettlementBatchByID(ctx, batch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.TotalKobo != 0 || updated.LineCount != 0 {
			t.Fatalf("expected empty batch, got total=%d lines=%d", updated.TotalKobo, updated.LineCount)
		}
	})

	t.Run("disputes", func(t *testing.T) {
		d, err := repository.CreateDispute(ctx, p1, "chargeback from bank")
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != DisputeOpen {
			t.Fatalf("expected open, got %s", d.Status)
		}
		if err := repository.ResolveDispute(ctx, d.ID, DisputeWon); err != nil {
			t.Fatal(err)
		}
		resolved, err := repository.DisputeByID(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Status != DisputeWon || resolved.ResolvedAt == nil {
			t.Fatalf("expected won with resolved_at, got %+v", resolved)
		}
	})

	t.Run("list refunds and disputes", func(t *testing.T) {
		refunds, err := repository.ListRefunds(ctx, merchant.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(refunds) < 2 {
			t.Fatalf("expected at least 2 refunds, got %d", len(refunds))
		}
		disputes, err := repository.ListDisputes(ctx, merchant.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(disputes) < 1 {
			t.Fatalf("expected at least 1 dispute, got %d", len(disputes))
		}
	})
}
