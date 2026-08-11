package kyc

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func fixedPayments() ([]TransactionInput, time.Time) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	return []TransactionInput{
		{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "Kora Books", MerchantCategory: "Books", AmountKobo: 500_000, PaidAt: now.Add(-10 * time.Minute)},
		{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "Lagos Lunchbox", MerchantCategory: "Food", AmountKobo: 750_000, PaidAt: now.Add(-5 * time.Minute)},
		{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "BrightFix NG", MerchantCategory: "Services", AmountKobo: 1_000_000, PaidAt: now},
	}, now
}

func TestRunTransactionMonitor(t *testing.T) {
	t.Run("clean profile fires nothing", func(t *testing.T) {
		events, _ := fixedPayments()
		cfg := MonitorConfig{
			VelocityWindow:     time.Hour,
			VelocityLimit:      10,
			StructuringWindow:  time.Hour,
			StructuringCount:   3,
			StructuringFloor:   900_000,
			StructuringCeil:    0,
			RoundAmountStep:    500_000,
			RoundAmountMin:     2_000_000,
			HighRiskCategories: []string{"Gambling", "Forex"},
		}
		alerts := RunTransactionMonitor(events, cfg)
		if len(alerts) != 0 {
			t.Fatalf("expected no alerts, got %d: %+v", len(alerts), alerts)
		}
	})

	t.Run("velocity fires above the window limit", func(t *testing.T) {
		now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
		var events []TransactionInput
		for i := 0; i < 12; i++ {
			events = append(events, TransactionInput{
				PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(),
				MerchantName: "Kora Books", MerchantCategory: "Books",
				AmountKobo: 100_000, PaidAt: now.Add(-time.Duration(i) * time.Minute),
			})
		}
		alerts := RunTransactionMonitor(events, MonitorConfig{
			VelocityWindow: time.Hour, VelocityLimit: 10,
		})
		if len(alerts) != 1 || alerts[0].Rule != RuleVelocity {
			t.Fatalf("expected a velocity alert, got %+v", alerts)
		}
		if alerts[0].Severity != AlertMedium {
			t.Fatalf("velocity severity = %q, want medium", alerts[0].Severity)
		}
	})

	t.Run("structuring fires for clustered below-threshold payments", func(t *testing.T) {
		events, _ := fixedPayments()
		cfg := MonitorConfig{
			StructuringWindow: time.Hour,
			StructuringCount:  3,
			StructuringFloor:  400_000,
			StructuringCeil:   0,
		}
		alerts := RunTransactionMonitor(events, cfg)
		if len(alerts) != 1 || alerts[0].Rule != RuleStructuring {
			t.Fatalf("expected a structuring alert, got %+v", alerts)
		}
		if alerts[0].Severity != AlertHigh {
			t.Fatalf("structuring severity = %q, want high", alerts[0].Severity)
		}
	})

	t.Run("round amounts fire for large clean multiples", func(t *testing.T) {
		events := []TransactionInput{
			{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "Lagos Lunchbox", MerchantCategory: "Food", AmountKobo: 5_000_000, PaidAt: time.Now()},
		}
		alerts := RunTransactionMonitor(events, MonitorConfig{
			RoundAmountStep: 500_000,
			RoundAmountMin:  1_000_000,
		})
		if len(alerts) != 1 || alerts[0].Rule != RuleRoundAmounts {
			t.Fatalf("expected a round-amount alert, got %+v", alerts)
		}
		if alerts[0].Severity != AlertLow {
			t.Fatalf("round-amount severity = %q, want low", alerts[0].Severity)
		}
	})

	t.Run("high-risk counterparty fires by category", func(t *testing.T) {
		events := []TransactionInput{
			{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "BetKing NG", MerchantCategory: "Gambling", AmountKobo: 200_000, PaidAt: time.Now()},
		}
		alerts := RunTransactionMonitor(events, MonitorConfig{
			HighRiskCategories: []string{"Gambling", "Forex"},
		})
		if len(alerts) != 1 || alerts[0].Rule != RuleHighRiskCounterparty {
			t.Fatalf("expected a high-risk counterparty alert, got %+v", alerts)
		}
		if alerts[0].Severity != AlertMedium {
			t.Fatalf("counterparty severity = %q, want medium", alerts[0].Severity)
		}
	})

	t.Run("round and counterparty alerts coexist", func(t *testing.T) {
		events := []TransactionInput{
			{PaymentID: uuid.New(), UserID: uuid.New(), MerchantID: uuid.New(), MerchantName: "BetKing NG", MerchantCategory: "Gambling", AmountKobo: 5_000_000, PaidAt: time.Now()},
		}
		alerts := RunTransactionMonitor(events, MonitorConfig{
			RoundAmountStep:    500_000,
			RoundAmountMin:     1_000_000,
			HighRiskCategories: []string{"Gambling"},
		})
		if len(alerts) != 2 {
			t.Fatalf("expected 2 alerts, got %d: %+v", len(alerts), alerts)
		}
	})
}

func TestAlertScore(t *testing.T) {
	cases := map[string]float64{AlertLow: 15, AlertMedium: 35, AlertHigh: 60}
	for severity, want := range cases {
		if got := AlertScore(severity); got != want {
			t.Errorf("AlertScore(%q) = %v, want %v", severity, got, want)
		}
	}
}
