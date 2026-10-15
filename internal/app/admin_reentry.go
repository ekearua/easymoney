package app

import (
	"fmt"
	"time"

	"whatsapp-payment-demo/internal/store"
)

// Saved-methods evidence panel (docs/payments/SAVED_CARD_METHODS_PLAN.md Phase 0.5).
//
// The saved-card decision turns on a question nobody had measured: how much
// does re-entering card details actually cost? Posture A is cheap and delivers
// little; posture B costs a PCI DSS scope change. This panel is where the answer
// lives, so the decision rests on the funnel rather than on how annoying the
// problem sounds.
//
// buildReentryPanel is deliberately pure — given the store snapshot and a
// window it computes every label — so the reading rules are unit-testable
// rather than only visible in production.
const (
	// reentryMinCardAttempts is the sample below which the panel refuses to call
	// a rate. A percentage over a handful of attempts is noise, and a dashboard
	// that reports noise invites a decision made on it.
	reentryMinCardAttempts = 30

	// reentryMaterialAbandonment is the share of card attempts that must fail to
	// complete before re-entry friction is presented as worth acting on. This is
	// a stated convention, not a measured fact: it is the point at which roughly
	// one payer in seven reaches the card page and does not finish, which is
	// where the funnel loss reads as material rather than as ordinary card
	// declines. Anyone may argue with the number; the panel says which number it
	// used, so the argument can be had about the threshold instead of about the
	// arithmetic.
	reentryMaterialAbandonment = 0.15
)

// ReentryPanel is the rendered evidence: the aggregate figures, the per-merchant
// breakdown, and a stated reading of them.
type ReentryPanel struct {
	Window  string
	HasData bool

	Payments       int64
	CardAttempts   int64
	CardAbandoned  int64
	AbandonRate    string
	RepeatPayments int64
	RepeatPayers   int64
	RepeatShare    string
	FollowUps      int64
	FollowUpWindow string

	Merchants []ReentryMerchantRow

	// Reading states what the figures do and do not support, in the words the
	// panel shows. It is the whole point of the panel: it must never imply more
	// confidence than the sample carries.
	Reading string
}

// ReentryMerchantRow is one merchant's line in the breakdown.
type ReentryMerchantRow struct {
	Name           string
	Payments       int64
	CardAttempts   int64
	CardAbandoned  int64
	AbandonRate    string
	RepeatPayments int64
}

// buildReentryPanel renders the evidence for the window covered by the metrics.
func buildReentryPanel(metrics store.CardReentryMetrics, byMerchant []store.MerchantReentryMetric, window time.Duration) ReentryPanel {
	panel := ReentryPanel{
		Window:         windowLabel(window),
		Payments:       metrics.Payments,
		CardAttempts:   metrics.CardAttempts,
		CardAbandoned:  metrics.CardAbandoned,
		AbandonRate:    percentLabel(metrics.CardAbandonmentRate()),
		RepeatPayments: metrics.RepeatPayments,
		RepeatPayers:   metrics.RepeatPaymentPayerCount,
		RepeatShare:    percentLabel(metrics.RepeatPaymentShare()),
		FollowUps:      metrics.CardFollowUps,
		FollowUpWindow: windowLabel(store.CardReentryFollowUpWindow),
		Reading:        reentryReading(metrics),
	}
	panel.HasData = metrics.Payments > 0
	for _, m := range byMerchant {
		panel.Merchants = append(panel.Merchants, ReentryMerchantRow{
			Name:           m.MerchantName,
			Payments:       m.Payments,
			CardAttempts:   m.CardAttempts,
			CardAbandoned:  m.CardAbandoned,
			AbandonRate:    percentLabel(m.CardAbandonmentRate()),
			RepeatPayments: m.RepeatPayments,
		})
	}
	return panel
}

// reentryReading is the sentence an operator reads instead of doing the
// arithmetic. Every branch names the sample it rests on, because the failure
// mode of a dashboard like this is a confident percentage drawn from three rows.
func reentryReading(metrics store.CardReentryMetrics) string {
	if metrics.CardAttempts == 0 {
		if metrics.Payments == 0 {
			return "No payments in this window — nothing to read yet. The saved-card decision (posture A, B, or neither) cannot be made on an empty funnel."
		}
		return "No card attempts in this window, so there is no re-entry friction to measure. Whatever repeat-payer activity is shown here went through another rail."
	}
	if metrics.CardAttempts < reentryMinCardAttempts {
		return fmt.Sprintf(
			"Only %d card attempt(s) in this window — too few to read a rate from, and deliberately not called here. The measurement is live; it starts saying something at %d attempts.",
			metrics.CardAttempts, reentryMinCardAttempts)
	}
	rate := metrics.CardAbandonmentRate()
	if rate >= reentryMaterialAbandonment {
		return fmt.Sprintf(
			"%.0f%% of card attempts did not complete (%d of %d) — over the %.0f%% this panel treats as material. That is the case for spending on saved methods; compare it against the PCI DSS scope change posture B carries.",
			rate*100, metrics.CardAbandoned, metrics.CardAttempts, reentryMaterialAbandonment*100)
	}
	return fmt.Sprintf(
		"%.0f%% of card attempts did not complete (%d of %d) — below the %.0f%% this panel treats as material. On this evidence, re-entry friction does not yet justify a PCI DSS scope change: Option 0 (build neither, and speed up the hosted entry) stays on the table.",
		rate*100, metrics.CardAbandoned, metrics.CardAttempts, reentryMaterialAbandonment*100)
}

// percentLabel renders a 0..1 share for display, with no decimal places: the
// panel is a decision aid, not an accounting record, and a spurious second
// digit would imply a precision the sample does not have.
func percentLabel(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
}

// windowLabel renders a duration as the window a figure covers.
func windowLabel(window time.Duration) string {
	switch {
	case window >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(window.Hours()/24))
	case window >= time.Hour:
		return fmt.Sprintf("%dh", int(window.Hours()))
	case window >= time.Minute:
		return fmt.Sprintf("%dm", int(window.Minutes()))
	default:
		return window.String()
	}
}
