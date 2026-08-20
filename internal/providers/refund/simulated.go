package refund

import (
	"context"
	"fmt"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

type SimulatedProvider struct {
	now func() time.Time
}

func NewSimulatedProvider() *SimulatedProvider {
	return &SimulatedProvider{now: time.Now}
}

func (s *SimulatedProvider) Refund(_ context.Context, req ports.RefundRequest) (ports.RefundResult, error) {
	ref := fmt.Sprintf("SIM-REF-%s-%d", req.PaymentID, s.now().Unix())
	return ports.RefundResult{
		Status:   "succeeded",
		RefundID: ref,
		Message:  "simulated refund accepted",
	}, nil
}
