package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
)

// TestPostgresVirtualAccountInstructionRoundTrip persists a DVA instruction
// against a payment, reloads it, and verifies a re-initialized checkout
// replaces the previous account instead of duplicating rows.
func TestPostgresVirtualAccountInstructionRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.resetWithSeed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012349091")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	paymentID := uuid.New()
	if _, err := repository.CreatePayment(ctx, domain.Payment{
		ID: paymentID, UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 120_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "interswitch",
		ProviderReference: "ref-dva-integration", Channel: ChannelCheckout,
		ReceiptToken: "tok-dva-integration", Recipient: user.WhatsAppNumber,
	}); err != nil {
		t.Fatal(err)
	}

	first := ports.TransferInstruction{
		AccountNumber: "9951001234", AccountName: "LATIFAT ABDUL", BankName: "PROVIDUS BANK",
		Reference: "DVA-REF-001", ValidityMins: 30, ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	if err := repository.SetVirtualAccountInstruction(ctx, paymentID, first); err != nil {
		t.Fatal(err)
	}
	got, err := repository.VirtualAccountInstructionByPaymentID(ctx, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountNumber != first.AccountNumber || got.AccountName != first.AccountName ||
		got.BankName != first.BankName || got.ProviderReference != first.Reference ||
		got.ValidityMins != first.ValidityMins {
		t.Fatalf("instruction mismatch: got %+v want %+v", got, first)
	}
	if got.PaymentID != paymentID || got.CreatedAt.IsZero() {
		t.Fatalf("instruction metadata not persisted: %+v", got)
	}

	replacement := ports.TransferInstruction{
		AccountNumber: "9953987654", AccountName: "PEPE ABDUL", BankName: "PROVIDUS BANK",
		Reference: "DVA-REF-002", ValidityMins: 60, ExpiresAt: time.Now().Add(60 * time.Minute),
	}
	if err := repository.SetVirtualAccountInstruction(ctx, paymentID, replacement); err != nil {
		t.Fatal(err)
	}
	got, err = repository.VirtualAccountInstructionByPaymentID(ctx, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountNumber != replacement.AccountNumber || got.ProviderReference != replacement.Reference {
		t.Fatalf("replacement not applied: got %+v", got)
	}
}