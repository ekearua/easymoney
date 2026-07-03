package data

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestSimulatorFulfilData(t *testing.T) {
	result, err := NewSimulator().FulfilData(context.Background(), ports.DataFulfilmentRequest{
		RequestCode:      "XG-DATA-12345678",
		NetworkCode:      "MTN",
		PlanCode:         "MTN1GB",
		ProviderSKU:      "SIM-MTN-1GB",
		BeneficiaryPhone: "+2348031234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fulfilled" || result.ProviderReference == "" {
		t.Fatalf("unexpected simulator result: %#v", result)
	}
}

func TestSimulatorForcedFailure(t *testing.T) {
	result, err := NewSimulator().FulfilData(context.Background(), ports.DataFulfilmentRequest{ProviderSKU: "SIM-FAIL"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed result, got %#v", result)
	}
}
