package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

type stubGateway struct {
	initErr   error
	verifyErr error
}

func (g *stubGateway) Initialize(_ context.Context, _ ports.InitializePayment) (ports.Checkout, error) {
	return ports.Checkout{Reference: "ref", URL: "https://checkout.test"}, g.initErr
}

func (g *stubGateway) Verify(_ context.Context, _ string, _ int64) (ports.Verification, error) {
	return ports.Verification{Status: "success"}, g.verifyErr
}

func (g *stubGateway) ValidateWebhook(_ []byte, _ string) (ports.GatewayWebhook, error) {
	return ports.GatewayWebhook{}, nil
}

func newTestLogger() *slog.Logger {
	return slog.Default()
}

func TestPickProviderReturnsHealthy(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"paystack":    &stubGateway{},
		"flutterwave": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())
	got := router.PickProvider()
	if got != "paystack" && got != "flutterwave" {
		t.Fatalf("expected paystack or flutterwave, got %q", got)
	}
}

func TestPickProviderSkipsUnhealthy(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"paystack":    &stubGateway{},
		"flutterwave": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())

	// Mark paystack as unhealthy via repeated errors.
	for i := 0; i < 3; i++ {
		router.ReportResult("paystack", 10*time.Millisecond, errors.New("down"))
	}

	got := router.PickProvider()
	if got != "flutterwave" {
		t.Fatalf("expected flutterwave after paystack failure, got %q", got)
	}
}

func TestPickProviderPrefersLowerLatency(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"fast": &stubGateway{},
		"slow": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())

	// Train latency: fast at 10ms, slow at 200ms.
	for i := 0; i < 5; i++ {
		router.ReportResult("fast", 10*time.Millisecond, nil)
		router.ReportResult("slow", 200*time.Millisecond, nil)
	}

	got := router.PickProvider()
	if got != "fast" {
		t.Fatalf("expected fast provider, got %q", got)
	}
}

func TestPickProviderFallsBackWhenAllUnhealthy(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"a": &stubGateway{},
		"b": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())

	// Mark both unhealthy.
	for i := 0; i < 3; i++ {
		router.ReportResult("a", 10*time.Millisecond, errors.New("down"))
		router.ReportResult("b", 10*time.Millisecond, errors.New("down"))
	}

	got := router.PickProvider()
	if got != "a" && got != "b" {
		t.Fatalf("expected fallback to any provider, got %q", got)
	}
}

func TestReportResultMarksHealthyAfterSuccess(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"p": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())

	// Make it unhealthy.
	for i := 0; i < 3; i++ {
		router.ReportResult("p", 10*time.Millisecond, errors.New("down"))
	}
	got := router.PickProvider()
	// Even though unhealthy, there's only one provider so it's still picked.
	if got != "p" {
		t.Fatalf("expected p, got %q", got)
	}

	// One success makes it healthy again.
	router.ReportResult("p", 10*time.Millisecond, nil)
	st := router.stats["p"]
	if !st.healthy || st.consecutiveErrors != 0 {
		t.Fatalf("provider should be healthy after success: %+v", st)
	}
}

func TestProvidersReturnsAllNames(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"paystack":    &stubGateway{},
		"flutterwave": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())
	names := router.Providers()
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d: %v", len(names), names)
	}
}

func TestGatewayReturnsCorrectInstance(t *testing.T) {
	t.Parallel()
	ps := &stubGateway{}
	fw := &stubGateway{}
	gateways := map[string]ports.PaymentGateway{
		"paystack":    ps,
		"flutterwave": fw,
	}
	router := NewProviderRouter(gateways, newTestLogger())
	if router.Gateway("paystack") != ps {
		t.Fatal("expected paystack gateway instance")
	}
	if router.Gateway("flutterwave") != fw {
		t.Fatal("expected flutterwave gateway instance")
	}
}

func TestSetHealthyOverrides(t *testing.T) {
	t.Parallel()
	gateways := map[string]ports.PaymentGateway{
		"p": &stubGateway{},
	}
	router := NewProviderRouter(gateways, newTestLogger())
	for i := 0; i < 3; i++ {
		router.ReportResult("p", 10*time.Millisecond, errors.New("down"))
	}
	if router.stats["p"].healthy {
		t.Fatal("provider should be unhealthy")
	}
	router.SetHealthy("p", true)
	if !router.stats["p"].healthy {
		t.Fatal("provider should be healthy after SetHealthy")
	}
}
