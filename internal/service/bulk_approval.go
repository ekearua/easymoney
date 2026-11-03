// Bulk payment approval: batches are built from a filtered recipient list by
// the payments role, then released. Approval re-runs the affordability and
// capacity guards so a draft that went stale cannot start paying, and the
// merchant's location setting decides whether the batch runs the location
// window (stage 1) or pays the matched recipients directly.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// ErrBulkNoRecipients blocks a draft whose filter matches nothing payable.
var ErrBulkNoRecipients = errors.New("no verified recipients match this filter")

// ErrBulkBankPayoutUnsupported refuses to release a bank-mode batch that can
// never reach a bank rail: no payout provider is configured, or the batch pays
// into restricted program purses, which are wallet destinations by definition.
var ErrBulkBankPayoutUnsupported = errors.New("this batch cannot use bank payout")

// DraftFromFilter validates a filtered selection and snapshots it into a draft
// batch. Nothing is paid here, and a draft is never refused for lack of funds:
// the wallet balance is checked at approval and again item by item as money
// moves, so the payments team can prepare work ahead of funding.
func (s *BulkPaymentsService) DraftFromFilter(ctx context.Context, merchantID uuid.UUID, filter store.RecipientFilter, amountKobo int64, createdBy *uuid.UUID) (store.BulkBatch, error) {
	if amountKobo <= 0 {
		return store.BulkBatch{}, errors.New("amount must be positive")
	}
	settings, err := s.store.MerchantBulkSettings(ctx, merchantID)
	if err != nil {
		return store.BulkBatch{}, err
	}
	filter.Verification = store.RecipientVerified
	targets, err := s.store.CountBulkRecipients(ctx, merchantID, filter)
	if err != nil {
		return store.BulkBatch{}, err
	}
	if targets == 0 {
		return store.BulkBatch{}, ErrBulkNoRecipients
	}
	if s.cfg.MaxTargets > 0 && targets > s.cfg.MaxTargets {
		return store.BulkBatch{}, fmt.Errorf("%w: %d > %d", ErrBulkTooManyTargets, targets, s.cfg.MaxTargets)
	}
	expected := int64(targets) * amountKobo

	batch, err := s.store.CreateBulkDraft(ctx, store.NewBulkDraft{
		MerchantID:      merchantID,
		AmountKobo:      amountKobo,
		Currency:        "NGN",
		TTLSeconds:      int(s.cfg.LocationTTL.Seconds()),
		CreatedByMember: createdBy,
		PayoutMode:      settings.PayoutMode,
		RequireLocation: settings.LocationRequired,
		Filter:          filter,
	})
	if err != nil {
		return store.BulkBatch{}, err
	}
	if s.exceedsKYBLimit(ctx, merchantID, expected) {
		if err := s.store.SetBulkBatchComplianceFlag(ctx, batch.ID, true); err != nil {
			s.logger.WarnContext(ctx, "bulk compliance flag failed", "batch", batch.BatchNo, "error", err)
		}
		batch.ComplianceFlag = true
	}
	return batch, nil
}

// SubmitDraft puts a draft into the approval queue.
func (s *BulkPaymentsService) SubmitDraft(ctx context.Context, merchantID, batchID, memberID uuid.UUID) error {
	return s.store.SubmitBulkBatch(ctx, merchantID, batchID, memberID)
}

