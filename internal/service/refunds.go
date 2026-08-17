package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// RefundService orchestrates refund creation and provider dispatch.
type RefundService struct {
	store    *store.Store
	provider ports.RefundProvider
	logger   *slog.Logger
}

// NewRefundService returns a RefundService backed by the given store and
// provider. If provider is nil a simulated rail is used.
func NewRefundService(repository *store.Store, provider ports.RefundProvider, logger *slog.Logger) *RefundService {
	if provider == nil {
		provider = NewSimulatedRefundProvider()
	}
	return &RefundService{store: repository, provider: provider, logger: logger}
}

// Refund validates preconditions, dispatches the refund to the provider, and
// processes the result. The store handles ledger reversal and payment
// transition atomically.
func (s *RefundService) Refund(ctx context.Context, paymentID, reason string) (store.Refund, error) {
	pid := uuid.MustParse(paymentID)

	// 1. Create the refund and post the ledger reversal inside one transaction.
	ref, err := s.store.RefundPayment(ctx, pid, reason, "merchant", nil)
	if err != nil {
		return store.Refund{}, err
	}

	// 2. Dispatch to the provider.
	payment, err := s.store.PaymentByID(ctx, pid)
	if err != nil {
		return ref, fmt.Errorf("load payment for provider call: %w", err)
	}
	result, err := s.provider.Refund(ctx, ports.RefundRequest{
		PaymentID:  payment.ProviderReference,
		AmountKobo: ref.AmountKobo,
		Currency:   payment.Currency,
	})
	if err != nil {
		_ = s.store.FailRefund(ctx, ref.ID, err.Error())
		return ref, fmt.Errorf("provider refund: %w", err)
	}

	// 3. Provider declined.
	if result.Status != "succeeded" {
		message := result.Message
		if message == "" {
			message = "provider declined the refund"
		}
		_ = s.store.FailRefund(ctx, ref.ID, message)
		return s.store.RefundByID(ctx, ref.ID)
	}

	// 4. Provider accepted — complete the refund.
	if err := s.store.CompleteRefund(ctx, ref.ID, result.RefundID); err != nil {
		return ref, err
	}
	s.logger.Info("refund completed", "payment_id", paymentID, "provider_refund_id", result.RefundID)
	return s.store.RefundByID(ctx, ref.ID)
}

// RefundByID returns one refund by its id.
func (s *RefundService) RefundByID(ctx context.Context, refundID string) (store.Refund, error) {
	return s.store.RefundByID(ctx, uuid.MustParse(refundID))
}

// ListRefunds returns a merchant's refunds.
func (s *RefundService) ListRefunds(ctx context.Context, merchantID string, limit int) ([]store.Refund, error) {
	return s.store.ListRefunds(ctx, uuid.MustParse(merchantID), limit)
}

// ListAllRefunds returns all refunds (admin).
func (s *RefundService) ListAllRefunds(ctx context.Context, limit int) ([]store.Refund, error) {
	return s.store.ListAllRefunds(ctx, limit)
}
