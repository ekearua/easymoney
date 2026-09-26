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

// Monitoring rails. The engine is rail-agnostic: card/transfer payments,
// wallet top-ups, standing-account bank credits, refunds, and merchant
// settlement payouts are all evaluated by the same rules, so a structuring or
// velocity pattern cannot hide by switching rail.
const (
	RailPayment        = "payment"
	RailTopup          = "topup"
	RailStandingCredit = "standing_credit"
	RailRefund         = "refund"
	RailPayout         = "payout"
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

// TransactionInput is one settled money movement observed by the monitor.
type TransactionInput struct {
	// Rail identifies which money rail the event came from. Empty is treated as
	// RailPayment for backwards compatibility.
	Rail string
	// Reference is the rail's stable row reference (payment id, bank credit
	// provider reference, refund id, payout id). It keys alert dedupe so a
	// redelivered or re-scanned event is never alerted twice.
	Reference string
	// PaymentID is set for payment-backed rails only; bank credits, refunds,
	// and payouts carry a Reference instead.
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
	StructuringCount  int   // minimum number of below-threshold movements
	StructuringFloor  int64 // movements at or above this amount count as structuring
	StructuringCeil   int64 // movements >= Ceil are ignored by this rule

	// ReportThreshold is the currency-transaction reporting boundary in kobo
	// (REPORT_CTR_THRESHOLD_KOBO). When > 0 it caps StructuringCeil, so the
	// rule only fires for clusters that stay below the reporting boundary and
	// alerts name the boundary they straddle instead of a bare hardcoded
	// ceiling.
	ReportThreshold int64

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
	// Rails is the sorted distinct set of rails the alert was raised over.
	Rails []string
	// References are the stable event references behind the alert; the first
	// one is recorded with the alert and used to dedupe redeliveries.
	References []string
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

	// Velocity: more than N settled money movements inside the window, across
	// every rail.
	if len(events) >= cfg.VelocityLimit {
		sorted := sortedByPaidAt(events)
		cutoff := sorted[len(sorted)-1].PaidAt.Add(-cfg.VelocityWindow)
		var within []TransactionInput
		var ids []uuid.UUID
		for _, e := range sorted {
			if !e.PaidAt.Before(cutoff) {
				within = append(within, e)
				if e.PaymentID != uuid.Nil {
					ids = append(ids, e.PaymentID)
				}
			}
		}
		if len(within) >= cfg.VelocityLimit {
			alerts = append(alerts, MonitorAlert{
				Rule:       RuleVelocity,
				Severity:   AlertMedium,
				UserID:     events[0].UserID,
				PaymentIDs: ids,
				Rails:      distinctRails(within),
				References: referencesOf(within),
				Details:    map[string]any{"window": cfg.VelocityWindow.String(), "limit": cfg.VelocityLimit, "count": len(within)},
			})
		}
	}

	// Structuring: several payments just under a reporting threshold inside
	// the window. The rule only engages when a floor is configured; payments at
	// or above the ceiling are ignored (they are handled as large transfers).
	// The reporting boundary caps the rule: a movement at or above the CTR
	// threshold is a reportable transaction, not a structuring candidate.
	ceil := cfg.StructuringCeil
	if cfg.ReportThreshold > 0 && (ceil <= 0 || ceil > cfg.ReportThreshold) {
		ceil = cfg.ReportThreshold
	}
	var structured []TransactionInput
	var structuredIDs []uuid.UUID
	latest := time.Time{}
	if cfg.StructuringFloor > 0 {
		for _, e := range events {
			if e.AmountKobo < cfg.StructuringFloor {
				continue
			}
			if ceil > 0 && e.AmountKobo >= ceil {
				continue
			}
			structured = append(structured, e)
			if e.PaymentID != uuid.Nil {
				structuredIDs = append(structuredIDs, e.PaymentID)
			}
			if e.PaidAt.After(latest) {
				latest = e.PaidAt
			}
		}
	}
	if len(structured) >= cfg.StructuringCount {
		details := map[string]any{
			"count":  len(structured),
			"floor":  cfg.StructuringFloor,
			"window": cfg.StructuringWindow.String(),
			"latest": latest,
		}
		if ceil > 0 {
			details["ceil"] = ceil
		}
		if cfg.ReportThreshold > 0 {
			// Name the CTR boundary the cluster stays under, so an investigator
			// sees why these amounts are suspicious together.
			details["report_threshold"] = cfg.ReportThreshold
		}
		alerts = append(alerts, MonitorAlert{
			Rule:       RuleStructuring,
			Severity:   AlertHigh,
			UserID:     events[0].UserID,
			PaymentIDs: structuredIDs,
			Rails:      distinctRails(structured),
			References: referencesOf(structured),
			Details:    details,
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
				PaymentIDs: paymentIDsOf(e),
				Rails:      []string{railOf(e)},
				References: []string{referenceOf(e)},
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
				PaymentIDs: paymentIDsOf(e),
				Rails:      []string{railOf(e)},
				References: []string{referenceOf(e)},
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

// railOf resolves the event's rail, defaulting an unset rail to a payment so
// inputs built before rails existed keep their meaning.
func railOf(e TransactionInput) string {
	if e.Rail == "" {
		return RailPayment
	}
	return e.Rail
}

// referenceOf resolves the event's stable dedupe reference, falling back to the
// payment id when the rail did not supply one.
func referenceOf(e TransactionInput) string {
	if e.Reference != "" {
		return e.Reference
	}
	if e.PaymentID != uuid.Nil {
		return e.PaymentID.String()
	}
	return ""
}

// paymentIDsOf returns the payment-backed ids of one event (none for non-
// payment rails).
func paymentIDsOf(e TransactionInput) []uuid.UUID {
	if e.PaymentID == uuid.Nil {
		return nil
	}
	return []uuid.UUID{e.PaymentID}
}

// distinctRails returns the sorted distinct rails behind a set of events.
func distinctRails(events []TransactionInput) []string {
	seen := map[string]bool{}
	for _, e := range events {
		seen[railOf(e)] = true
	}
	rails := make([]string, 0, len(seen))
	for rail := range seen {
		rails = append(rails, rail)
	}
	sort.Strings(rails)
	return rails
}

// referencesOf returns the distinct non-empty references behind a set of
// events, in event order, so the first one is stable for dedupe.
func referencesOf(events []TransactionInput) []string {
	seen := map[string]bool{}
	var refs []string
	for _, e := range events {
		ref := referenceOf(e)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}
