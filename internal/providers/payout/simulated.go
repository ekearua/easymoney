package payout

import (
	"context"
	"fmt"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

type SimulatedProvider struct {
	mu       sync.Mutex
	outcomes map[string]ports.PayoutResult
	now      func() time.Time
}

func NewSimulatedProvider() *SimulatedProvider {
	return &SimulatedProvider{outcomes: map[string]ports.PayoutResult{}, now: time.Now}
}

func (p *SimulatedProvider) Payout(ctx context.Context, req ports.PayoutRequest) (ports.PayoutResult, error) {
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
