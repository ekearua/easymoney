package service

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

func TestRefundServiceRefund(t *testing.T) {
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
	user, err := repository.GetOrCreateUser(ctx, "+2348012348801")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 200_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: "refund-svc-ref", ReceiptToken: "tok-refund-svc",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.PaymentStatus{domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusSucceeded} {
		if _, err := repository.TransitionPayment(ctx, payment.ID, status, "test", nil); err != nil {
			t.Fatalf("transition %s: %v", status, err)
		}
	}

	svc := NewRefundService(repository, nil, testLogger())
	refund, err := svc.Refund(ctx, payment.ID.String(), "test refund")
	if err != nil {
		t.Fatalf("refund request: %v", err)
	}
	if refund.ApprovalStatus != "pending_approval" {
		t.Fatalf("expected pending_approval, got %s", refund.ApprovalStatus)
	}
	if refund.AmountKobo != 200_000 {
		t.Fatalf("expected 200000, got %d", refund.AmountKobo)
	}

	// Approve the refund (maker-checker) — create an admin user first.
	if err := repository.EnsureAdminUser(ctx, "ops@xego.local", "x"); err != nil {
		t.Fatal(err)
	}
	admin, err := repository.AdminUserByEmail(ctx, "ops@xego.local")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := svc.ApproveRefund(ctx, refund.ID.String(), admin.ID, "admin.approve")
	if err != nil {
		t.Fatalf("approve refund: %v", err)
	}
	if approved.Status != store.RefundSucceeded {
		t.Fatalf("expected succeeded refund after approval, got %s", approved.Status)
	}
	if approved.ApprovalStatus != "approved" {
		t.Fatalf("expected approval_status=approved, got %s", approved.ApprovalStatus)
	}

	// Payment is now refunded.
	p, err := repository.PaymentByID(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusRefunded {
		t.Fatalf("expected refunded payment, got %s", p.Status)
	}
}

func TestRefundServiceIdempotent(t *testing.T) {
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
	user, err := repository.GetOrCreateUser(ctx, "+2348012348801")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 100_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: "refund-idemp-ref", ReceiptToken: "tok-refund-idemp",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.PaymentStatus{domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusSucceeded} {
		if _, err := repository.TransitionPayment(ctx, payment.ID, status, "test", nil); err != nil {
			t.Fatalf("transition %s: %v", status, err)
		}
	}

	svc := NewRefundService(repository, nil, testLogger())
	if _, err := svc.Refund(ctx, payment.ID.String(), "first refund"); err != nil {
		t.Fatal(err)
	}
	// Second refund should fail.
	_, err = svc.Refund(ctx, payment.ID.String(), "second refund")
	if err == nil {
		t.Fatal("expected error for duplicate refund")
	}
}
