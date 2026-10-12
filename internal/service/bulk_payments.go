// Bulk payments: location-aware disbursements to a merchant's recipients.
//
// A batch is a two-stage one-off job. Stage 1 asks each recipient to share
// their current location over WhatsApp/Telegram; stage 2 runs at the TTL
// boundary, pays everyone who responded and is eligible (Tier-2 KYC, within the
// center radius, acceptable accuracy), then finalises the batch. Every
// successful payment is a balanced ledger pair (debit the merchant's business
// wallet / credit the recipient's wallet), so the merchant is only debited for
// payments that actually land. A batch that cannot be fully covered by the
// wallet is blocked before it starts.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/geo"
	"whatsapp-payment-demo/internal/store"
)

// BulkPaymentsConfig tunes the bulk-payment orchestrator.
type BulkPaymentsConfig struct {
	RadiusM         int
	MaxAccuracyM    float64
	ChunkSize       int
	SendRatePerSec  int
	MaxTargets      int
	Stage1Template  string
	BaseURL         string
	LocationTTL     time.Duration
	SendMessage     string
	InviteMessage   string
	LocationURLPath string
}

// BulkPaymentsService orchestrates the two-stage bulk-payment lifecycle.
type BulkPaymentsService struct {
	store      *store.Store
	messengers map[string]ports.Messenger
	logger     *slog.Logger
	cfg        BulkPaymentsConfig
}

// ErrBulkInsufficientFunds blocks a batch the merchant cannot cover.
var ErrBulkInsufficientFunds = errors.New("business wallet balance cannot cover the batch")

// ErrBulkTooManyTargets blocks a batch larger than the configured ceiling.
var ErrBulkTooManyTargets = errors.New("bulk batch exceeds the maximum number of recipients")

// NewBulkPaymentsService constructs the orchestrator.
func NewBulkPaymentsService(repository *store.Store, messengers map[string]ports.Messenger, logger *slog.Logger, cfg BulkPaymentsConfig) *BulkPaymentsService {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RadiusM <= 0 {
		cfg.RadiusM = 50
	}
	if cfg.MaxAccuracyM <= 0 {
		cfg.MaxAccuracyM = 50
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 500
	}
	if cfg.SendRatePerSec <= 0 {
		cfg.SendRatePerSec = 20
	}
	if cfg.LocationTTL <= 0 {
		cfg.LocationTTL = 5 * time.Minute
	}
	if cfg.SendMessage == "" {
		cfg.SendMessage = "Share your current location to receive a payment. This link is valid for a few minutes."
	}
	if cfg.InviteMessage == "" {
		cfg.InviteMessage = "You have a payment waiting. Create or open your Xego account and share your location to receive it."
	}
	return &BulkPaymentsService{store: repository, messengers: messengers, logger: logger, cfg: cfg}
}

// Config exposes the tuned config (the app uses the rate/worker knobs).
func (s *BulkPaymentsService) Config() BulkPaymentsConfig { return s.cfg }

