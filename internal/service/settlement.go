// S1: settlement orchestration. Cutting a batch moves a merchant's accrued
// 3100 merchant_payable into 3200 settlement_payable (store.CutSettlement),
// then the payout dispatches funds to the merchant's verified destination
// through the PayoutProvider. Provider calls carry the batch number as the
// idempotency key, so a redelivered payout request settles exactly once.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// SettlementService orchestrates the settlement lifecycle for merchants and
// the dispatch worker.
type SettlementService struct {
	store    *store.Store
	provider ports.PayoutProvider
	feeBps   int
	logger   *slog.Logger

	payoutMinKobo      int64
	payoutMaxKobo      int64
	payoutDailyCapKobo int64
	payoutDailyCount   int
}

// NewSettlementService constructs the settlement orchestrator. A nil provider
// defaults to the simulated rail. feeBps is the settlement fee in basis points
// (250 = 2.5 %) applied at batch-cut time.
func NewSettlementService(repository *store.Store, provider ports.PayoutProvider, logger *slog.Logger, feeBps int, opts ...SettlementOption) *SettlementService {
	if provider == nil {
		provider = NewSimulatedPayoutProvider()
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &SettlementService{store: repository, provider: provider, feeBps: feeBps, logger: logger}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SettlementOption configures optional settlement service parameters.
type SettlementOption func(*SettlementService)

func WithPayoutLimits(minKobo, maxKobo, dailyCapKobo int64, dailyCount int) SettlementOption {
	return func(s *SettlementService) {
		s.payoutMinKobo = minKobo
		s.payoutMaxKobo = maxKobo
		s.payoutDailyCapKobo = dailyCapKobo
		s.payoutDailyCount = dailyCount
	}
}

// Cut freezes the merchant's settled payable into a batch with the given
// external batch number. Replaying a known batch returns it unchanged.
func (s *SettlementService) Cut(ctx context.Context, merchantID uuid.UUID, batchNo string) (store.SettlementBatch, error) {
	return s.store.CutSettlement(ctx, merchantID, batchNo, time.Time{}, s.feeBps)
}

// RequestPayout settles a batch to the merchant's default destination, or to
// the explicitly supplied destination when given. It returns the payout in its
// post-attempt state.
func (s *SettlementService) RequestPayout(ctx context.Context, batchNo string, destinationID *uuid.UUID) (store.Payout, error) {
	batch, err := s.store.SettlementBatchByBatchNo(ctx, batchNo)
	if err != nil {
		return store.Payout{}, err
	}
	if batch.Status == store.SettlementBatchProcessed {
		payout, err := s.store.PayoutByBatchID(ctx, batch.ID)
		if err == nil {
			return payout, nil
		}
		return store.Payout{}, fmt.Errorf("batch %s processed without a payout", batchNo)
	}
	if batch.Status != store.SettlementBatchOpen && batch.Status != store.SettlementBatchScheduled {
		return store.Payout{}, fmt.Errorf("batch %s is not settleable (status %s)", batchNo, batch.Status)
	}
	var destination store.MerchantSettlementAccount
	if destinationID != nil {
		destination, err = s.store.SettlementAccountByID(ctx, *destinationID)
		if err != nil {
			return store.Payout{}, err
		}
		if destination.MerchantID != batch.MerchantID {
			return store.Payout{}, errors.New("settlement account belongs to another merchant")
		}
		if destination.Status != store.SettlementAccountActive {
			return store.Payout{}, errors.New("settlement account is not active")
		}
	} else {
		destination, err = s.store.DefaultSettlementAccount(ctx, batch.MerchantID)
		if err != nil {
			return store.Payout{}, err
		}
	}
	payout, err := s.store.CreatePayout(ctx, batch.ID, destination.ID)
	if err != nil {
		return store.Payout{}, err
	}
	if payout.Status == store.PayoutCompleted {
		return payout, nil
	}
	payoutAmount := batch.TotalKobo - batch.FeeKobo
	if s.payoutMinKobo > 0 && payoutAmount < s.payoutMinKobo {
		return store.Payout{}, fmt.Errorf("payout amount %d kobo is below minimum %d kobo", payoutAmount, s.payoutMinKobo)
	}
	if s.payoutMaxKobo > 0 && payoutAmount > s.payoutMaxKobo {
		return store.Payout{}, fmt.Errorf("payout amount %d kobo exceeds maximum %d kobo", payoutAmount, s.payoutMaxKobo)
	}
	dailyCount, dailyTotal, err := s.store.DailyPayoutStats(ctx, batch.MerchantID)
	if err != nil {
		return store.Payout{}, err
	}
	if s.payoutDailyCount > 0 && dailyCount >= s.payoutDailyCount {
		return store.Payout{}, fmt.Errorf("merchant has reached daily payout count limit (%d/%d)", dailyCount, s.payoutDailyCount)
	}
	if s.payoutDailyCapKobo > 0 && dailyTotal+payoutAmount > s.payoutDailyCapKobo {
		return store.Payout{}, fmt.Errorf("merchant would exceed daily payout cap (%d+%d > %d kobo)", dailyTotal, payoutAmount, s.payoutDailyCapKobo)
	}
	return s.dispatch(ctx, payout)
}

// dispatch leases the payout, calls the provider under the batch-number
// idempotency key, and records the outcome.
func (s *SettlementService) dispatch(ctx context.Context, payout store.Payout) (store.Payout, error) {
	claim, err := s.store.ClaimPayoutForDispatch(ctx, payout.BatchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.store.PayoutByID(ctx, payout.ID)
		}
		return store.Payout{}, err
	}
	req := ports.PayoutRequest{
		// The payout row id is the rail idempotency key: a retry of the same
		// payout replays its deterministic outcome, while a reversed payout
		// (a fresh row) gets a fresh reference and a fresh outcome.
		Reference:     claim.Payout.ID.String(),
		AccountName:   claim.Destination.AccountName,
		AccountNumber: claim.Destination.AccountNumber,
		BankCode:      claim.Destination.BankCode,
		AmountKobo:    claim.Payout.AmountKobo,
		Currency:      "NGN",
	}
	result, err := s.provider.Payout(ctx, req)
	if err != nil {
		if errors.Is(err, ports.ErrPayoutPending) {
			s.logger.Info("payout pending provider confirmation", "batch", claim.Batch.BatchNo)
			return s.store.PayoutByID(ctx, payout.ID)
		}
		s.logger.Error("payout provider call failed", "batch", claim.Batch.BatchNo, "error", err)
		_ = s.store.FailPayout(ctx, payout.ID, err.Error())
		return s.store.PayoutByID(ctx, payout.ID)
	}
	if result.Status != "succeeded" {
		message := result.Message
		if message == "" {
			message = "provider declined the payout"
		}
		s.logger.Warn("payout declined", "batch", claim.Batch.BatchNo, "message", message)
		_ = s.store.FailPayout(ctx, payout.ID, message)
		return s.store.PayoutByID(ctx, payout.ID)
	}
	if err := s.store.CompletePayout(ctx, payout.ID, result.ExternalRef); err != nil {
		return store.Payout{}, err
	}
	s.logger.Info("payout settled", "batch", claim.Batch.BatchNo, "external_ref", result.ExternalRef)
	return s.store.PayoutByID(ctx, payout.ID)
}

// DispatchPending settles every queued payout. It is called by the worker tick
// so operators and the CLI never have to trigger dispatch manually.
func (s *SettlementService) DispatchPending(ctx context.Context) error {
	queued, err := s.store.ListQueuedPayouts(ctx, 50)
	if err != nil {
		return err
	}
	for _, payout := range queued {
		if _, err := s.dispatch(ctx, payout); err != nil {
			s.logger.Error("payout dispatch failed", "payout_id", payout.ID, "error", err)
		}
	}
	return nil
}

// Retry re-queues a failed payout for another provider attempt.
func (s *SettlementService) Retry(ctx context.Context, payoutID uuid.UUID) error {
	return s.store.RetryPayout(ctx, payoutID)
}

// Reverse cancels a failed payout and reopens the batch for a fresh payout.
func (s *SettlementService) Reverse(ctx context.Context, payoutID uuid.UUID, reason string) error {
	return s.store.ReversePayout(ctx, payoutID, reason)
}
