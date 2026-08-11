package kyc

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// C14: transaction monitoring rules. The engine evaluates settled payments per
// customer against flat bounds plus behaviour rules (velocity, structuring,
// round amounts, high-risk counterparties) and emits alerts that feed both the
// manual review queue and the ML/FT risk score (each alert becomes a risk
// event recorded by the store).

// Alert severities (transaction_alerts.severity).
const (
	AlertLow    = "low"
	AlertMedium = "medium"
	AlertHigh   = "high"
)

// Monitoring rule identifiers (transaction_alerts.rule).
const (
	RuleVelocity             = "velocity"
	RuleStructuring          = "structuring"
	RuleRoundAmounts         = "round_amounts"
	RuleHighRiskCounterparty = "high_risk_counterparty"
)

// AlertScore maps an alert severity onto the ML/FT risk score contribution.
func AlertScore(severity string) float64 {
	switch severity {
	case AlertHigh:
		return 60
	case AlertMedium:
		return 35
	}
	return 15
}

// TransactionInput is one settled payment observed by the monitor.
type TransactionInput struct {
	PaymentID        uuid.UUID
	UserID           uuid.UUID
	MerchantID       uuid.UUID
	MerchantName     string
	MerchantCategory string
	AmountKobo       int64
	PaidAt           time.Time
}

// MonitorConfig parameterizes the detection rules.
type MonitorConfig struct {
	VelocityWindow time.Duration
	VelocityLimit  int // max settled payments per window before an alert

	StructuringWindow time.Duration
	StructuringCount  int   // minimum number of below-threshold payments
	StructuringFloor  int64 // payments in [Floor, Floor+Floor) count as structuring
	StructuringCeil   int64 // payments >= Ceil are ignored by this rule

	RoundAmountStep int64 // amounts divisible by this step flag the rule
	RoundAmountMin  int64 // smallest amount that can flag the rule

	HighRiskCategories []string
}

// MonitorAlert is one suspicious activity detection.
type MonitorAlert struct {
	Rule       string
	Severity   string
	UserID     uuid.UUID
	PaymentIDs []uuid.UUID
	AmountKobo int64
	Details    map[string]any
}

// RunTransactionMonitor evaluates settled payments for a customer and returns
// the rules that fired. Events should be for a single user; callers group by
// user before invoking. Alerts are returned in stable order (rule, payment).
func RunTransactionMonitor(events []TransactionInput, cfg MonitorConfig) []MonitorAlert {
	if cfg.VelocityWindow <= 0 || cfg.VelocityLimit <= 0 {
		cfg.VelocityWindow = 24 * time.Hour
		cfg.VelocityLimit = 10
	}
	if cfg.StructuringWindow <= 0 || cfg.StructuringCount <= 0 {
		cfg.StructuringWindow = time.Hour
		cfg.StructuringCount = 3
	}
	var alerts []MonitorAlert

	// Velocity: more than N settled payments inside the window.
	if len(events) >= cfg.VelocityLimit {
		sorted := sortedByPaidAt(events)
		cutoff := sorted[len(sorted)-1].PaidAt.Add(-cfg.VelocityWindow)
		within := 0
		var ids []uuid.UUID
		for _, e := range sorted {
			if !e.PaidAt.Before(cutoff) {
				within++
				ids = append(ids, e.PaymentID)
			}
		}
		if within >= cfg.VelocityLimit {
			alerts = append(alerts, MonitorAlert{
				Rule:       RuleVelocity,
				Severity:   AlertMedium,
				UserID:     events[0].UserID,
				PaymentIDs: ids,
				Details:    map[string]any{"window": cfg.VelocityWindow.String(), "limit": cfg.VelocityLimit, "count": within},
			})
		}
	}

	// Structuring: several payments just under a reporting threshold inside
	// the window. The rule only engages when a floor is configured; payments at
	// or above the ceiling are ignored (they are handled as large transfers).
	structured := 0
	var structuredIDs []uuid.UUID
	latest := time.Time{}
	if cfg.StructuringFloor > 0 {
		for _, e := range events {
			if e.AmountKobo < cfg.StructuringFloor {
				continue
			}
			if cfg.StructuringCeil > 0 && e.AmountKobo >= cfg.StructuringCeil {
				continue
			}
			structured++
			structuredIDs = append(structuredIDs, e.PaymentID)
			if e.PaidAt.After(latest) {
				latest = e.PaidAt
			}
		}
	}
	if structured >= cfg.StructuringCount {
		alerts = append(alerts, MonitorAlert{
			Rule:       RuleStructuring,
			Severity:   AlertHigh,
			UserID:     events[0].UserID,
			PaymentIDs: structuredIDs,
			Details: map[string]any{
				"count":  structured,
				"floor":  cfg.StructuringFloor,
				"window": cfg.StructuringWindow.String(),
				"latest": latest,
			},
		})
	}

	// Round amounts: a large payment that is a clean multiple of the step.
	if cfg.RoundAmountStep > 0 {
		for _, e := range events {
			if e.AmountKobo < cfg.RoundAmountMin {
				continue
			}
			if e.AmountKobo%cfg.RoundAmountStep != 0 {
				continue
			}
			alerts = append(alerts, MonitorAlert{
				Rule:       RuleRoundAmounts,
				Severity:   AlertLow,
				UserID:     e.UserID,
				PaymentIDs: []uuid.UUID{e.PaymentID},
				AmountKobo: e.AmountKobo,
				Details:    map[string]any{"step": cfg.RoundAmountStep},
			})
		}
	}

	// High-risk counterparties: merchant category on the deny list.
	if len(cfg.HighRiskCategories) > 0 {
		categorySet := make(map[string]bool, len(cfg.HighRiskCategories))
		for _, c := range cfg.HighRiskCategories {
			categorySet[c] = true
		}
		for _, e := range events {
			if !categorySet[e.MerchantCategory] {
				continue
			}
			alerts = append(alerts, MonitorAlert{
				Rule:       RuleHighRiskCounterparty,
				Severity:   AlertMedium,
				UserID:     e.UserID,
				PaymentIDs: []uuid.UUID{e.PaymentID},
				AmountKobo: e.AmountKobo,
				Details: map[string]any{
					"merchant":    e.MerchantName,
					"merchant_id": e.MerchantID.String(),
					"category":    e.MerchantCategory,
				},
			})
		}
	}
	return alerts
}

func sortedByPaidAt(events []TransactionInput) []TransactionInput {
	out := make([]TransactionInput, len(events))
	copy(out, events)
	sort.Slice(out, func(i, j int) bool { return out[i].PaidAt.Before(out[j].PaidAt) })
	return out
}
