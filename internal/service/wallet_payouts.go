// Wallet cash-out orchestration. A cash-out is a user-initiated NIP transfer
// from the customer's Xego wallet to a bank account the customer enters for
// that transfer (Interswitch name enquiry confirms the account holder first).
//
// Money-safe ordering: the allowance is reserved, the counterparty is screened,
// and only then does the store debit the wallet and record the row. If the
// provider ultimately fails, the worker reverses the debit back into the wallet.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// ErrWalletCashOutUnderReview is returned when the counterparty screen was
// ambiguous or blocked. The movement is refused but the allowance reservation
// stays in place (money held, not released), matching ScreenUserWithdrawal.
var ErrWalletCashOutUnderReview = errors.New("cash-out is under compliance review; contact support")

// ErrWalletCashOutUnavailable means no payout rail is configured.
var ErrWalletCashOutUnavailable = errors.New("wallet cash-out is not available right now")

// AccountNameEnquirer resolves the account holder's name for a bank account.
// Interswitch implements it; a nil enquirer falls back to a masked label so
// tests and local runs can still exercise the flow.
type AccountNameEnquirer interface {
	NameEnquiry(ctx context.Context, accountNumber, bankCode string) (string, error)
}

// WalletPayoutQuote is what the customer sees before confirming: the resolved
// account name, the NIP fee, and what actually lands in the bank.
type WalletPayoutQuote struct {
	AccountName string
	FeeKobo     int64
	PayoutKobo  int64
}

// WalletPayoutRequest is a confirmed cash-out instruction.
type WalletPayoutRequest struct {
	AmountKobo    int64
	BankCode      string
	BankName      string
	AccountNumber string
	AccountName   string
	DestinationID *uuid.UUID
}

// WalletPayoutService orchestrates customer cash-outs and their dispatch.
type WalletPayoutService struct {
	cfg          config.Config
	store        *store.Store
	provider     ports.PayoutProvider
	nameEnquirer AccountNameEnquirer
	logger       *slog.Logger

	paymentScreener ports.PaymentScreener
	screenerName    string
}

// WalletPayoutOption configures optional cash-out dependencies.
type WalletPayoutOption func(*WalletPayoutService)

// WithWalletPayoutScreener installs the G2 counterparty screen for cash-outs.
func WithWalletPayoutScreener(screener ports.PaymentScreener) WalletPayoutOption {
	return func(s *WalletPayoutService) {
		s.paymentScreener = screener
		if namer, ok := screener.(interface{ ProviderName() string }); ok {
			s.screenerName = namer.ProviderName()
		}
	}
}

// NewWalletPayoutService constructs the cash-out orchestrator. A nil provider
// is tolerated (the flow reports ErrWalletCashOutUnavailable) so unit tests can
// build the service without a rail.
func NewWalletPayoutService(cfg config.Config, repository *store.Store, provider ports.PayoutProvider, nameEnquirer AccountNameEnquirer, logger *slog.Logger, opts ...WalletPayoutOption) *WalletPayoutService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &WalletPayoutService{
		cfg: cfg, store: repository, provider: provider,
		nameEnquirer: nameEnquirer, logger: logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Quote validates the destination and amount and returns the name/fee the
// customer should review. It performs the Interswitch name enquiry.
func (s *WalletPayoutService) Quote(ctx context.Context, amountKobo int64, bankCode, accountNumber string) (WalletPayoutQuote, error) {
	if err := s.validateAmount(amountKobo); err != nil {
		return WalletPayoutQuote{}, err
	}
	accountNumber = strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(accountNumber))
	if len(accountNumber) != 10 || !isDigitsOnly(accountNumber) {
		return WalletPayoutQuote{}, errors.New("account number must be exactly 10 digits")
	}
	if strings.TrimSpace(bankCode) == "" {
		return WalletPayoutQuote{}, errors.New("bank code is required")
	}
	fee := XegoPayoutFee(s.cfg, amountKobo)
	payout := amountKobo - fee
	if payout <= 0 {
		return WalletPayoutQuote{}, fmt.Errorf("amount must be greater than the %s NIP fee", domain.FormatNGN(fee))
	}
	name, err := s.accountName(ctx, accountNumber, bankCode)
	if err != nil {
		return WalletPayoutQuote{}, err
	}
	return WalletPayoutQuote{AccountName: name, FeeKobo: fee, PayoutKobo: payout}, nil
}

