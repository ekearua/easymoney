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
	if err := repository.EnsureAdminUser(ctx, "maker@test.local", "x"); err != nil {
		t.Fatal(err)
	}
	maker, err := repository.AdminUserByEmail(ctx, "maker@test.local")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureAdminUser(ctx, "checker@test.local", "x"); err != nil {
		t.Fatal(err)
	}
	checker, err := repository.AdminUserByEmail(ctx, "checker@test.local")
	if err != nil {
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

	t.Run("maker-checker refund succeeds", func(t *testing.T) {
		// Maker creates refund request.
		refund, err := repository.RequestRefund(ctx, p1, "customer complaint", "merchant", nil)
		if err != nil {
			t.Fatalf("request refund: %v", err)
		}
		if refund.ApprovalStatus != "pending_approval" {
			t.Fatalf("expected pending_approval, got %s", refund.ApprovalStatus)
		}
		if refund.AmountKobo != 100_000 {
			t.Fatalf("expected 100000, got %d", refund.AmountKobo)
		}

		// Payment is still succeeded (not yet refunded).
		p, err := repository.PaymentByID(ctx, p1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != domain.StatusSucceeded {
			t.Fatalf("expected succeeded before approval, got %s", p.Status)
		}

		// Maker cannot approve their own refund (when admin_id is set).
		// First, create one via admin path to test self-approval guard.
		pSelf := createSucceeded("refund-ref-self", 25_000)
		adminRefund, err := repository.RequestRefund(ctx, pSelf, "self-approval test", "admin", &maker.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.ApproveRefund(ctx, adminRefund.ID, maker.ID, "admin.approve")
		if err == nil {
			t.Fatal("expected error for self-approval")
		}

		// Checker approves the original merchant-initiated refund.
		approved, err := repository.ApproveRefund(ctx, refund.ID, checker.ID, "admin.approve")
		if err != nil {
			t.Fatalf("approve refund: %v", err)
		}
		if approved.ApprovalStatus != "approved" {
			t.Fatalf("expected approved, got %s", approved.ApprovalStatus)
		}
		if approved.Status != RefundPending {
			t.Fatalf("expected pending (before provider), got %s", approved.Status)
		}

		// Payment is now refunded.
		p, err = repository.PaymentByID(ctx, p1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != domain.StatusRefunded {
			t.Fatalf("expected refunded, got %s", p.Status)
		}

		// Ledger is reversed.
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
		if err := repository.CompleteRefund(ctx, approved.ID, "SIM-REF-abc"); err != nil {
			t.Fatal(err)
		}
		completed, err := repository.RefundByID(ctx, approved.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status != RefundSucceeded || completed.ProviderRefundID != "SIM-REF-abc" {
			t.Fatalf("expected succeeded, got %+v", completed)
		}
	})

	t.Run("duplicate refund rejected", func(t *testing.T) {
		_, err := repository.RequestRefund(ctx, p1, "double refund", "merchant", nil)
		if err == nil {
			t.Fatal("expected error for duplicate refund")
		}
	})

	t.Run("already-refunded payment rejected", func(t *testing.T) {
		_, err := repository.RequestRefund(ctx, p1, "refund again", "merchant", nil)
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
		_, err = repository.RequestRefund(ctx, payment.ID, "draft refund", "merchant", nil)
		if err == nil {
			t.Fatal("expected error for non-succeeded payment")
		}
	})

	t.Run("refund in processed batch rejected", func(t *testing.T) {
		batch, err := repository.CutSettlement(ctx, merchant.ID, "REFUND-BATCH-1", time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.pool.Exec(ctx, `UPDATE settlement_batches SET status='processed' WHERE id=$1`, batch.ID); err != nil {
			t.Fatal(err)
		}
		_, err = repository.RequestRefund(ctx, p2, "processed batch refund", "merchant", nil)
		if err == nil {
			t.Fatal("expected error for payment in processed batch")
		}
	})

	t.Run("refund in open batch removes line", func(t *testing.T) {
		p3 := createSucceeded("refund-ref-3", 75_000)
		batch, err := repository.CutSettlement(ctx, merchant.ID, "REFUND-BATCH-2", time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if batch.TotalKobo != 75_000 {
			t.Fatalf("expected 75000, got %d", batch.TotalKobo)
		}
		refund, err := repository.RequestRefund(ctx, p3, "open batch refund", "merchant", nil)
		if err != nil {
			t.Fatalf("request refund in open batch: %v", err)
		}
		// Approve to execute.
		_, err = repository.ApproveRefund(ctx, refund.ID, checker.ID, "admin.approve")
		if err != nil {
			t.Fatalf("approve refund: %v", err)
		}
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
