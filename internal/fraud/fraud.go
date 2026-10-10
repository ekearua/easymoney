// Package fraud implements the fraud rule engine (gap G13): a rule set
// distinct from the AML transaction monitor in internal/kyc, so payment and
// payout fraud signals can be triaged on their own queue and never silently
// become AML alerts. The engine is pure — callers load the events and persist
// the alerts — so every rule is table-testable without a database.
//
// The separation is deliberate: fraud alerts do NOT contribute to the ML/FT
// risk score (RiskEvent). PEP/sanctions/adverse-media feed AML; failed
// attempts, chargeback velocity and destination churn feed fraud; the two
// lanes stay independently measurable.
package fraud

import (
	"time"

	"github.com/google/uuid"
)

// Alert severities (transaction_alerts.severity).
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

// Subject kinds: a fraud alert attaches either to a customer or a merchant.
const (
	SubjectUser     = "user"
	SubjectMerchant = "merchant"
)

// Monitored rails.
const (
	RailPayment = "payment"
	RailDispute = "dispute"
	RailRefund  = "refund"
	RailPayout  = "payout"
)

// Lane is the transaction_alerts.lane value for fraud detections.
const Lane = "fraud"

// Rule identifiers (transaction_alerts.rule, lane='fraud').
const (
	RuleFailedAttemptBurst   = "failed_attempt_burst"
	RulePaymentVelocity      = "payment_velocity"
	RuleDisputeVelocity      = "dispute_velocity"
	RuleRefundVelocity       = "refund_velocity"
	RuleNewPayoutDestination = "new_payout_destination"
	RulePayoutVelocity       = "payout_velocity"
)

// AlertScore maps a fraud alert severity onto a weight. It is recorded on the
// alert details for queue ordering and tuning; it is not added to the ML/FT
// risk score.
func AlertScore(severity string) float64 {
	switch severity {
	case SeverityHigh:
		return 60
	case SeverityMedium:
		return 35
	}
	return 15
}

// FraudEvent is one observed movement or signal. SubjectKind + SubjectID
// identify who an alert attaches to (a user id or a merchant id).
type FraudEvent struct {
	// Rail is one of RailPayment, RailDispute, RailRefund, RailPayout. An
	// empty rail is treated as RailPayment.
	Rail string
	// Reference is the rail's stable row reference (payment id, dispute id,
	// refund id, payout id) and keys alert dedupe.
	Reference string
	// SubjectKind is SubjectUser or SubjectMerchant.
	SubjectKind string
	SubjectID   uuid.UUID
	// Failed marks a payment attempt that did not succeed (the card-testing
	// shape). Successful-payment velocity ignores failed attempts.
	Failed bool
	// AmountKobo is the movement amount.
	AmountKobo int64
	// NewDestination reports, for a payout, whether its destination bank
	// account was created inside the configured destination window. Callers
	// compute it from merchant_settlement_accounts.created_at.
	NewDestination bool
	OccurredAt     time.Time
}

// FraudConfig parameterizes the fraud rules. A zero limit disables its rule;
// a zero Window falls back to the default.
type FraudConfig struct {
	Window time.Duration

	FailedAttemptLimit int
	PaymentLimit       int
	DisputeLimit       int
	RefundLimit        int
	PayoutLimit        int

	// NewDestinationMinKobo is the smallest payout that flags the
	// new-destination rule; zero flags every payout to a new destination.
	NewDestinationMinKobo int64

	// NewDestinationWindow is how recently a payout destination account must
	// have been created to count as new. It is consumed by the caller that
	// builds FraudEvents (the store loader sets Event.NewDestination), not by
	// RunFraudMonitor; a zero window flags every destination.
	NewDestinationWindow time.Duration
}

// FraudAlert is one detection on the fraud lane.
type FraudAlert struct {
	Rule        string
	Severity    string
	SubjectKind string
	SubjectID   uuid.UUID
	Rails       []string
	References  []string
	AmountKobo  int64
	Details     map[string]any
}