// Request executes a confirmed cash-out: reserve allowance, screen, debit the
// wallet (atomically with the row insert), then attempt the transfer.
func (s *WalletPayoutService) Request(ctx context.Context, userID uuid.UUID, in WalletPayoutRequest) (store.WalletPayout, error) {
	if s.provider == nil {
		return store.WalletPayout{}, ErrWalletCashOutUnavailable
	}
	if err := s.validateAmount(in.AmountKobo); err != nil {
		return store.WalletPayout{}, err
	}
	in.AccountNumber = strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(in.AccountNumber))
	if len(in.AccountNumber) != 10 || !isDigitsOnly(in.AccountNumber) {
		return store.WalletPayout{}, errors.New("account number must be exactly 10 digits")
	}
	if strings.TrimSpace(in.BankCode) == "" {
		return store.WalletPayout{}, errors.New("bank code is required")
	}
	if strings.TrimSpace(in.AccountName) == "" {
		return store.WalletPayout{}, errors.New("account name is required")
	}

	profile, err := s.store.KYCProfileByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		profile, err = s.store.EnsureKYCProfile(ctx, userID)
	}
	if err != nil {
		return store.WalletPayout{}, err
	}

	fee := XegoPayoutFee(s.cfg, in.AmountKobo)
	payout := in.AmountKobo - fee
	if payout <= 0 {
		return store.WalletPayout{}, fmt.Errorf("amount must be greater than the %s NIP fee", domain.FormatNGN(fee))
	}

	reservationRef := "wallet-payout:" + uuid.New().String()
	if err := s.store.ReserveAllowance(ctx, store.AllowanceReservation{
		AccountType:             store.AccountIndividual,
		SubjectID:               userID,
		Direction:               kyc.DirOut,
		Tier:                    profile.Tier,
		AmountKobo:              in.AmountKobo,
		Ref:                     reservationRef,
		RiskBand:                profile.RiskBand,
		RiskHighSingleLimitKobo: s.cfg.RiskHighSingleLimitKobo,
	}); err != nil {
		return store.WalletPayout{}, err
	}

	if s.paymentScreener != nil {
		decision, err := s.store.ScreenUserWithdrawal(ctx, userID, reservationRef, in.AmountKobo, in.AccountName,
			func(ctx context.Context) (string, string, string, string, string, []string, error) {
				dec, err := s.paymentScreener.ScreenPayment(ctx, ports.PaymentScreenRequest{
					CounterpartyName: in.AccountName,
					AccountNumber:    in.AccountNumber,
					BankCode:         in.BankCode,
					AmountKobo:       in.AmountKobo,
					SubjectID:        userID.String(),
					Rail:             store.RailWalletWithdrawal,
				})
				if err != nil {
					return "", "", "", "", "", nil, err
				}
				return dec.Decision, s.screenerName, dec.ProviderRef, dec.ListVersion, dec.Message, dec.MatchedNames, nil
			})
		if err != nil {
			// Provider outage: no reviewer release path exists for a
			// withdrawal reservation, so a system failure must not eat the
			// customer's ceiling.
			_ = s.store.ReleaseAllowance(ctx, reservationRef)
			return store.WalletPayout{}, fmt.Errorf("cash-out screening unavailable: %w", err)
		}
		switch decision {
		case kyc.ScreenClear, kyc.ScreenManuallyCleared:
			// proceed
		default:
			// Ambiguous or blocked: keep the reservation (hold semantics).
			return store.WalletPayout{}, ErrWalletCashOutUnderReview
		}
	}

	payoutRow, err := s.store.CreateWalletPayout(ctx, store.CreateWalletPayoutInput{
		UserID:         userID,
		DestinationID:  in.DestinationID,
		BankCode:       strings.TrimSpace(in.BankCode),
		BankName:       strings.TrimSpace(in.BankName),
		AccountNumber:  in.AccountNumber,
		AccountName:    strings.TrimSpace(in.AccountName),
		AmountKobo:     in.AmountKobo,
		PayoutKobo:     payout,
		FeeKobo:        fee,
		Provider:       store.SettlementProviderDefault,
		ReservationRef: reservationRef,
		Description:    "Wallet cash-out " + in.AccountNumber,
	})
	if err != nil {
		_ = s.store.ReleaseAllowance(ctx, reservationRef)
		return store.WalletPayout{}, err
	}
	return s.Dispatch(ctx, payoutRow)
}

// Dispatch attempts one queued cash-out through the provider and records the
// outcome. A pending provider outcome leaves the row processing; the
// maintenance sweep re-queues it later.
func (s *WalletPayoutService) Dispatch(ctx context.Context, payout store.WalletPayout) (store.WalletPayout, error) {
	if s.provider == nil {
		return payout, ErrWalletCashOutUnavailable
	}
	if payout.Status != store.WalletPayoutQueued {
		return payout, nil
	}
	claim, err := s.store.ClaimWalletPayoutForDispatch(ctx, payout.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.store.WalletPayoutByID(ctx, payout.ID)
		}
		return store.WalletPayout{}, err
	}
	result, err := s.provider.Payout(ctx, ports.PayoutRequest{
		Reference:     claim.ID.String(),
		AccountName:   claim.AccountName,
		AccountNumber: claim.AccountNumber,
		BankCode:      claim.BankCode,
		AmountKobo:    claim.PayoutKobo,
		Currency:      "NGN",
	})
	if err != nil {
		if errors.Is(err, ports.ErrPayoutPending) {
			s.logger.InfoContext(ctx, "wallet cash-out pending provider confirmation", "payout_id", claim.ID)
			return s.store.WalletPayoutByID(ctx, claim.ID)
		}
		s.logger.ErrorContext(ctx, "wallet cash-out provider failed", "payout_id", claim.ID, "error", err)
		if ferr := s.store.FailWalletPayout(ctx, claim.ID, err.Error()); ferr != nil {
			s.logger.WarnContext(ctx, "record wallet cash-out failure", "payout_id", claim.ID, "error", ferr)
		}
		return s.store.WalletPayoutByID(ctx, claim.ID)
	}
	if result.Status != "succeeded" {
		message := result.Message
		if message == "" {
			message = "provider declined the cash-out"
		}
		if ferr := s.store.FailWalletPayout(ctx, claim.ID, message); ferr != nil {
			s.logger.WarnContext(ctx, "record wallet cash-out decline", "payout_id", claim.ID, "error", ferr)
		}
		return s.store.WalletPayoutByID(ctx, claim.ID)
	}
	if err := s.store.CompleteWalletPayout(ctx, claim.ID, result.ExternalRef); err != nil {
		return store.WalletPayout{}, err
	}
	s.logger.InfoContext(ctx, "wallet cash-out completed", "payout_id", claim.ID, "external_ref", result.ExternalRef)
	return s.store.WalletPayoutByID(ctx, claim.ID)
}

