// Package paymentscreening provides the simulated payment-time counterparty
// screening vendor behind the PaymentScreener port. It mirrors the
// cac/firs/geo simulated-adapter convention: a deterministic, dependency-free
// stand-in for the real vendor (Firco/Actimize/Napier lane in
// AML_VENDOR_DECISION_RECORD.md), so the G2 hold/interdiction semantics are
// exercisable end to end and a live adapter can be dropped in behind the same
// interface without touching the service seam.
package paymentscreening

import (
	"context"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// Simulated screens counterparty names against three deterministic lists:
//
//   - confirmed blocklist: names containing "sanctioned" or "watchlisted"
//     (case-insensitive) are decisive hits — the decision is blocked;
//   - review list: names containing "similar" produce a fuzzy hit — the
//     decision is strong (ambiguous: hold for review, never auto-clear);
//   - everyone else is clear.
//
// The thresholds (name containment, no amount gate) are deliberately simple;
// the point of the simulator is the decision vocabulary and the seam's
// enforcement behavior, not matching quality.
type Simulated struct{}

// NewSimulated creates the deterministic simulated payment screener.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName names the vendor recorded on screening rows.
func (s *Simulated) ProviderName() string { return "simulated" }

// ScreenPayment applies the deterministic lists to the counterparty name.
// A provider outage is part of the contract: an empty counterparty name is
// rejected as an ambiguous outcome rather than silently cleared, because a
// movement without a screenable name must never auto-pass.
func (s *Simulated) ScreenPayment(_ context.Context, req ports.PaymentScreenRequest) (ports.PaymentScreenDecision, error) {
	name := strings.TrimSpace(req.CounterpartyName)
	if name == "" {
		return ports.PaymentScreenDecision{
			Decision:     kyc.ScreenPossible,
			MatchedNames: []string{},
			ListVersion:  "sim-1",
			Message:      "counterparty name missing; movement cannot be auto-cleared",
		}, nil
	}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "sanctioned"), strings.Contains(lower, "watchlisted"):
		return ports.PaymentScreenDecision{
			Decision:     kyc.ScreenBlocked,
			MatchedNames: []string{name},
			ProviderRef:  "sim-pscreen-blocked-" + req.SubjectID,
			ListVersion:  "sim-1",
			Message:      "counterparty matches the simulated consolidated blocklist",
		}, nil
	case strings.Contains(lower, "similar"):
		return ports.PaymentScreenDecision{
			Decision:     kyc.ScreenStrong,
			MatchedNames: []string{name},
			ProviderRef:  "sim-pscreen-review-" + req.SubjectID,
			ListVersion:  "sim-1",
			Message:      "counterparty resembles a simulated watchlist entry; review required",
		}, nil
	default:
		return ports.PaymentScreenDecision{
			Decision:    kyc.ScreenClear,
			ProviderRef: fmt.Sprintf("sim-pscreen-clear-%s-%d", req.SubjectID, req.AmountKobo),
			ListVersion: "sim-1",
			Message:     "no simulated list matches",
		}, nil
	}
}
