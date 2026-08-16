// SimulatedPayoutProvider stands in for a real bank rail (Paystack
// Transfers, Flutterwave, Providus). It is deterministic on the request's
// reference and bank code so tests and the demo can rehearse both success and
// failure paths: bank code 011 (First Bank) is always declined, everything
// else settles. Replays of the same reference return the same outcome,
// mirroring a rail's idempotency key.
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// SimulatedPayoutProvider is the default outbound rail for the demo.
type SimulatedPayoutProvider struct {
	mu       sync.Mutex
	outcomes map[string]ports.PayoutResult
	now      func() time.Time
}

// NewSimulatedPayoutProvider constructs the deterministic simulated rail.
func NewSimulatedPayoutProvider() *SimulatedPayoutProvider {
	return &SimulatedPayoutProvider{outcomes: map[string]ports.PayoutResult{}, now: time.Now}
}

// Payout returns the deterministic outcome for the reference. The same
// reference always resolves to the same outcome (idempotent rail replay).
func (p *SimulatedPayoutProvider) Payout(ctx context.Context, req ports.PayoutRequest) (ports.PayoutResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if res, ok := p.outcomes[req.Reference]; ok {
		return res, nil
	}
	var res ports.PayoutResult
	switch req.BankCode {
	case "011":
		res = ports.PayoutResult{
			Status:  "failed",
			Message: "simulated rail declined: recipient bank unavailable",
		}
	default:
		res = ports.PayoutResult{
			Status:      "succeeded",
			ExternalRef: fmt.Sprintf("SIM-PAY-%s-%d", req.Reference, p.now().Unix()),
			Message:     "simulated payout completed",
		}
	}
	p.outcomes[req.Reference] = res
	return res, nil
}