// DispatchPending sends every queued cash-out.
func (s *WalletPayoutService) DispatchPending(ctx context.Context) error {
	queued, err := s.store.ListQueuedWalletPayouts(ctx, 50)
	if err != nil {
		return err
	}
	for _, payout := range queued {
		if _, err := s.Dispatch(ctx, payout); err != nil {
			s.logger.ErrorContext(ctx, "wallet cash-out dispatch failed", "payout_id", payout.ID, "error", err)
		}
	}
	return nil
}

// Maintain is the worker entry point: retry failed transfers while attempts
// remain, reverse those that have exhausted their attempts (returning the money
// to the wallet), requeue stale in-flight rows, and dispatch queued work.
func (s *WalletPayoutService) Maintain(ctx context.Context) error {
	if requeued, err := s.store.RequeueStaleWalletPayouts(ctx, 30*time.Minute); err != nil {
		s.logger.WarnContext(ctx, "requeue stale wallet cash-outs failed", "error", err)
	} else if requeued > 0 {
		s.logger.InfoContext(ctx, "requeued stale wallet cash-outs", "count", requeued)
	}

	failed, err := s.store.ListFailedWalletPayouts(ctx, 50)
	if err != nil {
		return err
	}
	for _, payout := range failed {
		if payout.Attempts < store.PayoutMaxAttempts {
			if err := s.store.RetryWalletPayout(ctx, payout.ID); err != nil {
				s.logger.WarnContext(ctx, "retry wallet cash-out failed", "payout_id", payout.ID, "error", err)
			}
			continue
		}
		if _, err := s.store.ReverseWalletPayout(ctx, payout.ID,
			"transfer could not be completed after "+fmt.Sprint(payout.Attempts)+" attempts"); err != nil {
			s.logger.WarnContext(ctx, "reverse wallet cash-out failed", "payout_id", payout.ID, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "wallet cash-out reversed to wallet", "payout_id", payout.ID)
	}
	return s.DispatchPending(ctx)
}

// Reverse returns a failed cash-out to the customer's wallet.
func (s *WalletPayoutService) Reverse(ctx context.Context, payoutID uuid.UUID, reason string) (store.WalletPayout, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled"
	}
	return s.store.ReverseWalletPayout(ctx, payoutID, reason)
}

func (s *WalletPayoutService) validateAmount(amountKobo int64) error {
	if amountKobo <= 0 {
		return errors.New("cash-out amount must be positive")
	}
	if s.cfg.PaymentMinKobo > 0 && amountKobo < s.cfg.PaymentMinKobo {
		return fmt.Errorf("amount is below the minimum of %s", domain.FormatNGN(s.cfg.PaymentMinKobo))
	}
	if s.cfg.PaymentMaxKobo > 0 && amountKobo > s.cfg.PaymentMaxKobo {
		return fmt.Errorf("amount is above the maximum of %s", domain.FormatNGN(s.cfg.PaymentMaxKobo))
	}
	return nil
}

// accountName resolves the destination name, falling back to a masked label
// when no enquirer is wired (tests/local). A wired enquirer's failure is
// surfaced: an unverified account must not be paid silently.
func (s *WalletPayoutService) accountName(ctx context.Context, accountNumber, bankCode string) (string, error) {
	if s.nameEnquirer != nil {
		name, err := s.nameEnquirer.NameEnquiry(ctx, accountNumber, bankCode)
		if err != nil {
			return "", fmt.Errorf("could not verify the account: %w", err)
		}
		if name = strings.TrimSpace(name); name != "" {
			return name, nil
		}
	}
	return "Account ending " + accountNumber[len(accountNumber)-4:], nil
}

// MaskedAccountName is the public fallback used by callers that need a label
// without performing an enquiry.
func MaskedAccountName(accountNumber string) string {
	accountNumber = strings.TrimSpace(accountNumber)
	if len(accountNumber) < 4 {
		return "Bank account"
	}
	return "Account ending " + accountNumber[len(accountNumber)-4:]
}
