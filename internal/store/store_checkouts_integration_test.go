package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

func TestCheckouts(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE checkouts,merchant_webhook_deliveries,payments,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}

	// Minting a link resolves its own token and returns merchant display fields.
	view, err := repository.CreateCheckout(ctx, CheckoutSpec{
		PayeeMerchantID: merchant.ID,
		Reference:       "LINK-1",
		Note:            "Lunch money",
		AmountKobo:      35_000,
		Currency:        "NGN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "open" || view.AmountKobo != 35_000 || view.PayeeName != merchant.Name || view.PayeeSlug != merchant.Slug {
		t.Fatalf("created checkout: %+v", view)
	}
	if len(view.Token) < 32 {
		t.Fatalf("checkout token too short: %q", view.Token)
	}

	// Replaying the same payee reference is idempotent.
	replay, err := repository.CreateCheckout(ctx, CheckoutSpec{
		PayeeMerchantID: merchant.ID,
		Reference:       "LINK-1",
		Note:            "Lunch money",
		AmountKobo:      35_000,
		Currency:        "NGN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != view.ID {
		t.Fatalf("replay produced a new checkout: %s vs %s", replay.ID, view.ID)
	}

	// Token and payee-reference lookups agree.
	byToken, err := repository.CheckoutByToken(ctx, view.Token)
	if err != nil {
		t.Fatal(err)
	}
	if byToken.ID != view.ID {
		t.Fatalf("token lookup mismatch: %+v", byToken)
	}
	byRef, err := repository.CheckoutByPayeeAndReference(ctx, merchant.ID, "LINK-1")
	if err != nil {
		t.Fatal(err)
	}
	if byRef.ID != view.ID {
		t.Fatalf("reference lookup mismatch: %+v", byRef)
	}

	// Linking a payment surfaces it on the view.
	user, err := repository.GetOrCreateUser(ctx, "+2348012340888")
	if err != nil {
		t.Fatal(err)
	}
	payment := domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 35_000,
		Currency: "NGN", Status: domain.StatusSucceeded, Provider: "paystack",
		ProviderReference: "ref-checkout-test", ReceiptToken: "ctok", Channel: "checkout",
	}
	if _, err := repository.CreatePayment(ctx, payment); err != nil {
		t.Fatal(err)
	}
	if err := repository.LinkCheckoutPayment(ctx, view.ID, payment.ID); err != nil {
		t.Fatal(err)
	}
	linked, err := repository.CheckoutByToken(ctx, view.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !linked.PaymentID.Valid || linked.PaymentID.UUID != payment.ID {
		t.Fatalf("payment not linked: %+v", linked)
	}

	// Terminal success marks the checkout paid.
	if err := repository.MarkCheckoutsPaidByPayment(ctx, payment.ID); err != nil {
		t.Fatal(err)
	}
	paid, err := repository.CheckoutByToken(ctx, view.Token)
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != "paid" || paid.PaymentStatus != string(domain.StatusSucceeded) {
		t.Fatalf("expected paid checkout: %+v", paid)
	}

	// Past expiry closes open checkouts.
	expiring, err := repository.CreateCheckout(ctx, CheckoutSpec{
		PayeeMerchantID: merchant.ID,
		Reference:       "LINK-EXPIRE",
		Note:            "",
		AmountKobo:      5_000,
		Currency:        "NGN",
		ExpiresAt:       ptrTime(time.Now().Add(-time.Minute)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ExpireCheckouts(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	expired, err := repository.CheckoutByToken(ctx, expiring.Token)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != "expired" {
		t.Fatalf("expected expired checkout: %+v", expired)
	}
}
