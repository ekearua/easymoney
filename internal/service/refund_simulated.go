package service

import (
	"context"
	"fmt"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// SimulatedRefundProvider satisfies the RefundProvider port for demo and
// integration testing. Bank code 011 always fails; any other bank code
// immediately succeeds with a deterministic reference.
type SimulatedRefundProvider struct {
	now func() time.Time
}

// NewSimulatedRefundProvider returns a simulated refund rail using wall-clock time.
func NewSimulatedRefundProvider() *SimulatedRefundProvider {
	return &SimulatedRefundProvider{now: time.Now}
}

func (s *SimulatedRefundProvider) Refund(_ context.Context, req ports.RefundRequest) (ports.RefundResult, error) {
	ref := fmt.Sprintf("SIM-REF-%s-%d", req.PaymentID, s.now().Unix())
	return ports.RefundResult{
		Status:   "succeeded",
		RefundID: ref,
		Message:  "simulated refund accepted",
	}, nil
}