// ApproveDraft re-checks the guards and releases the batch. Wallet batches
// enqueue either the location stage or a direct payment run.
func (s *BulkPaymentsService) ApproveDraft(ctx context.Context, merchantID, batchID, memberID uuid.UUID) error {
	approval, err := s.store.BulkBatchApprovalInfo(ctx, merchantID, batchID)
	if err != nil {
		return err
	}
	switch approval.Status {
	case store.BulkBatchDraft, store.BulkBatchSubmitted:
	default:
		return fmt.Errorf("batch %s is %s and cannot be approved", approval.BatchNo, approval.Status)
	}
	if approval.TotalTargets == 0 {
		return ErrBulkNoRecipients
	}
	if s.cfg.MaxTargets > 0 && approval.TotalTargets > s.cfg.MaxTargets {
		return fmt.Errorf("%w: %d > %d", ErrBulkTooManyTargets, approval.TotalTargets, s.cfg.MaxTargets)
	}
	expected := int64(approval.TotalTargets) * approval.AmountKobo
	balance, _, err := s.store.BusinessWalletBalance(ctx, merchantID)
	if err != nil {
		return err
	}
	if balance < expected {
		return fmt.Errorf("%w: have %d kobo, need %d kobo", ErrBulkInsufficientFunds, balance, expected)
	}
	if approval.PayoutMode == "bank" {
		// The batch is already locked to a rail it may not be able to use, so
		// refuse now with a reason instead of paying the wrong destination.
		if s.payout == nil {
			return fmt.Errorf("%w: no bank payout provider is configured", ErrBulkBankPayoutUnsupported)
		}
		if _, isProgram, perr := s.store.ProgramForBulkBatch(ctx, batchID); perr != nil {
			return perr
		} else if isProgram {
			return fmt.Errorf("%w: program batches pay into restricted purses, not bank accounts", ErrBulkBankPayoutUnsupported)
		}
	}
	if err := s.store.ApproveBulkBatch(ctx, merchantID, batchID, memberID); err != nil {
		return err
	}
	kind := store.BulkJobStage1
	delay := time.Duration(0)
	if !approval.RequireLocation {
		kind = store.BulkJobPay
	}
	if err := s.store.EnqueueBulkJob(ctx, kind, batchID, delay); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "bulk batch approved", "batch", approval.BatchNo, "job", kind,
		"recipients", approval.TotalTargets, "location_required", approval.RequireLocation)
	return nil
}

// RejectDraft refuses a batch and records why.
func (s *BulkPaymentsService) RejectDraft(ctx context.Context, merchantID, batchID, memberID uuid.UUID, reason string) error {
	return s.store.RejectBulkBatch(ctx, merchantID, batchID, memberID, reason)
}

func (s *BulkPaymentsService) payBulkItemBank(ctx context.Context, batch store.BulkBatch, item store.BulkBatchItem) error {
	if s.payout == nil {
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "payout_unavailable", "bank payout not configured")
		return errors.New("bank payout not configured")
	}
	var centerUserID uuid.UUID
	if item.CenterUserID != nil {
		centerUserID = *item.CenterUserID
	} else {
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "missing_recipient", "recipient details missing")
		return errors.New("missing recipient")
	}
	recipient, err := s.store.BulkRecipientByID(ctx, batch.MerchantID, centerUserID)
	if err != nil {
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "recipient_not_found", err.Error())
		return err
	}
	if strings.TrimSpace(recipient.AccountNumber) == "" || strings.TrimSpace(recipient.BankCode) == "" {
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "missing_bank_details", "recipient missing bank account details")
		return errors.New("missing bank details")
	}
	// Check funding before the transfer: a provider payout that has left cannot
	// be recalled, so an unfunded merchant must never reach the rail.
	balance, hasWallet, err := s.store.BusinessWalletBalance(ctx, batch.MerchantID)
	if err != nil {
		return err
	}
	if !hasWallet || balance < item.AmountKobo {
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "insufficient_funds", "business wallet cannot cover this item")
		return fmt.Errorf("%w: business wallet has %d kobo, need %d kobo", store.ErrInsufficientWalletBalance, balance, item.AmountKobo)
	}
	accountName := strings.TrimSpace(recipient.AccountName)
	if accountName == "" && s.nameEnquirer != nil {
		name, nerr := s.nameEnquirer.NameEnquiry(ctx, recipient.AccountNumber, recipient.BankCode)
		if nerr == nil && strings.TrimSpace(name) != "" {
			accountName = strings.TrimSpace(name)
		}
	}
	if accountName == "" {
		accountName = MaskedAccountName(recipient.AccountNumber)
	}
	transferRef := fmt.Sprintf("bulk:%s:%s", batch.BatchNo, item.ID)
	res, err := s.payout.Payout(ctx, ports.PayoutRequest{
		Reference:     transferRef,
		AccountName:   accountName,
		AccountNumber: recipient.AccountNumber,
		BankCode:      recipient.BankCode,
		AmountKobo:    item.AmountKobo,
		Currency:      "NGN",
	})
	if err != nil {
		if errors.Is(err, ports.ErrPayoutPending) {
			// Leave as payable/pending for reconciliation; don't mark failed
			return nil
		}
		_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "payout_failed", err.Error())
		return err
	}
	if res.Status == "succeeded" {
		if _, perr := s.store.PostBulkPaymentItemBank(ctx, item.ID, res.ExternalRef); perr != nil {
			// The money is already gone: record loudly so the transfer is
			// reconciled against the merchant's wallet rather than retried.
			s.logger.ErrorContext(ctx, "bulk bank transfer succeeded but settlement failed",
				"batch", batch.BatchNo, "item", item.ID, "transfer_ref", res.ExternalRef, "error", perr)
			return perr
		}
		return nil
	}
	_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "payout_failed", res.Message)
	return errors.New(res.Message)
}

