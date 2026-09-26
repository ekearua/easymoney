package kyc

import (
	"strings"
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

// TestRunTransactionMonitorRails pins the rail-agnostic behaviour: velocity
// and structuring count movements on every rail in the same window, single-
// event rules carry the event's rail and reference, and a non-payment rail
// never claims a payment id.
func TestRunTransactionMonitorRails(t *testing.T) {
	t.Parallel()

	t.Run("velocity counts every rail in the window", func(t *testing.T) {
		now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
		userID := uuid.New()
		var events []TransactionInput
		for i := 0; i < 10; i++ {
			events = append(events, TransactionInput{
				Rail: RailPayment, Reference: uuid.NewString(), PaymentID: uuid.New(),
				UserID: userID, MerchantID: uuid.New(), MerchantName: "Kora Books",
				MerchantCategory: "Books", AmountKobo: 100_000,
				PaidAt: now.Add(-time.Duration(i) * time.Minute),
			})
		}
		// The eleventh movement is a bank credit on a different rail: it must
		// count in the same velocity window as the card payments.
		events = append(events, TransactionInput{
			Rail: RailStandingCredit, Reference: "STD-CREDIT-0009", UserID: userID,
			MerchantName: "Standing account", MerchantCategory: "Bank transfer",
			AmountKobo: 250_000, PaidAt: now,
		})
		alerts := RunTransactionMonitor(events, MonitorConfig{VelocityWindow: time.Hour, VelocityLimit: 10})
		if len(alerts) != 1 || alerts[0].Rule != RuleVelocity {
			t.Fatalf("expected a velocity alert, got %+v", alerts)
		}
		if got := strings.Join(alerts[0].Rails, ","); got != "payment,standing_credit" {
			t.Fatalf("velocity rails = %q, want payment,standing_credit", got)
		}
		if len(alerts[0].References) != 11 {
			t.Fatalf("expected 11 event references, got %d", len(alerts[0].References))
		}
		if len(alerts[0].PaymentIDs) != 10 {
			t.Fatalf("expected only the 10 payment ids, got %d", len(alerts[0].PaymentIDs))
		}
	})

	t.Run("a standing credit carries its rail and reference", func(t *testing.T) {
		events := []TransactionInput{{
			Rail: RailStandingCredit, Reference: "STD-CREDIT-0042",
			UserID: uuid.New(), MerchantName: "Standing account",
			MerchantCategory: "Bank transfer", AmountKobo: 2_000_000, PaidAt: time.Now(),
		}}
		alerts := RunTransactionMonitor(events, MonitorConfig{RoundAmountStep: 1_000_000, RoundAmountMin: 1_000_000})
		if len(alerts) != 1 || alerts[0].Rule != RuleRoundAmounts {
			t.Fatalf("expected a round-amount alert, got %+v", alerts)
		}
		if len(alerts[0].Rails) != 1 || alerts[0].Rails[0] != RailStandingCredit {
			t.Fatalf("round-amount rails = %v, want [standing_credit]", alerts[0].Rails)
		}
		if len(alerts[0].References) != 1 || alerts[0].References[0] != "STD-CREDIT-0042" {
			t.Fatalf("round-amount references = %v", alerts[0].References)
		}
		if len(alerts[0].PaymentIDs) != 0 {
			t.Fatalf("a bank credit has no payment id, got %v", alerts[0].PaymentIDs)
		}
	})

	t.Run("structuring sees mixed rails", func(t *testing.T) {
		events, _ := fixedPayments()
		events = append(events, TransactionInput{
			Rail: RailStandingCredit, Reference: "STD-CREDIT-0011", UserID: events[0].UserID,
			MerchantName: "Standing account", MerchantCategory: "Bank transfer",
			AmountKobo: 500_000, PaidAt: time.Now(),
		})
		alerts := RunTransactionMonitor(events, MonitorConfig{
			StructuringWindow: time.Hour, StructuringCount: 4, StructuringFloor: 400_000,
		})
		if len(alerts) != 1 || alerts[0].Rule != RuleStructuring {
			t.Fatalf("expected a structuring alert, got %+v", alerts)
		}
		if got := strings.Join(alerts[0].Rails, ","); got != "payment,standing_credit" {
			t.Fatalf("structuring rails = %q", got)
		}
		if len(alerts[0].References) != 4 {
			t.Fatalf("structuring should carry every event reference, got %v", alerts[0].References)
		}
	})
}

// TestStructuringReportBoundary pins the CTR-aware structuring rule: the
// REPORT_CTR_THRESHOLD_KOBO boundary caps the ceiling, movements at or above
// the boundary are reportable transactions rather than structuring, an
// explicit lower ceiling still wins, and the alert names the boundary.
func TestStructuringReportBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	events := func(amounts ...int64) []TransactionInput {
		userID := uuid.New()
		var out []TransactionInput
		for i, amount := range amounts {
			out = append(out, TransactionInput{
				Rail: RailPayment, Reference: uuid.NewString(), PaymentID: uuid.New(),
				UserID: userID, MerchantID: uuid.New(), MerchantName: "Kora Books",
				MerchantCategory: "Books", AmountKobo: amount,
				PaidAt: now.Add(-time.Duration(i) * time.Minute),
			})
		}
		return out
	}

	t.Run("the report threshold caps the ceiling", func(t *testing.T) {
		alerts := RunTransactionMonitor(events(500_000, 600_000, 700_000, 1_500_000), MonitorConfig{
			StructuringWindow: time.Hour, StructuringCount: 3, StructuringFloor: 400_000,
			ReportThreshold: 1_000_000,
		})
		if len(alerts) != 1 || alerts[0].Rule != RuleStructuring {
			t.Fatalf("expected a structuring alert, got %+v", alerts)
		}
		if got := alerts[0].Details["count"]; got != 3 {
			t.Fatalf("only movements below the report threshold should count, got %v", got)
		}
		if got := alerts[0].Details["report_threshold"]; got != int64(1_000_000) {
			t.Fatalf("alert must name the CTR boundary, got %v", got)
		}
		if got := alerts[0].Details["ceil"]; got != int64(1_000_000) {
			t.Fatalf("effective ceiling = %v, want the report threshold", got)
		}
	})

	t.Run("an explicit lower ceiling still wins", func(t *testing.T) {
		alerts := RunTransactionMonitor(events(500_000, 600_000, 900_000), MonitorConfig{
			StructuringWindow: time.Hour, StructuringCount: 2, StructuringFloor: 400_000,
			StructuringCeil: 800_000, ReportThreshold: 1_000_000,
		})
		if len(alerts) != 1 {
			t.Fatalf("expected a structuring alert, got %+v", alerts)
		}
		if got := alerts[0].Details["count"]; got != 2 {
			t.Fatalf("movements above the configured ceiling must be ignored, got %v", got)
		}
		if got := alerts[0].Details["ceil"]; got != int64(800_000) {
			t.Fatalf("configured ceiling should win, got %v", got)
		}
	})

	t.Run("no report threshold leaves the rule unbounded", func(t *testing.T) {
		alerts := RunTransactionMonitor(events(500_000, 600_000, 700_000), MonitorConfig{
			StructuringWindow: time.Hour, StructuringCount: 3, StructuringFloor: 400_000,
		})
		if len(alerts) != 1 {
			t.Fatalf("expected a structuring alert, got %+v", alerts)
		}
		if _, ok := alerts[0].Details["report_threshold"]; ok {
			t.Fatalf("an unset boundary must not be reported: %+v", alerts[0].Details)
		}
		if _, ok := alerts[0].Details["ceil"]; ok {
			t.Fatalf("an unbounded rule must not claim a ceiling: %+v", alerts[0].Details)
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
