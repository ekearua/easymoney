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
		ProviderReference: "wpd_ref",
	}}
	valid := ports.Verification{
		Reference: "wpd_ref", Status: "success", AmountKobo: 50_000,
		Currency: "NGN", Domain: "test", Channel: "card",
		Metadata: map[string]string{"payment_id": paymentID.String(), "merchant_id": merchantID.String()},
	}
	if err := validateVerification(payment, valid); err != nil {
		t.Fatalf("valid verification rejected: %v", err)
	}

	tests := []struct {
		name   string
		change func(*ports.Verification)
	}{
		{name: "reference", change: func(v *ports.Verification) { v.Reference = "other" }},
		{name: "amount", change: func(v *ports.Verification) { v.AmountKobo++ }},
		{name: "currency", change: func(v *ports.Verification) { v.Currency = "USD" }},
		{name: "live domain", change: func(v *ports.Verification) { v.Domain = "live" }},
		{name: "channel", change: func(v *ports.Verification) { v.Channel = "bank" }},
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

func TestGatewayStatusMapping(t *testing.T) {
	t.Parallel()
	if got := mapGatewayStatus("success"); got != domain.StatusSucceeded {
		t.Fatalf("success maps to %q", got)
	}
	if got := mapGatewayStatus("pending"); got != domain.StatusPending {
		t.Fatalf("pending maps to %q", got)
	}
	if got := mapGatewayStatus("mystery"); got != "" {
		t.Fatalf("unknown maps to %q", got)
	}
}
