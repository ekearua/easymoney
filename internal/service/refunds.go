package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/refund"
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
		provider = refund.NewSimulatedProvider()
	}
	return &RefundService{store: repository, provider: provider, logger: logger}
}

// Refund creates a refund request pending admin approval (maker-checker).
// The actual provider dispatch happens only after ApproveRefund is called.
func (s *RefundService) Refund(ctx context.Context, paymentID, reason string) (store.Refund, error) {
	pid := uuid.MustParse(paymentID)
	ref, err := s.store.RequestRefund(ctx, pid, reason, "merchant", nil)
	if err != nil {
		return store.Refund{}, err
	}
	s.logger.Info("refund requested (pending approval)", "payment_id", paymentID, "refund_id", ref.ID)
	return ref, nil
}

// ApproveRefund approves a pending refund request and dispatches to the provider.
func (s *RefundService) ApproveRefund(ctx context.Context, refundID string, approvedBy uuid.UUID, postedBy string) (store.Refund, error) {
	rid := uuid.MustParse(refundID)
	ref, err := s.store.ApproveRefund(ctx, rid, approvedBy, postedBy)
	if err != nil {
		return store.Refund{}, err
	}

	// Dispatch to the provider.
	payment, err := s.store.PaymentByID(ctx, ref.PaymentID)
	if err != nil {
		return ref, fmt.Errorf("load payment for provider call: %w", err)
	}
	result, err := s.provider.Refund(ctx, ports.RefundRequest{
		PaymentID:         payment.ProviderReference,
		PaymentAmountKobo: payment.AmountKobo,
		AmountKobo:        ref.AmountKobo,
		Currency:          payment.Currency,
	})
	if err != nil {
		_ = s.store.FailRefund(ctx, ref.ID, err.Error())
		return ref, fmt.Errorf("provider refund: %w", err)
	}

	// Provider declined.
	if result.Status != "succeeded" {
		message := result.Message
		if message == "" {
			message = "provider declined the refund"
		}
		_ = s.store.FailRefund(ctx, ref.ID, message)
		return s.store.RefundByID(ctx, ref.ID)
	}

	// Provider accepted — complete the refund.
	if err := s.store.CompleteRefund(ctx, ref.ID, result.RefundID); err != nil {
		return ref, err
	}
	s.logger.Info("refund completed", "refund_id", refundID, "provider_refund_id", result.RefundID)
	return s.store.RefundByID(ctx, ref.ID)
}

// RejectRefund rejects a pending refund request.
func (s *RefundService) RejectRefund(ctx context.Context, refundID string, rejectedBy uuid.UUID, reason string) error {
	return s.store.RejectRefund(ctx, uuid.MustParse(refundID), rejectedBy, reason)
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
