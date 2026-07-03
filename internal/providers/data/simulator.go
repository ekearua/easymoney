// Package data provides provider adapters for mobile-data fulfilment.
package data

import (
	"context"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// Simulator fulfils data orders locally for the MVP while preserving the same
// boundary a live VTU or network provider will use later.
type Simulator struct{}

// NewSimulator creates the local data fulfilment provider.
func NewSimulator() *Simulator {
	return &Simulator{}
}

// FulfilData returns a successful provider result for active seeded plans.
func (s *Simulator) FulfilData(_ context.Context, request ports.DataFulfilmentRequest) (ports.DataFulfilmentResult, error) {
	if strings.Contains(strings.ToUpper(request.ProviderSKU), "FAIL") {
		return ports.DataFulfilmentResult{Status: "failed", Message: "simulated provider failure"}, nil
	}
	return ports.DataFulfilmentResult{
		ProviderReference: "SIM-DATA-" + request.RequestCode,
		Status:            "fulfilled",
		Message:           fmt.Sprintf("%s %s delivered to %s", request.NetworkCode, request.PlanCode, request.BeneficiaryPhone),
	}, nil
}

// CheckDataStatus reloads the simulated result for a provider reference.
func (s *Simulator) CheckDataStatus(_ context.Context, providerReference string) (ports.DataFulfilmentResult, error) {
	if strings.TrimSpace(providerReference) == "" {
		return ports.DataFulfilmentResult{Status: "pending", Message: "no provider reference yet"}, nil
	}
	return ports.DataFulfilmentResult{ProviderReference: providerReference, Status: "fulfilled", Message: "fulfilled"}, nil
}
