package store

// TestFraudMonitorLaneSeparationAndDedupe proves the G13 store seam end to
// end: the sweep file lane='fraud' alerts for a burst of failed attempts,
// the AML lane does not see them, no AML risk_events row is written, and a
// second sweep over the same window raises nothing new.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestFraudMonitorLaneSeparationAndDedupe -v

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/fraud"
)

func TestFraudMonitorLaneSeparationAndDedupe(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants, step_up_grants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348097770301")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}

	// A burst of failed attempts on one payer inside the lookback window.
	for i := 0; i < 4; i++ {
		payment := domain.Payment{
			ID:                uuid.New(),
			UserID:            user.ID,
			MerchantID:        merchant.ID,
			AmountKobo:        1_000,
			Currency:          "NGN",
			Status:            domain.StatusFailed,
			Provider:          "simulated",
			ProviderReference: fmt.Sprintf("fail-%d", i),
			ReceiptToken:      fmt.Sprintf("fail-rt-%d", i),
		}
		if _, err := repository.CreatePayment(ctx, payment); err != nil {
			t.Fatal(err)
		}
	}

	cfg := fraud.FraudConfig{Window: time.Hour, FailedAttemptLimit: 3}
	raised, err := repository.RunFraudMonitor(ctx, time.Now().Add(-time.Hour), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if raised == 0 {
		t.Fatal("expected a failed_attempt_burst fraud alert")
	}

	alerts, err := repository.ListFraudAlerts(ctx, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var burst bool
	for _, a := range alerts {
		if a.UserID == user.ID && a.Rule == fraud.RuleFailedAttemptBurst {
			burst = true
		}
	}
	if !burst {
		t.Fatalf("fraud lane missing the burst alert for %s", user.ID)
	}

	// The AML lane must not surface the fraud alert.
	amlAlerts, err := repository.ListTransactionAlertsFiltered(ctx, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range amlAlerts {
		if a.Rule == fraud.RuleFailedAttemptBurst {
			t.Fatal("fraud alert leaked into the AML lane")
		}
	}

	// Fraud never moves the ML/FT score.
	var riskEvents int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM risk_events WHERE user_id=$1`, user.ID).Scan(&riskEvents); err != nil {
		t.Fatal(err)
	}
	if riskEvents != 0 {
		t.Fatalf("fraud sweep wrote %d AML risk_events, want none", riskEvents)
	}

	// Re-running the same window is idempotent.
	again, err := repository.RunFraudMonitor(ctx, time.Now().Add(-time.Hour), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("expected dedupe on replay, raised %d more alerts", again)
	}
}
