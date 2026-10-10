package fraud

import (
	"sort"
	"time"
)

// defaultWindow is used when a config leaves Window unset.
const defaultWindow = 24 * time.Hour

// RunFraudMonitor evaluates the fraud rules over events for a single subject.
// The caller groups events by (SubjectKind, SubjectID) before invoking, the
// same contract the AML monitor uses, so the engine stays a pure function.
// Alerts are returned in stable order.
func RunFraudMonitor(events []FraudEvent, cfg FraudConfig) []FraudAlert {
	if cfg.Window <= 0 {
		cfg.Window = defaultWindow
	}
	if len(events) == 0 {
		return nil
	}
	subjectKind, subjectID := events[0].SubjectKind, events[0].SubjectID

	sorted := make([]FraudEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OccurredAt.Before(sorted[j].OccurredAt) })
	cutoff := sorted[len(sorted)-1].OccurredAt.Add(-cfg.Window)

	var alerts []FraudAlert
	countAlert := func(rule, severity string, within []FraudEvent, details map[string]any) {
		alerts = append(alerts, FraudAlert{
			Rule:        rule,
			Severity:    severity,
			SubjectKind: subjectKind,
			SubjectID:   subjectID,
			Rails:       distinctRails(within),
			References:  referencesOf(within),
			Details:     details,
		})
	}

	// Bucket the events inside the window.
	var failed, payments, disputes, refunds, payouts []FraudEvent
	for _, e := range sorted {
		if e.OccurredAt.Before(cutoff) {
			continue
		}
		switch railOf(e) {
		case RailDispute:
			disputes = append(disputes, e)
		case RailRefund:
			refunds = append(refunds, e)
		case RailPayout:
			payouts = append(payouts, e)
		default:
			if e.Failed {
				failed = append(failed, e)
			} else {
				payments = append(payments, e)
			}
		}
	}

	if cfg.FailedAttemptLimit > 0 && len(failed) >= cfg.FailedAttemptLimit {
		countAlert(RuleFailedAttemptBurst, SeverityHigh, failed,
			map[string]any{"window": cfg.Window.String(), "limit": cfg.FailedAttemptLimit, "count": len(failed)})
	}
	if cfg.PaymentLimit > 0 && len(payments) >= cfg.PaymentLimit {
		countAlert(RulePaymentVelocity, SeverityMedium, payments,
			map[string]any{"window": cfg.Window.String(), "limit": cfg.PaymentLimit, "count": len(payments)})
	}
	if cfg.DisputeLimit > 0 && len(disputes) >= cfg.DisputeLimit {
		countAlert(RuleDisputeVelocity, SeverityHigh, disputes,
			map[string]any{"window": cfg.Window.String(), "limit": cfg.DisputeLimit, "count": len(disputes)})
	}
	if cfg.RefundLimit > 0 && len(refunds) >= cfg.RefundLimit {
		countAlert(RuleRefundVelocity, SeverityMedium, refunds,
			map[string]any{"window": cfg.Window.String(), "limit": cfg.RefundLimit, "count": len(refunds)})
	}
	if cfg.PayoutLimit > 0 && len(payouts) >= cfg.PayoutLimit {
		countAlert(RulePayoutVelocity, SeverityMedium, payouts,
			map[string]any{"window": cfg.Window.String(), "limit": cfg.PayoutLimit, "count": len(payouts)})
	}

	// New-destination payouts alert per offending payout: each is a distinct
	// money-out event, not a threshold count.
	for _, e := range payouts {
		if !e.NewDestination || e.AmountKobo < cfg.NewDestinationMinKobo {
			continue
		}
		alerts = append(alerts, FraudAlert{
			Rule:        RuleNewPayoutDestination,
			Severity:    SeverityHigh,
			SubjectKind: subjectKind,
			SubjectID:   subjectID,
			Rails:       []string{RailPayout},
			References:  referenceList(e),
			AmountKobo:  e.AmountKobo,
			Details:     map[string]any{"amount_kobo": e.AmountKobo},
		})
	}
	return alerts
}

// railOf resolves the event's rail, defaulting an unset rail to a payment so
// inputs built before rails existed keep their meaning.
func railOf(e FraudEvent) string {
	if e.Rail == "" {
		return RailPayment
	}
	return e.Rail
}

// distinctRails returns the sorted distinct rails behind a set of events.
func distinctRails(events []FraudEvent) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		r := railOf(e)
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// referencesOf returns the distinct non-empty references behind a set of
// events, in event order, so the first one is stable for dedupe.
func referencesOf(events []FraudEvent) []string {
	seen := map[string]bool{}
	var refs []string
	for _, e := range events {
		if e.Reference == "" || seen[e.Reference] {
			continue
		}
		seen[e.Reference] = true
		refs = append(refs, e.Reference)
	}
	return refs
}

// referenceList returns the single non-empty reference of one event.
func referenceList(e FraudEvent) []string {
	if e.Reference == "" {
		return nil
	}
	return []string{e.Reference}
}