// RunDirectPayment pays the verified recipients of a batch whose merchant has
// location verification switched off. Every successful item is still its own
// balanced wallet pair, so the merchant only pays for what lands.
func (s *BulkPaymentsService) RunDirectPayment(ctx context.Context, batchID uuid.UUID) error {
	batch, err := s.store.BulkBatchByIDAny(ctx, batchID)
	if err != nil {
		return err
	}
	if err := s.store.MarkBulkBatchStage2(ctx, batchID); err != nil {
		return err
	}
	if _, err := s.store.MarkDirectItemsPayable(ctx, batchID); err != nil {
		return err
	}
	// Determine payout mode
	approval, err := s.store.BulkBatchApprovalInfo(ctx, batch.MerchantID, batchID)
	payoutMode := approval.PayoutMode
	if payoutMode == "" {
		payoutMode = "wallet"
	}
	items, err := s.store.ListBulkBatchItems(ctx, batchID, store.BulkItemLocationReceived, s.cfg.ChunkSize)
	if err != nil {
		return err
	}
	var transient error
	if payoutMode == "bank" {
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := s.payBulkItemBank(ctx, batch, item); err != nil {
				if errors.Is(err, store.ErrInsufficientWalletBalance) {
					_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "insufficient_funds", err.Error())
					continue
				}
				transient = err
			}
		}
	} else {
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			userID := item.UserID
			if userID == nil {
				user, ferr := s.store.FindUserByWhatsAppNumber(ctx, item.PhoneE164)
				if ferr != nil {
					continue
				}
				if user.ID == uuid.Nil {
					_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemNotRegistered, "not_registered", "recipient has no Xego account")
					continue
				}
				if err := s.store.SetBulkItemUserID(ctx, item.ID, user.ID); err != nil {
					continue
				}
				userID = &user.ID
			}
			profile, perr := s.store.KYCProfileByUser(ctx, *userID)
			if perr != nil || kyc.Order(profile.Tier) < kyc.Order(kyc.TierL2) {
				_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemKYCIneligible, "kyc_ineligible", "recipient has not reached Tier 2")
				continue
			}
			if _, postErr := s.store.PostBulkPaymentItem(ctx, item.ID); postErr != nil {
				if errors.Is(postErr, store.ErrInsufficientWalletBalance) {
					_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "insufficient_funds", postErr.Error())
					continue
				}
				transient = postErr
			}
		}
	}
	finalized, err := s.store.FinalizeBulkBatch(ctx, batchID, "")
	if err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "bulk direct batch finalised", "batch", batch.BatchNo, "status", finalized.Status,
		"paid", finalized.PaidCount, "failed", finalized.FailedCount)
	return transient
}
