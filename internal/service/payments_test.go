package service

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func TestValidateVerification(t *testing.T) {
	t.Parallel()
	paymentID := uuid.New()
	merchantID := uuid.New()
	payment := store.PaymentView{Payment: domain.Payment{
		ID: paymentID, MerchantID: merchantID, AmountKobo: 50_000, Currency: "NGN",
		Provider: ProviderInterswitch, ProviderReference: "wpd_ref",
	}}
	valid := ports.Verification{
		Reference: "wpd_ref", Status: "success", AmountKobo: 50_000,
		Currency: "NGN", Domain: "test", Channel: "card",
		Metadata: map[string]string{"payment_id": paymentID.String(), "merchant_id": merchantID.String()},
	}
	if err := validateVerification(payment, valid); err != nil {
		t.Fatalf("valid verification rejected: %v", err)
	}

	live := valid
	live.Domain = "live"
	if err := validateVerification(payment, live); err != nil {
		t.Fatalf("live-domain verification rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*ports.Verification)
	}{
		{name: "reference", change: func(v *ports.Verification) { v.Reference = "other" }},
		{name: "amount", change: func(v *ports.Verification) { v.AmountKobo++ }},
		{name: "currency", change: func(v *ports.Verification) { v.Currency = "USD" }},
		{name: "payment metadata", change: func(v *ports.Verification) { v.Metadata["payment_id"] = uuid.NewString() }},
		{name: "merchant metadata", change: func(v *ports.Verification) { v.Metadata["merchant_id"] = uuid.NewString() }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			copy := valid
			copy.Metadata = map[string]string{
				"payment_id":  valid.Metadata["payment_id"],
				"merchant_id": valid.Metadata["merchant_id"],
			}
			test.change(&copy)
			if err := validateVerification(payment, copy); err == nil {
				t.Fatal("mismatch should be rejected")
			}
		})
	}

	// A non-terminal requery has no authoritative amount yet: Interswitch's
	// gettransaction.json returns Amount 0 while a transaction is still being
	// processed, so the payment must stay pending instead of failing.
	pending := valid
	pending.Status = "pending"
	pending.AmountKobo = 0
	if err := validateVerification(payment, pending); err != nil {
		t.Fatalf("pending requery without settled amount should not be rejected: %v", err)
	}
}

func TestResultOutboxRespectsWhatsAppServiceWindow(t *testing.T) {
	t.Parallel()
	payments := &PaymentService{
		cfg: config.Config{
			BaseURL:                "https://demo.example",
			WhatsAppTemplateName:   "payment_status_update",
			WhatsAppTemplateLocale: "en",
		},
		logger: slog.Default(),
	}
	payment := store.PaymentView{
		Payment:        domain.Payment{UserID: uuid.New(), AmountKobo: 50_000, ReceiptToken: "receipt-token"},
		WhatsAppNumber: "+2348012345678", MerchantName: "Lagos Lunchbox", LastInboundAt: time.Now(),
	}
	recent := payments.resultOutbox(payment, domain.StatusSucceeded)
	if recent.Kind != "text" {
		t.Fatalf("recent conversation should use text, got %q", recent.Kind)
	}
	payment.LastInboundAt = time.Now().Add(-25 * time.Hour)
	late := payments.resultOutbox(payment, domain.StatusSucceeded)
	if late.Kind != "template" {
		t.Fatalf("late conversation should use template, got %q", late.Kind)
	}
	var payload struct {
		Name       string   `json:"name"`
		Parameters []string `json:"parameters"`
	}
	if err := json.Unmarshal(late.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Name != "payment_status_update" || len(payload.Parameters) != 4 {
		t.Fatalf("unexpected template payload: %#v", payload)
	}
}

func TestResultOutboxPreservesPaymentChannel(t *testing.T) {
	t.Parallel()
	payments := &PaymentService{
		cfg: config.Config{BaseURL: "https://demo.example"},
	}
	payment := store.PaymentView{
		Payment: domain.Payment{
			UserID: uuid.New(), AmountKobo: 50_000, ReceiptToken: "receipt-token",
			Channel: ChannelTelegram, Recipient: "12345",
		},
		MerchantName: "Lagos Lunchbox", LastInboundAt: time.Now(),
	}
	outbox := payments.resultOutbox(payment, domain.StatusSucceeded)
	if outbox.Channel != ChannelTelegram || outbox.Recipient != "12345" {
		t.Fatalf("unexpected outbox route: channel=%q recipient=%q", outbox.Channel, outbox.Recipient)
	}
}

func TestResultOutboxSkipsAPIChannel(t *testing.T) {
	t.Parallel()
	payments := &PaymentService{
		cfg: config.Config{BaseURL: "https://demo.example"},
	}
	payment := store.PaymentView{
		Payment: domain.Payment{
			UserID: uuid.New(), AmountKobo: 50_000, ReceiptToken: "receipt-token",
			Channel: ChannelAPI, Recipient: "+2348012345678",
		},
		MerchantName: "Lagos Lunchbox", LastInboundAt: time.Now(),
	}
	outbox := payments.resultOutbox(payment, domain.StatusSucceeded)
	if outbox.Channel != ChannelAPI || outbox.UserID != uuid.Nil {
		t.Fatalf("API channel should produce a skip-sentinel outbox, got channel=%q user=%s", outbox.Channel, outbox.UserID)
	}
}

func TestGatewayStatusMapping(t *testing.T) {
	t.Parallel()
	if got := mapGatewayStatus("success"); got != domain.StatusSucceeded {
		t.Fatalf("success maps to %q", got)
	}
	if got := mapGatewayStatus("pending"); got != domain.StatusPending {
		t.Fatalf("pending maps to %q", got)
	}
	if got := mapGatewayStatus("failed"); got != domain.StatusFailed {
		t.Fatalf("failed maps to %q", got)
	}
	if got := mapGatewayStatus("mystery"); got != "" {
		t.Fatalf("unknown maps to %q", got)
	}
}

func TestIsGatewaySuccessEvent(t *testing.T) {
	t.Parallel()
	payments := &PaymentService{}
	if !payments.IsGatewaySuccessEvent(ProviderInterswitch, "TRANSACTION.COMPLETED") {
		t.Fatal("expected TRANSACTION.COMPLETED to be a terminal interswitch event")
	}
	if payments.IsGatewaySuccessEvent(ProviderInterswitch, "TRANSACTION.CREATED") {
		t.Fatal("TRANSACTION.CREATED must not be terminal")
	}
	if payments.IsGatewaySuccessEvent(ProviderInterswitch, "TRANSACTION.UPDATED") {
		t.Fatal("TRANSACTION.UPDATED must not be terminal")
	}
	if payments.IsGatewaySuccessEvent(ProviderInterswitch, "charge.notification") {
		t.Fatal("legacy redirect event must not be terminal")
	}
	if payments.IsGatewaySuccessEvent("unknown", "TRANSACTION.COMPLETED") {
		t.Fatal("unknown provider must return false")
	}
}
