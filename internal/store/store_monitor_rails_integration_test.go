package store

// TestTransactionMonitorCoversAllRails proves the transaction monitor sees
// every money rail — card/transfer payments, wallet top-ups, standing-account
// bank credits, refunds, and merchant settlement payouts — tags each alert
// with the rails it fired over plus a stable dedupe reference, and never
// re-raises an alert when the same window is rescanned (the periodic monitor
// tick must not spam the compliance queue).
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestTransactionMonitorCoversAllRails -v

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
)

func TestTransactionMonitorCoversAllRails(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	payer, err := repository.GetOrCreateUser(ctx, "+2348095550101")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	// The system top-up merchant is intentionally inactive, so resolve it
	// directly rather than through the active-only picker.
	var topupMerchantID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `SELECT id FROM merchants WHERE slug='xego-wallet-topup'`).Scan(&topupMerchantID); err != nil {
		t.Fatal(err)
	}
	// settlePayment creates a payment and drives it to succeeded exactly like
	// the confirmation pipeline does, returning its id.
	settlePayment := func(merchantID uuid.UUID, amountKobo int64) uuid.UUID {
		t.Helper()
		token, err := domain.NewReceiptToken()
		if err != nil {
			t.Fatal(err)
		}
		pmt, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: payer.ID, MerchantID: merchantID, AmountKobo: amountKobo,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []domain.PaymentStatus{
			domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusSucceeded,
		} {
			if changed, err := repository.TransitionPayment(ctx, pmt.ID, status, "test", nil); err != nil || !changed {
				t.Fatalf("transition to %s: changed=%v err=%v", status, changed, err)
			}
		}
		return pmt.ID
	}

	// Top-up rail: a wallet funding payment against the system top-up merchant.
	settlePayment(topupMerchantID, 1_500_000)
	// Refund rail: one succeeded payment is refunded and its refund confirmed
	// by the provider (the pending row is what the rail monitors).
	refundedPayment := settlePayment(merchant.ID, 500_000)
	refund, err := repository.RefundPayment(ctx, refundedPayment, "monitor rail test", "test", nil)
	if err != nil {
		t.Fatalf("refund payment: %v", err)
	}
	if _, err := repository.pool.Exec(ctx, `
		UPDATE refunds SET status='succeeded', completed_at=now() WHERE id=$1`, refund.ID); err != nil {
		t.Fatal(err)
	}

	// Standing-credit rail: a bank transfer into the reusable 9029-* account.
	account, err := repository.EnsureStandingAccount(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordStandingCredit(ctx, payer.ID, account.AccountNumber, "STD-RAIL-CREDIT-1", 2_000_000); err != nil {
		t.Fatal(err)
	}
	if applied, err := repository.ApplyStandingCredit(ctx, payer.ID, "STD-RAIL-CREDIT-1", 2_000_000, "monitor rail test"); err != nil || !applied {
		t.Fatalf("apply standing credit: applied=%v err=%v", applied, err)
	}

	// Payout rail: a completed merchant settlement payout. Seed links the demo
	// merchant admin as owner of every seeded merchant, so the payout is
	// attributed to that user.
	var destinationID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO merchant_settlement_accounts(merchant_id, bank_code, account_number, account_name)
		VALUES($1,'058','0123456789','Lagos Lunchbox')
		RETURNING id`, merchant.ID).Scan(&destinationID); err != nil {
		t.Fatal(err)
	}
	var batchID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO settlement_batches(batch_no, merchant_id, status, total_kobo, line_count, cutoff_at)
		VALUES('MONITOR-RAIL-BATCH', $1, 'processed', 5000000, 1, now())
		RETURNING id`, merchant.ID).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO payouts(batch_id, merchant_id, destination_id, amount_kobo, status, completed_at)
		VALUES($1,$2,$3,5000000,'completed',now())`, batchID, merchant.ID, destinationID); err != nil {
		t.Fatal(err)
	}

	cfg := kyc.MonitorConfig{
		VelocityWindow: 24 * time.Hour, VelocityLimit: 10,
		StructuringWindow: 24 * time.Hour, StructuringCount: 2, StructuringFloor: 400_000,
		RoundAmountStep: 1_000_000, RoundAmountMin: 1_000_000,
	}
	raised, err := repository.RunTransactionMonitor(ctx, time.Now().Add(-24*time.Hour), cfg)
	if err != nil {
		t.Fatalf("run transaction monitor: %v", err)
	}
	if raised == 0 {
		t.Fatal("expected alerts from the seeded rails")
	}
	alerts, err := repository.ListTransactionAlerts(ctx, "open", 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, alert := range alerts {
		if ref, ok := alert.Details["reference"].(string); !ok || ref == "" {
			t.Fatalf("alert %s has no dedupe reference: %+v", alert.Rule, alert.Details)
		}
		rails, ok := alert.Details["rails"].([]any)
		if !ok || len(rails) == 0 {
			t.Fatalf("alert %s has no rail tags: %+v", alert.Rule, alert.Details)
		}
		for _, rail := range rails {
			seen[fmt.Sprint(rail)] = true
		}
	}
	for _, want := range []string{kyc.RailTopup, kyc.RailStandingCredit, kyc.RailRefund, kyc.RailPayout} {
		if !seen[want] {
			t.Fatalf("no alert carried the %s rail; alerts=%+v", want, alerts)
		}
	}

	// The compliance queue can triage by rail: filtering returns only alerts
	// tagged with that rail.
	payoutAlerts, err := repository.ListTransactionAlertsFiltered(ctx, "open", kyc.RailPayout, 50)
	if err != nil {
		t.Fatalf("list payout-rail alerts: %v", err)
	}
	if len(payoutAlerts) == 0 {
		t.Fatal("expected at least one payout-rail alert")
	}
	for _, alert := range payoutAlerts {
		rails, _ := alert.Details["rails"].([]any)
		found := false
		for _, rail := range rails {
			if rail == kyc.RailPayout {
				found = true
			}
		}
		if !found {
			t.Fatalf("payout filter returned an alert without the payout rail: %+v", alert.Details)
		}
	}
	refundAlerts, err := repository.ListTransactionAlertsFiltered(ctx, "", kyc.RailRefund, 50)
	if err != nil {
		t.Fatalf("list refund-rail alerts: %v", err)
	}
	if len(refundAlerts) == 0 {
		t.Fatal("expected at least one refund-rail alert")
	}

	// A rescan of the same window must not duplicate a single alert.
	again, err := repository.RunTransactionMonitor(ctx, time.Now().Add(-24*time.Hour), cfg)
	if err != nil {
		t.Fatalf("rescan transaction monitor: %v", err)
	}
	if again != 0 {
		t.Fatalf("rescan raised %d duplicate alerts, want 0", again)
	}
}
