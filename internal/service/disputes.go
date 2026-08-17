package service

import (
	"context"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// DisputeService manages dispute lifecycle operations.
type DisputeService struct {
	store *store.Store
}

// NewDisputeService returns a DisputeService backed by the given store.
func NewDisputeService(repository *store.Store) *DisputeService {
	return &DisputeService{store: repository}
}

// OpenDispute records a new dispute against a payment.
func (s *DisputeService) OpenDispute(ctx context.Context, paymentID, reason string) (store.Dispute, error) {
	return s.store.CreateDispute(ctx, uuid.MustParse(paymentID), reason)
}

// Resolve marks a dispute as won, lost, or expired.
func (s *DisputeService) Resolve(ctx context.Context, disputeID, outcome string) error {
	return s.store.ResolveDispute(ctx, uuid.MustParse(disputeID), outcome)
}

// DisputeByID returns one dispute by its id.
func (s *DisputeService) DisputeByID(ctx context.Context, disputeID string) (store.Dispute, error) {
	return s.store.DisputeByID(ctx, uuid.MustParse(disputeID))
}

// ListDisputes returns a merchant's disputes.
func (s *DisputeService) ListDisputes(ctx context.Context, merchantID string, limit int) ([]store.Dispute, error) {
	return s.store.ListDisputes(ctx, uuid.MustParse(merchantID), limit)
}

// ListAllDisputes returns all disputes (admin).
func (s *DisputeService) ListAllDisputes(ctx context.Context, limit int) ([]store.Dispute, error) {
	return s.store.ListAllDisputes(ctx, limit)
}