// CreateBatch validates affordability and capacity, snapshots the center's
// recipients, records the stage-1 job, and flags the batch when its total
// exceeds the merchant's KYB payout ceiling (flag-only).
func (s *BulkPaymentsService) CreateBatch(ctx context.Context, merchantID, centerID uuid.UUID, amountKobo int64, createdBy uuid.UUID) (store.BulkBatch, error) {
	if amountKobo <= 0 {
		return store.BulkBatch{}, errors.New("amount must be positive")
	}
	center, err := s.store.MerchantCenterByID(ctx, merchantID, centerID)
	if err != nil {
		return store.BulkBatch{}, fmt.Errorf("center: %w", err)
	}
	targets, err := s.store.CountCenterUsers(ctx, center.ID)
	if err != nil {
		return store.BulkBatch{}, err
	}
	if targets == 0 {
		return store.BulkBatch{}, errors.New("center has no recipients")
	}
	if s.cfg.MaxTargets > 0 && targets > s.cfg.MaxTargets {
		return store.BulkBatch{}, fmt.Errorf("%w: %d > %d", ErrBulkTooManyTargets, targets, s.cfg.MaxTargets)
	}
	expected := int64(targets) * amountKobo
	balance, _, err := s.store.BusinessWalletBalance(ctx, merchantID)
	if err != nil {
		return store.BulkBatch{}, err
	}
	if balance < expected {
		return store.BulkBatch{}, fmt.Errorf("%w: have %d kobo, need %d kobo", ErrBulkInsufficientFunds, balance, expected)
	}

	batch, err := s.store.CreateBulkBatch(ctx, store.NewBulkBatch{
		MerchantID: merchantID,
		CenterID:   center.ID,
		AmountKobo: amountKobo,
		Currency:   "NGN",
		TTLSeconds: int(s.cfg.LocationTTL.Seconds()),
		CreatedBy:  createdBy,
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

// exceedsKYBLimit reports whether a batch total breaches the merchant's KYB
// payout single ceiling. Failures are non-fatal: an unknown ceiling never
// blocks, and the flag exists only so compliance can follow up.
func (s *BulkPaymentsService) exceedsKYBLimit(ctx context.Context, merchantID uuid.UUID, totalKobo int64) bool {
	profile, err := s.store.KYBProfileByMerchant(ctx, merchantID)
	if err != nil {
		return false
	}
	limits, err := s.store.TierLimits(ctx, store.AccountBusiness, profile.Tier, kyc.DirOut)
	if err != nil {
		return false
	}
	return limits.SingleLimitKobo > 0 && totalKobo > limits.SingleLimitKobo
}

// RunStage1 sends the location request to every pending recipient, throttled to
// the configured rate, then schedules stage 2 at the TTL boundary. Pending
// items are drained in bounded chunks so a large batch never loads every row at
// once; a persistently failing send is retried up to three times before it is
// marked failed, so the drain always terminates.
func (s *BulkPaymentsService) RunStage1(ctx context.Context, batchID uuid.UUID) error {
	batch, err := s.store.BulkBatchByIDAny(ctx, batchID)
	if err != nil {
		return err
	}
	interval := time.Duration(0)
	if s.cfg.SendRatePerSec > 0 {
		interval = time.Second / time.Duration(s.cfg.SendRatePerSec)
	}
	requested := 0
	for {
		items, err := s.store.PendingBulkItems(ctx, batchID, s.cfg.ChunkSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			channel, to, registered := s.resolveChannel(ctx, item.PhoneE164)
			messenger := s.messengers[channel]
			if messenger == nil {
				messenger = s.messengers[ChannelWhatsApp]
				channel = ChannelWhatsApp
			}
			if messenger == nil {
				_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "no_messenger", "no messenger for channel "+channel)
				continue
			}
			sendErr := s.deliverStage1(ctx, messenger, channel, item.ID, to, registered)
			switch {
			case sendErr != nil:
				s.logger.WarnContext(ctx, "bulk location request failed", "batch", batch.BatchNo, "phone", item.PhoneE164, "error", sendErr)
				_ = s.store.RecordBulkItemSendFailure(ctx, item.ID, sendErr.Error())
				if item.Attempts+1 >= 3 {
					_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "send_failed", sendErr.Error())
				}
			case !registered:
				_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemNotRegistered, "not_registered", "invite sent to a recipient without an Xego account")
			default:
				if err := s.store.MarkBulkItemRequested(ctx, item.ID, channel); err != nil {
					return err
				}
				requested++
			}
			if interval > 0 {
				time.Sleep(interval)
			}
		}
	}
	due := time.Now().Add(s.cfg.LocationTTL)
	if err := s.store.MarkBulkBatchStage1(ctx, batchID, requested, due); err != nil {
		return err
	}
	return s.store.ScheduleBulkStage2(ctx, batchID, due)
}

// resolveChannel picks the delivery channel and destination for a phone number.
// A number with no Xego account is never registered; a Telegram-linked user is
// reached on Telegram, everyone else on WhatsApp.
func (s *BulkPaymentsService) resolveChannel(ctx context.Context, phone string) (string, string, bool) {
	user, err := s.store.FindUserByWhatsAppNumber(ctx, phone)
	if err != nil || user.ID == uuid.Nil {
		return ChannelWhatsApp, phone, false
	}
	if user.TelegramChatID.Valid && user.TelegramChatID.String != "" {
		return ChannelTelegram, user.TelegramChatID.String, true
	}
	return ChannelWhatsApp, phone, true
}

// deliverStage1 sends the location request (registered recipients) or the
// signup invite (everyone else). When the native location affordance fails on
// WhatsApp, it falls back to a link to the browser-geolocation page.
func (s *BulkPaymentsService) deliverStage1(ctx context.Context, messenger ports.Messenger, channel string, itemID uuid.UUID, to string, registered bool) error {
	if !registered {
		if s.cfg.Stage1Template != "" && channel == ChannelWhatsApp {
			return messenger.SendTemplate(ctx, to, s.cfg.Stage1Template, []string{s.cfg.InviteMessage})
		}
		return messenger.SendText(ctx, to, s.cfg.InviteMessage)
	}
	err := messenger.SendLocationRequest(ctx, to, s.cfg.SendMessage)
	if err == nil || channel != ChannelWhatsApp || s.cfg.BaseURL == "" {
		return err
	}
	link := s.LocationURL() + "?id=" + itemID.String()
	if linkErr := messenger.SendLink(ctx, to, s.cfg.SendMessage, link, "Share location"); linkErr != nil {
		return err
	}
	return nil
}

