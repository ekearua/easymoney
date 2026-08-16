package service

import (
	"context"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

// TestResolveCheckout covers the request-money link lifecycle through the
// service: minting, resolving a payer, and the payment binding to the payee.
func TestResolveCheckout(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(24 * time.Hour)
	payments := &PaymentService{
		store:  repository,
		cfg:    config.Config{BaseURL: "https://xego.ng"},
		logger: testLogger(),
	}

	checkout, err := payments.CreateCheckout(ctx, merchant, store.CheckoutSpec{
		PayeeMerchantID: merchant.ID,
		Reference:       "XG-LINK-1",
		Note:            "Weekend groceries",
		AmountKobo:      45_000,
		Currency:        "NGN",
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Status != "open" || checkout.PayeeName != merchant.Name {
		t.Fatalf("unexpected checkout: %+v", checkout)
	}

	payment, err := payments.ResolveCheckout(ctx, checkout, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if payment.Channel != "checkout" {
		t.Fatalf("expected checkout channel, got %q", payment.Channel)
	}
	if payment.MerchantID != merchant.ID {
		t.Fatalf("payment bound to wrong merchant: %s", payment.MerchantID)
	}
	if payment.AmountKobo != 45_000 || payment.Currency != "NGN" {
		t.Fatalf("payment amount mismatch: %+v", payment.Payment)
	}

	linked, err := repository.CheckoutByToken(ctx, checkout.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !linked.PaymentID.Valid || linked.PaymentID.UUID != payment.ID {
		t.Fatalf("checkout not linked to resolved payment: %+v", linked)
	}

	// Resolution is idempotent: reopening the link returns the same active
	// payment instead of minting a second one.
	again, err := payments.ResolveCheckout(ctx, linked, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != payment.ID {
		t.Fatalf("expected the same payment on re-resolve, got %s (was %s)", again.ID, payment.ID)
	}

	// After the attempt reaches a terminal failed state, resolving again mints
	// a fresh payment and re-links the checkout.
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusFailed, "test", nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := payments.ResolveCheckout(ctx, linked, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == payment.ID {
		t.Fatal("expected a fresh payment after the previous attempt failed")
	}
	relinked, err := repository.CheckoutByToken(ctx, checkout.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !relinked.PaymentID.Valid || relinked.PaymentID.UUID != fresh.ID {
		t.Fatalf("checkout not re-linked to the fresh payment: %+v", relinked)
	}

	if _, err := payments.ResolveCheckout(ctx, checkout, "0801"); err == nil {
		t.Fatal("short payer number should be rejected")
	}
}