// RunStage2 expires non-responders, then pays each eligible responder. It is
// safe to replay: already-paid items are no-ops and unpaid prior failures are
// retried.
func (s *BulkPaymentsService) RunStage2(ctx context.Context, batchID uuid.UUID) error {
	batch, err := s.store.BulkBatchByIDAny(ctx, batchID)
	if err != nil {
		return err
	}
	if err := s.store.MarkBulkBatchStage2(ctx, batchID); err != nil {
		return err
	}
	if _, err := s.store.ExpireUnrespondedBulkItems(ctx, batchID); err != nil {
		return err
	}
	items, err := s.store.ListBulkBatchItems(ctx, batchID, store.BulkItemLocationReceived, s.cfg.MaxTargets)
	if err != nil {
		return err
	}
	// P1: a program batch pays into each recipient's restricted purse instead of
	// their ordinary wallet. Resolved once for the whole batch so a single
	// disbursement can never mix the two destinations.
	var isProgramBatch bool
	if _, isProgram, perr := s.store.ProgramForBulkBatch(ctx, batchID); perr != nil {
		return perr
	} else {
		isProgramBatch = isProgram
	}
	var transient error
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
		var postErr error
		if isProgramBatch {
			_, postErr = s.store.PostBulkProgramPaymentItem(ctx, item.ID)
		} else {
			_, postErr = s.store.PostBulkPaymentItem(ctx, item.ID)
		}
		if postErr != nil {
			switch {
			case errors.Is(postErr, store.ErrInsufficientWalletBalance):
				_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "insufficient_funds", postErr.Error())
			case isProgramBatch && (errors.Is(postErr, store.ErrProgramNotEnrolled) ||
				errors.Is(postErr, store.ErrProgramNotActive) ||
				errors.Is(postErr, store.ErrPurseNotActive) || errors.Is(postErr, store.ErrPurseExpired)):
				// The recipient has no live purse for this program (not enrolled, or
				// the purse already lapsed). Paying them anyway would put restricted
				// money into a withdrawable balance, so the item is refused instead.
				_ = s.store.SetBulkItemStatus(ctx, item.ID, store.BulkItemFailed, "program_ineligible", postErr.Error())
			default:
				transient = postErr
			}
			continue
		}
	}
	finalized, err := s.store.FinalizeBulkBatch(ctx, batchID, "")
	if err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "bulk batch finalised", "batch", batch.BatchNo, "status", finalized.Status,
		"paid", finalized.PaidCount, "failed", finalized.FailedCount, "expired", finalized.ExpiredCount)
	return transient
}

// RecordLocation matches an inbound location reply to the most recent open
// request from that phone, scores it against the center, and stores the
// outcome. found is false when no request is open.
func (s *BulkPaymentsService) RecordLocation(ctx context.Context, phone string, lat, lng, accuracy float64) (bool, error) {
	phone = domain.CanonicalE164Phone(phone)
	target, found, err := s.store.FindOpenBulkItemByPhone(ctx, phone)
	if err != nil || !found {
		return false, err
	}
	return true, s.scoreLocation(ctx, target, lat, lng, accuracy)
}

// RecordLocationForItem records a fallback web-link capture for a known item.
func (s *BulkPaymentsService) RecordLocationForItem(ctx context.Context, itemID uuid.UUID, lat, lng, accuracy float64) (bool, error) {
	target, found, err := s.store.FindOpenBulkItemByID(ctx, itemID)
	if err != nil || !found {
		return false, err
	}
	return true, s.scoreLocation(ctx, target, lat, lng, accuracy)
}

// scoreLocation expires, scores, and stores a captured position.
func (s *BulkPaymentsService) scoreLocation(ctx context.Context, target store.OpenBulkLocationTarget, lat, lng, accuracy float64) error {
	if time.Since(target.Stage1At) > time.Duration(target.TTLSeconds)*time.Second {
		_ = s.store.SetBulkItemStatus(ctx, target.ItemID, store.BulkItemExpired, "expired", "location arrived after the response window")
		return nil
	}
	distance := geo.DistanceMeters(target.CenterLat, target.CenterLng, lat, lng)
	status := store.BulkItemLocationReceived
	switch {
	case accuracy > s.cfg.MaxAccuracyM:
		status = store.BulkItemPoorAccuracy
	case distance > float64(target.RadiusM):
		status = store.BulkItemOutOfRange
	}
	return s.store.SetBulkItemLocation(ctx, target.ItemID, lat, lng, accuracy, distance, status)
}

// LocationURL builds the browser fallback capture link for a recipient.
func (s *BulkPaymentsService) LocationURL() string {
	path := s.cfg.LocationURLPath
	if path == "" {
		path = "/bulk/loc"
	}
	return s.cfg.BaseURL + path
}
