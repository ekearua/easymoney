// Package service coordinates domain rules, persistence, and external providers.
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	qrcode "github.com/skip2/go-qrcode"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// PaymentService owns checkout initialization and authoritative verification.
type PaymentService struct {
	cfg      config.Config
	store    *store.Store
	gateways map[string]ports.PaymentGateway
	router   *ProviderRouter
	logger   *slog.Logger
}

const (
	// ProviderInterswitch identifies card checkout attempts handled by Interswitch Web Checkout.
	ProviderInterswitch = "interswitch"
	// ProviderBankTransfer identifies the in-app simulated bank-transfer rail.
	ProviderBankTransfer = "bank_transfer"
	// ProviderWallet identifies payments funded from the payer's Xego wallet.
	// The customer confirms inline (no hosted checkout) and the wallet is
	// debited atomically with the success transition.
	ProviderWallet = "wallet"
	// ProviderAuto selects the best gateway via the router.
	ProviderAuto = "auto"
)

// NewPaymentService creates the provider-neutral payment coordinator.
func NewPaymentService(cfg config.Config, repository *store.Store, gateways map[string]ports.PaymentGateway, router *ProviderRouter, logger *slog.Logger) *PaymentService {
	return &PaymentService{cfg: cfg, store: repository, gateways: gateways, router: router, logger: logger}
}

// CreateDraft creates a plain merchant collection payment (collection fee added
// to the customer's charge) and moves it to customer confirmation.
func (s *PaymentService) CreateDraft(ctx context.Context, user store.User, merchant store.Merchant, baseKobo int64) (store.PaymentView, error) {
	return s.CreateCollectionDraft(ctx, user, merchant, baseKobo, ProviderInterswitch, ChannelWhatsApp, user.WhatsAppNumber)
}

// CreateDraftForProvider creates a payment attempt for the selected rail
// charging exactly the given amount. It is used by flows with their own
// payment semantics (invoice contributions, thrift, data orders) that do not
// add the collection surcharge.
func (s *PaymentService) CreateDraftForProvider(ctx context.Context, user store.User, merchant store.Merchant, amountKobo int64, provider, channel, recipient string) (store.PaymentView, error) {
	return s.createDraftCore(ctx, user, merchant, amountKobo, provider, channel, recipient, nil)
}

// CreateCollectionDraft creates a plain merchant collection where the Xego
// collection fee is added on top of the merchant's base amount. The customer
// pays base+fee; the merchant receives the full base amount. The base and fee
// are carried in payment metadata so the split posting can credit the merchant
// the full base and book the fee to Xego.
func (s *PaymentService) CreateCollectionDraft(ctx context.Context, user store.User, merchant store.Merchant, baseKobo int64, provider, channel, recipient string) (store.PaymentView, error) {
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider(provider), baseKobo).FeeKobo
	return s.createDraftCore(ctx, user, merchant, baseKobo+fee, provider, channel, recipient, map[string]any{
		"base_amount_kobo":    baseKobo,
		"collection_fee_kobo": fee,
	})
}

func (s *PaymentService) createDraftCore(ctx context.Context, user store.User, merchant store.Merchant, amountKobo int64, provider, channel, recipient string, meta map[string]any) (store.PaymentView, error) {
	if provider == ProviderAuto {
		provider = s.router.PickProvider()
	}
	if provider != ProviderInterswitch && provider != ProviderBankTransfer && provider != ProviderWallet {
		return store.PaymentView{}, fmt.Errorf("unsupported payment provider %q", provider)
	}
	if channel == "" {
		channel = ChannelWhatsApp
	}
	if strings.TrimSpace(recipient) == "" {
		return store.PaymentView{}, errors.New("payment recipient is required")
	}
	token, err := domain.NewReceiptToken()
	if err != nil {
		return store.PaymentView{}, err
	}
	checkoutToken, err := domain.NewCheckoutToken()
	if err != nil {
		return store.PaymentView{}, err
	}
	var metadata json.RawMessage
	if len(meta) > 0 {
		raw, err := json.Marshal(meta)
		if err != nil {
			return store.PaymentView{}, err
		}
		metadata = raw
	}
	payment := domain.Payment{
		ID:                uuid.New(),
		UserID:            user.ID,
		MerchantID:        merchant.ID,
		AmountKobo:        amountKobo,
		Currency:          "NGN",
		Status:            domain.StatusDraft,
		Provider:          provider,
		ProviderReference: domain.NewProviderReference(),
		Channel:           channel,
		Recipient:         recipient,
		ReceiptToken:      token,
		CheckoutToken:     checkoutToken,
		Metadata:          metadata,
	}
	// C9-tiers: money-in allowance. The payer's tier ceiling (single/daily/
	// monthly) is validated and the movement reserved in the same step, keyed by
	// the provider reference so replay never double-counts. A rejection here
	// stops creation entirely; the reservation is released again if the attempt
	// later fails, is abandoned, expires, or is refunded (transitionPayment).
	// Wallet payments skip this reservation: the money is not coming into the
	// platform (it is already in the payer's wallet) — the money-out ceiling is
	// enforced at confirmation instead (ConfirmWalletPayment).
	if provider != ProviderWallet {
		profile, err := s.store.KYCProfileByUser(ctx, user.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			profile, err = s.store.EnsureKYCProfile(ctx, user.ID)
		}
		if err != nil {
			return store.PaymentView{}, err
		}
		if err := s.store.ReserveAllowance(ctx, store.AllowanceReservation{
			AccountType: store.AccountIndividual,
			SubjectID:   user.ID,
			Direction:   kyc.DirIn,
			Tier:        profile.Tier,
			AmountKobo:  amountKobo,
			Ref:         payment.ProviderReference,
		}); err != nil {
			return store.PaymentView{}, err
		}
	}
	if _, err := s.store.CreatePayment(ctx, payment); err != nil {
		// The reservation is keyed by the provider reference and is otherwise
		// only released by transitionPayment on a successful terminal-state
		// transition; a failed creation would orphan it permanently.
		_ = s.store.ReleaseAllowance(ctx, payment.ProviderReference)
		return store.PaymentView{}, err
	}
	if _, err := s.store.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "conversation", map[string]any{"merchant": merchant.Slug}); err != nil {
		_ = s.store.ReleaseAllowance(ctx, payment.ProviderReference)
		return store.PaymentView{}, err
	}
	return s.store.PaymentByID(ctx, payment.ID)
}

// ConfirmWalletPayment completes a wallet-funded payment: it reserves the
// payer's money-out allowance and transitions the payment to succeeded, which
// debits the wallet atomically with the transition (transitionPayment). The
// money-out reservation is released if the confirmation fails. A replay
// (already-succeeded payment) is a no-op: the ref-keyed reservation and the
// status check in the transition both short-circuit.
func (s *PaymentService) ConfirmWalletPayment(ctx context.Context, payment store.PaymentView) (store.PaymentView, bool, error) {
	if payment.Provider != ProviderWallet {
		return payment, false, fmt.Errorf("payment provider %q is not a wallet payment", payment.Provider)
	}
	if payment.Status != domain.StatusAwaitingConfirmation && payment.Status != domain.StatusSucceeded {
		return payment, false, fmt.Errorf("payment is not awaiting confirmation (status %s)", payment.Status)
	}
	if payment.Status == domain.StatusSucceeded {
		return payment, false, nil // replay
	}
	profile, err := s.store.KYCProfileByUser(ctx, payment.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		profile, err = s.store.EnsureKYCProfile(ctx, payment.UserID)
	}
	if err != nil {
		return payment, false, err
	}
	if err := s.store.ReserveAllowance(ctx, store.AllowanceReservation{
		AccountType: store.AccountIndividual,
		SubjectID:   payment.UserID,
		Direction:   kyc.DirOut,
		Tier:        profile.Tier,
		AmountKobo:  payment.AmountKobo,
		Ref:         payment.ProviderReference,
	}); err != nil {
		return payment, false, err
	}
	changed, err := s.store.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "wallet",
		map[string]any{"wallet_payment": true}, s.resultOutbox(payment, domain.StatusSucceeded))
	if err != nil {
		_ = s.store.ReleaseAllowance(ctx, payment.ProviderReference)
		return payment, false, err
	}
	updated, err := s.store.PaymentByID(ctx, payment.ID)
	if err != nil {
		return payment, changed, err
	}
	if changed {
		s.applyPaymentSuccessHooks(ctx, updated)
	}
	return updated, changed, nil
}

// CreateWalletTopupDraft creates the payer's wallet top-up payment against the
// Xego system merchant, charged exactly the given amount and routed through
// the gateway like any other checkout. The wallet_topup metadata routes the
// post-success hook to credit the payer's wallet from the customer float, so
// the wallet lands the full amount once the gateway verifies the payment.
func (s *PaymentService) CreateWalletTopupDraft(ctx context.Context, user store.User, channel, recipient string, amountKobo int64, provider string) (store.PaymentView, error) {
	merchant, err := s.store.WalletTopupSystemMerchant(ctx)
	if err != nil {
		return store.PaymentView{}, err
	}
	return s.createDraftCore(ctx, user, merchant, amountKobo, provider, channel, recipient, map[string]any{
		"wallet_topup": map[string]any{
			"amount_kobo": amountKobo,
		},
	})
}

// isWalletTopup reports whether the payment is a wallet top-up (identified by
// its metadata).
func (s *PaymentService) isWalletTopup(payment store.PaymentView) bool {
	var wrapped struct {
		WalletTopup json.RawMessage `json:"wallet_topup"`
	}
	if len(payment.Metadata) > 0 {
		_ = json.Unmarshal(payment.Metadata, &wrapped)
	}
	return len(wrapped.WalletTopup) > 0
}

// CreateCheckout mints a general request-money link for a payee. No customer is
// bound at creation: the payer is resolved when the hosted link is opened.
func (s *PaymentService) CreateCheckout(ctx context.Context, payee store.Merchant, spec store.CheckoutSpec) (store.CheckoutView, error) {
	if payee.ID != spec.PayeeMerchantID {
		return store.CheckoutView{}, errors.New("checkout payee does not match authenticated merchant")
	}
	return s.store.CreateCheckout(ctx, spec)
}

// ResolveCheckout turns an opened request-money link into a draft payment bound
// to the payee merchant and the resolved payer. The payer is canonicalized to
// E.164 and resolved or created, mirroring the chat and Partner API paths.
// Resolution is idempotent: reopening the link for an active attempt returns
// the existing payment, so a link can never collect money twice. A fresh
// payment is minted only when the previous attempt reached a terminal state
// (failed, abandoned, expired, or already succeeded).
func (s *PaymentService) ResolveCheckout(ctx context.Context, checkout store.CheckoutView, payerPhone string) (store.PaymentView, error) {
	phone := domain.CanonicalE164Phone(payerPhone)
	if len(strings.TrimPrefix(phone, "+")) < 10 {
		return store.PaymentView{}, errors.New("payer phone must be a valid E.164 number")
	}
	if checkout.PaymentID.Valid {
		existing, err := s.store.PaymentByID(ctx, checkout.PaymentID.UUID)
		if err == nil && !paymentAttemptTerminal(existing.Status) {
			return existing, nil
		}
	}
	payee, err := s.store.MerchantByID(ctx, checkout.PayeeMerchantID)
	if err != nil {
		return store.PaymentView{}, err
	}
	user, err := s.store.GetOrCreateUser(ctx, phone)
	if err != nil {
		return store.PaymentView{}, err
	}
	payment, err := s.CreateCollectionDraft(ctx, user, payee, checkout.AmountKobo, ProviderInterswitch, ChannelCheckout, phone)
	if err != nil {
		return store.PaymentView{}, err
	}
	if err := s.store.LinkCheckoutPayment(ctx, checkout.ID, payment.ID); err != nil {
		return store.PaymentView{}, err
	}
	return payment, nil
}

// paymentAttemptTerminal reports whether a payment reached a state from which a
// checkout link should mint a fresh attempt on the next resolve.
func paymentAttemptTerminal(status domain.PaymentStatus) bool {
	switch status {
	case domain.StatusFailed, domain.StatusAbandoned, domain.StatusExpired, domain.StatusSucceeded:
		return true
	}
	return false
}

// ResolveInvoicePayment creates the linked payment attempt for a public
// fill-in payer identified by their WhatsApp number, mirroring how
// ResolveCheckout resolves a request-money link. The amount is charged exactly
// (invoices add no collection surcharge) and the attempt is linked so the
// post-success hook marks the invoice paid.
func (s *PaymentService) ResolveInvoicePayment(ctx context.Context, invoice store.InvoiceView, merchant store.Merchant, payerPhone string, amountKobo int64, provider string) (store.PaymentView, error) {
	phone := domain.CanonicalE164Phone(payerPhone)
	if len(strings.TrimPrefix(phone, "+")) < 10 {
		return store.PaymentView{}, errors.New("payer phone must be a valid E.164 number")
	}
	if provider != ProviderInterswitch && provider != ProviderBankTransfer {
		return store.PaymentView{}, fmt.Errorf("unsupported provider %q for a public invoice payment", provider)
	}
	user, err := s.store.GetOrCreateUser(ctx, phone)
	if err != nil {
		return store.PaymentView{}, err
	}
	payment, err := s.CreateDraftForProvider(ctx, user, merchant, amountKobo, provider, ChannelCheckout, phone)
	if err != nil {
		return store.PaymentView{}, err
	}
	if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amountKobo); err != nil {
		return store.PaymentView{}, err
	}
	return payment, nil
}

// HostedCheckoutURL returns the branded page where the customer reviews and
// confirms the payment before the secure gateway is initialized.
func (s *PaymentService) HostedCheckoutURL(payment store.PaymentView) string {
	return s.cfg.BaseURL + "/checkout/" + payment.CheckoutToken
}

// InitializeCheckout calls the gateway after explicit customer confirmation.
func (s *PaymentService) InitializeCheckout(ctx context.Context, payment store.PaymentView) (store.PaymentView, error) {
	// Bank-transfer payments are routed through the Interswitch gateway too.
	if payment.Status != domain.StatusAwaitingConfirmation {
		return store.PaymentView{}, fmt.Errorf("payment is not awaiting confirmation")
	}
	gateway := s.gateways[payment.Provider]
	if gateway == nil {
		return store.PaymentView{}, fmt.Errorf("no gateway configured for provider %q", payment.Provider)
	}
	checkout, err := gateway.Initialize(ctx, ports.InitializePayment{
		Reference:   payment.ProviderReference,
		Email:       payment.UserEmail,
		AmountKobo:  payment.AmountKobo,
		Currency:    payment.Currency,
		CallbackURL: s.cfg.BaseURL + "/payments/return",
		Metadata: map[string]string{
			"payment_id":    payment.ID.String(),
			"merchant_id":   payment.MerchantID.String(),
			"merchant_slug": payment.MerchantSlug,
		},
	})
	if err != nil {
		return store.PaymentView{}, err
	}
	if checkout.Reference != payment.ProviderReference {
		return store.PaymentView{}, errors.New("gateway returned a mismatched reference")
	}
	if err := s.store.SetCheckout(ctx, payment.ID, checkout.URL); err != nil {
		return store.PaymentView{}, err
	}
	return s.store.PaymentByID(ctx, payment.ID)
}

// InitializeBankTransferSimulation prepares demo transfer details for a chosen
// collection bank and moves the payment into pending.
func (s *PaymentService) InitializeBankTransferSimulation(ctx context.Context, payment store.PaymentView, account store.BankTransferAccount) (store.PaymentView, store.BankTransferInstruction, error) {
	if payment.Provider != ProviderBankTransfer {
		return store.PaymentView{}, store.BankTransferInstruction{}, fmt.Errorf("payment provider %q cannot use bank transfer", payment.Provider)
	}
	if payment.Status != domain.StatusAwaitingConfirmation {
		return store.PaymentView{}, store.BankTransferInstruction{}, fmt.Errorf("payment is not awaiting confirmation")
	}
	instruction, err := s.store.InitializeBankTransferSimulation(ctx, payment.ID, account.ID, payment.ProviderReference)
	if err != nil {
		return store.PaymentView{}, store.BankTransferInstruction{}, err
	}
	updated, err := s.store.PaymentByID(ctx, payment.ID)
	if err != nil {
		return store.PaymentView{}, store.BankTransferInstruction{}, err
	}
	return updated, instruction, nil
}

// ConfirmBankTransferSimulation treats the customer's WhatsApp confirmation as
// the demo's simulated bank verification signal.
func (s *PaymentService) ConfirmBankTransferSimulation(ctx context.Context, payment store.PaymentView) (store.PaymentView, bool, error) {
	if payment.Provider != ProviderBankTransfer {
		return store.PaymentView{}, false, fmt.Errorf("payment provider %q cannot confirm bank transfer", payment.Provider)
	}
	changed, err := s.store.ConfirmBankTransferSimulation(ctx, payment.ID, s.resultOutbox(payment, domain.StatusSucceeded))
	if err != nil {
		return payment, false, err
	}
	updated, err := s.store.PaymentByID(ctx, payment.ID)
	if err != nil {
		return payment, changed, err
	}
	if changed {
		s.applyPaymentSuccessHooks(ctx, updated)
	}
	return updated, changed, nil
}

// VerifyAndApply is the sole path that can mark a payment successful.
func (s *PaymentService) VerifyAndApply(ctx context.Context, reference, source string) (store.PaymentView, bool, error) {
	payment, err := s.store.PaymentByReference(ctx, reference)
	if err != nil {
		return store.PaymentView{}, false, err
	}
	gateway := s.gateways[payment.Provider]
	if gateway == nil {
		return payment, false, fmt.Errorf("no gateway configured for provider %q", payment.Provider)
	}
	verification, err := gateway.Verify(ctx, reference, payment.AmountKobo)
	if err != nil {
		return payment, false, err
	}
	if err := validateVerification(payment, verification); err != nil {
		return payment, false, err
	}
	target := mapGatewayStatus(verification.Status)
	if target == "" {
		return payment, false, nil
	}
	detail := map[string]any{
		"gateway_status":  verification.Status,
		"gateway_message": verification.Message,
	}
	var changed bool
	if isTerminal(target) {
		changed, err = s.store.TransitionPaymentWithOutbox(ctx, payment.ID, target, source, detail, s.resultOutbox(payment, target))
	} else {
		changed, err = s.store.TransitionPayment(ctx, payment.ID, target, source, detail)
	}
	if err != nil {
		return payment, false, err
	}
	updated, err := s.store.PaymentByID(ctx, payment.ID)
	if err != nil {
		return payment, changed, err
	}
	if changed && target == domain.StatusSucceeded {
		s.applyPaymentSuccessHooks(ctx, updated)
	}
	return updated, changed, nil
}

// Post-success purpose application (invoice, thrift, service stock, event
// tickets, collection splits, receipt scan) runs after the payment transition
// commits. Each hook is idempotent and tracked in payment_hooks, so a failure
// is logged and retried by ApplyPendingPaymentHooks instead of failing the
// customer-facing flow or silently leaving the purpose un-applied.
const maxPaymentHookAttempts = 8

func (s *PaymentService) applyPaymentSuccessHooks(ctx context.Context, payment store.PaymentView) {
	hooks := store.PaymentHookOrder
	if s.isWalletTopup(payment) {
		// A wallet top-up credits the payer's wallet from the customer float;
		// no collection-purpose hooks apply.
		hooks = []string{store.PaymentHookWalletTopup}
	} else if s.isIndividualPay(payment) {
		// Individual pay skips the collection-purpose hooks and settles the
		// recipient payout instead.
		hooks = []string{store.PaymentHookIndividualPay}
	}
	if err := s.store.EnsurePaymentHooks(ctx, payment.ID, hooks); err != nil {
		s.logger.Error("ensure payment hooks failed", "payment_id", payment.ID, "error", err)
		return
	}
	for _, hook := range hooks {
		if err := s.runPaymentHook(ctx, payment, hook); err != nil {
			next := paymentHookBackoff(0)
			_ = s.store.MarkPaymentHookFailed(ctx, payment.ID, hook, err.Error(), next, false)
			s.logger.Error("payment hook failed; retry scheduled", "payment_id", payment.ID, "hook", hook, "error", err, "next_retry_at", next)
			continue
		}
		if err := s.store.MarkPaymentHookDone(ctx, payment.ID, hook); err != nil {
			s.logger.Error("mark payment hook done failed", "payment_id", payment.ID, "hook", hook, "error", err)
		}
	}
}

// runPaymentHook applies a single post-success hook for a payment.
func (s *PaymentService) runPaymentHook(ctx context.Context, payment store.PaymentView, hook string) error {
	switch hook {
	case store.PaymentHookInvoice:
		_, _, err := s.store.ApplyInvoicePaymentSuccess(ctx, payment.ID)
		return err
	case store.PaymentHookThrift:
		_, _, err := s.store.ApplyThriftContributionPaymentSuccess(ctx, payment.ID)
		return err
	case store.PaymentHookService:
		return s.store.ConfirmServicePurchase(ctx, payment.ID)
	case store.PaymentHookEvent:
		return s.store.ConfirmEventTicketPurchase(ctx, payment.ID)
	case store.PaymentHookSplits:
		return s.applyCollectionSplits(ctx, payment)
	case store.PaymentHookReceiptScan:
		return s.createReceiptScanToken(ctx, payment)
	case store.PaymentHookIndividualPay:
		return s.applyIndividualPaySettlement(ctx, payment)
	case store.PaymentHookWalletTopup:
		return s.store.ApplyWalletTopup(ctx, payment.ID, payment.UserID, payment.AmountKobo)
	default:
		return fmt.Errorf("unknown payment hook %q", hook)
	}
}

// ApplyPendingPaymentHooks drains payment_hooks rows due for retry. It is the
// worker entry point that guarantees a succeeded payment's purpose application
// eventually completes even when an inline hook attempt fails.
func (s *PaymentService) ApplyPendingPaymentHooks(ctx context.Context) error {
	pending, err := s.store.PendingPaymentHooks(ctx, 50)
	if err != nil {
		return err
	}
	for _, h := range pending {
		payment, err := s.store.PaymentByID(ctx, h.PaymentID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The payment is gone; nothing left to apply.
				_ = s.store.MarkPaymentHookDone(ctx, h.PaymentID, h.Hook)
				continue
			}
			s.logger.Error("load payment for hook retry failed", "payment_id", h.PaymentID, "hook", h.Hook, "error", err)
			continue
		}
		if err := s.runPaymentHook(ctx, payment, h.Hook); err != nil {
			next := paymentHookBackoff(h.Attempts)
			final := h.Attempts+1 >= maxPaymentHookAttempts
			_ = s.store.MarkPaymentHookFailed(ctx, h.PaymentID, h.Hook, err.Error(), next, final)
			if final {
				s.logger.Error("payment hook permanently failed; manual review required", "payment_id", h.PaymentID, "hook", h.Hook, "error", err)
			} else {
				s.logger.Warn("payment hook retry failed", "payment_id", h.PaymentID, "hook", h.Hook, "attempts", h.Attempts+1, "error", err, "next_retry_at", next)
			}
			continue
		}
		if err := s.store.MarkPaymentHookDone(ctx, h.PaymentID, h.Hook); err != nil {
			s.logger.Error("mark payment hook done failed", "payment_id", h.PaymentID, "hook", h.Hook, "error", err)
		}
	}
	return nil
}

// paymentHookBackoff returns when the next retry is due, doubling from 1s up
// to a 15-minute ceiling based on the number of prior failures.
func paymentHookBackoff(attempts int) time.Time {
	delay := time.Duration(1<<min(attempts, 14)) * time.Second
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	return time.Now().Add(delay)
}

// individualPayMeta carries the recipient and split details for an
// individual-pay sender payment. It is stored in payment metadata at draft
// creation so the post-success hook can settle the payout without any session.
type individualPayMeta struct {
	RecipientPhone string `json:"recipient_phone"`
	BankCode       string `json:"bank_code"`
	BankName       string `json:"bank_name"`
	AccountNumber  string `json:"account_number"`
	AmountKobo     int64  `json:"amount_kobo"`
	CollectionFee  int64  `json:"collection_fee_kobo"`
	NIPFee         int64  `json:"nip_fee_kobo"`
	RecipientGets  int64  `json:"recipient_gets"`
}

// CreateIndividualPayDraft creates the sender's payment for an individual
// transfer on the bank-transfer rail. The payment is a plain collection
// against the Xego system merchant, routed through the Interswitch gateway
// like any other checkout; the recipient payout is settled by the post-success
// hook.
func (s *PaymentService) CreateIndividualPayDraft(ctx context.Context, user store.User, channel, recipient string, totalPay int64, meta map[string]any) (store.PaymentView, error) {
	return s.createIndividualPayDraft(ctx, user, channel, recipient, totalPay, ProviderBankTransfer, meta)
}

// CreateIndividualPayDraftWithProvider is CreateIndividualPayDraft for an
// explicitly chosen rail, so a sender can fund the transfer from their wallet.
func (s *PaymentService) CreateIndividualPayDraftWithProvider(ctx context.Context, user store.User, channel, recipient string, totalPay int64, provider string, meta map[string]any) (store.PaymentView, error) {
	return s.createIndividualPayDraft(ctx, user, channel, recipient, totalPay, provider, meta)
}

func (s *PaymentService) createIndividualPayDraft(ctx context.Context, user store.User, channel, recipient string, totalPay int64, provider string, meta map[string]any) (store.PaymentView, error) {
	merchant, err := s.store.IndividualPaySystemMerchant(ctx)
	if err != nil {
		return store.PaymentView{}, err
	}
	return s.createDraftCore(ctx, user, merchant, totalPay, provider, channel, recipient, meta)
}

// isIndividualPay reports whether the payment is an individual-pay sender
// payment (identified by its metadata).
func (s *PaymentService) isIndividualPay(payment store.PaymentView) bool {
	var wrapped struct {
		IndividualPay json.RawMessage `json:"individual_pay"`
	}
	if len(payment.Metadata) > 0 {
		_ = json.Unmarshal(payment.Metadata, &wrapped)
	}
	return len(wrapped.IndividualPay) > 0
}

// applyIndividualPaySettlement settles the recipient's share once the
// sender's payment is verified. It books the splits (collection fee + NIP
// fee) and credits the recipient's wallet (UserPayable → user wallet) in one
// idempotent transaction, so the retry worker can safely re-run it. The
// recipient's money-out allowance is enforced at withdrawal time, not here:
// the money stays inside the platform until the recipient cashes out.
func (s *PaymentService) applyIndividualPaySettlement(ctx context.Context, payment store.PaymentView) error {
	var wrapped struct {
		IndividualPay json.RawMessage `json:"individual_pay"`
	}
	if len(payment.Metadata) > 0 {
		if err := json.Unmarshal(payment.Metadata, &wrapped); err != nil {
			return err
		}
	}
	var meta individualPayMeta
	if len(wrapped.IndividualPay) > 0 {
		if err := json.Unmarshal(wrapped.IndividualPay, &meta); err != nil {
			return err
		}
	}
	if meta.RecipientPhone == "" || meta.AmountKobo <= 0 {
		return fmt.Errorf("individual pay metadata missing for payment %s", payment.ID)
	}
	if meta.RecipientGets <= 0 {
		return nil // the payout fee consumed the whole amount; nothing to disburse
	}
	recipientUser, err := s.store.GetOrCreateUser(ctx, meta.RecipientPhone)
	if err != nil {
		return err
	}
	dest, err := s.store.GetOrCreateUserPayoutDestination(ctx, recipientUser.ID, meta.BankCode, meta.BankName, meta.AccountNumber, "")
	if err != nil {
		return err
	}
	// W1: the recipient's share lands in their wallet rather than leaving the
	// platform, so no money-out allowance is reserved here — the money-out
	// ceiling is enforced when the recipient actually withdraws from the
	// wallet (WalletWithdraw reserves and releases on failure).
	if _, err := s.store.EnsureKYCProfile(ctx, recipientUser.ID); err != nil {
		return err
	}
	var splits []store.SplitSpec
	if meta.CollectionFee > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "xego_collection_fee",
			Account:     store.LedgerAccountXegoPayable,
			AmountKobo:  meta.CollectionFee,
			Currency:    "NGN",
			Description: "Xego collection fee (individual pay)",
		})
	}
	if meta.NIPFee > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "nip_fee",
			Account:     store.LedgerAccountXegoPayable,
			AmountKobo:  meta.NIPFee,
			Currency:    "NGN",
			Description: "NIP payout fee (individual pay)",
		})
	}
	if err := s.store.RecordIndividualPaySettlement(ctx, payment.ID, meta.RecipientGets, dest, splits); err != nil {
		return err
	}
	return nil
}

// applyCollectionSplits computes the Xego platform fee and records the split
// rows (fee + merchant receivable) for a plain merchant collection payment.
// Invoice, thrift, and data payments skip splits: their ledger flows are
// handled by their own hooks. For collection payments created via
// CreateCollectionDraft, the customer is charged base+fee and the merchant
// receives the full base amount (both carried in payment metadata).
func (s *PaymentService) applyCollectionSplits(ctx context.Context, payment store.PaymentView) error {
	isLinked, err := s.store.IsInvoiceThriftOrData(ctx, payment.ID)
	if err != nil {
		return err
	}
	if isLinked {
		return nil
	}

	feeKobo, merchantRecv := s.collectionSplitAmounts(payment)

	var splits []store.SplitSpec
	if feeKobo > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "xego_fee",
			Account:     store.LedgerAccountXegoPayable,
			AmountKobo:  feeKobo,
			Currency:    "NGN",
			Description: "Xego collection fee",
		})
	}
	if merchantRecv > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "merchant_receivable",
			Account:     store.LedgerAccountMerchantPayable,
			AmountKobo:  merchantRecv,
			Currency:    "NGN",
			Description: "Merchant receivable",
		})
	}
	if len(splits) == 0 {
		return nil
	}
	return s.store.ApplyPaymentSplits(ctx, payment.ID, payment.MerchantID, splits)
}

// collectionSplitAmounts derives the fee owed to Xego and the merchant's
// receivable for a succeeded plain collection. For a collection created via
// CreateCollectionDraft, the metadata carries base_amount_kobo and
// collection_fee_kobo, so the merchant receives the full base and the customer
// pays base+fee. Legacy/unknown payments fall back to the historical model
// (fee deducted from the paid amount).
func (s *PaymentService) collectionSplitAmounts(payment store.PaymentView) (feeKobo, merchantRecv int64) {
	var meta struct {
		BaseAmountKobo    int64 `json:"base_amount_kobo"`
		CollectionFeeKobo int64 `json:"collection_fee_kobo"`
	}
	if len(payment.Metadata) > 0 {
		_ = json.Unmarshal(payment.Metadata, &meta)
	}
	if meta.BaseAmountKobo > 0 && meta.CollectionFeeKobo > 0 && meta.BaseAmountKobo+meta.CollectionFeeKobo == payment.AmountKobo {
		return meta.CollectionFeeKobo, meta.BaseAmountKobo
	}
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider(payment.Provider), payment.AmountKobo)
	return fee.FeeKobo, MerchantReceivable(payment.AmountKobo, fee.FeeKobo)
}

func (s *PaymentService) createReceiptScanToken(ctx context.Context, payment store.PaymentView) error {
	uses, err := s.store.EventTicketPurchaseQuantityByPaymentID(ctx, payment.ID)
	if err != nil {
		return err
	}
	if uses <= 0 {
		uses, err = s.store.ServicePurchaseQuantityByPaymentID(ctx, payment.ID)
		if err != nil {
			return err
		}
	}
	token, created, err := s.store.EnsureReceiptScanToken(ctx, payment.ID, uses)
	if errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("receipt scan token skipped: no matching registered service or payment not succeeded",
			"payment_id", payment.ID, "merchant_id", payment.MerchantID)
		return nil
	}
	if err != nil {
		s.logger.Error("receipt scan token creation failed", "payment_id", payment.ID, "error", err)
		return err
	}
	if !created {
		return nil
	}
	scanURL := s.cfg.BaseURL + "/scan/" + token.Token
	receiptURL := s.cfg.BaseURL + "/receipts/" + payment.ReceiptToken
	usesText := "single-use"
	if uses > 1 {
		usesText = fmt.Sprintf("%d uses", uses)
	}
	caption := fmt.Sprintf("Xego receipt scan code\n\nService: %s\nCode: %s\nScan link: %s\nReceipt: %s\n\n%s · expires %s.",
		token.ServiceName, token.ManualCode, scanURL, receiptURL, usesText, token.ExpiresAt.Format("02 Jan 2006, 15:04 MST"))
	png, err := qrcode.Encode(scanURL, qrcode.Medium, 512)
	if err != nil {
		s.logger.Error("QR code generation failed, falling back to text", "payment_id", payment.ID, "error", err)
		return s.store.EnqueueTextForChannel(ctx, payment.UserID, payment.Channel, payment.Recipient, caption)
	}
	imageB64 := base64.StdEncoding.EncodeToString(png)
	if err := s.store.EnqueueImageForChannel(ctx, payment.UserID, payment.Channel, payment.Recipient, imageB64, caption); err != nil {
		s.logger.Error("receipt scan image enqueue failed", "payment_id", payment.ID, "channel", payment.Channel, "error", err)
		return err
	}
	s.logger.Info("receipt scan token created and enqueued as image", "payment_id", payment.ID, "service", token.ServiceName, "channel", payment.Channel)
	return nil
}

// NotifyMerchantPayment enqueues the invoice-owner notification for a settled
// payment. It is invoked by the Phase 3 notification consumer rather than
// inline in the payment transaction.
func (s *PaymentService) NotifyMerchantPayment(ctx context.Context, payment store.PaymentView) {
	invoice, err := s.store.InvoiceByPaymentID(ctx, payment.ID)
	if err != nil {
		return
	}
	prefs, err := s.store.MerchantNotificationPrefs(ctx, invoice.MerchantID)
	if err != nil || !prefs.NotifyPayment {
		return
	}
	owner, err := s.store.MerchantOwnerByInvoiceID(ctx, invoice.ID)
	if err != nil || owner.WhatsAppNumber == "" {
		return
	}
	_ = s.store.EnqueueTextForChannel(ctx, owner.ID, "whatsapp", owner.WhatsAppNumber, fmt.Sprintf(
		"Invoice payment received!\n\nInvoice: %s\nCustomer: %s\nPaid now: %s\nTotal collected: %s of %s\nStatus: %s",
		invoice.Reference, invoice.CustomerWhatsAppNumber, domain.FormatNGN(payment.AmountKobo),
		domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(invoice.TotalKobo), strings.ToUpper(invoice.Status)))
}

func validateVerification(payment store.PaymentView, verification ports.Verification) error {
	if verification.Reference != payment.ProviderReference {
		return errors.New("verified reference does not match payment")
	}
	if verification.AmountKobo != payment.AmountKobo {
		// An un-settled requery is inconclusive, not a discrepancy:
		// Interswitch's gettransaction.json returns Amount 0 while the
		// transaction is still being processed (non-00 response code). Leave the
		// payment pending to be re-requeried; only a terminal verdict
		// (approved/declined) is strict about the amount.
		if mapGatewayStatus(verification.Status) == domain.StatusPending {
			return nil
		}
		return fmt.Errorf("verified amount %d does not match expected %d", verification.AmountKobo, payment.AmountKobo)
	}
	if !strings.EqualFold(verification.Currency, payment.Currency) {
		return errors.New("verified currency does not match payment")
	}
	// Per-provider validation hooks.
	if payment.Provider == ProviderInterswitch {
		if verification.Domain != "test" {
			return errors.New("non-test Interswitch transaction rejected by demo")
		}
	}
	if value := verification.Metadata["payment_id"]; value != "" && value != payment.ID.String() {
		return errors.New("verified payment metadata does not match")
	}
	if value := verification.Metadata["merchant_id"]; value != "" && value != payment.MerchantID.String() {
		return errors.New("verified merchant metadata does not match")
	}
	return nil
}

func mapGatewayStatus(status string) domain.PaymentStatus {
	switch status {
	case "success":
		return domain.StatusSucceeded
	case "failed", "reversed":
		return domain.StatusFailed
	case "abandoned":
		return domain.StatusAbandoned
	case "ongoing", "pending", "processing", "queued":
		return domain.StatusPending
	default:
		return ""
	}
}

func isTerminal(status domain.PaymentStatus) bool {
	return status == domain.StatusSucceeded || status == domain.StatusFailed ||
		status == domain.StatusAbandoned || status == domain.StatusExpired
}

func (s *PaymentService) resultOutbox(payment store.PaymentView, statusValue domain.PaymentStatus) store.OutboxSpec {
	status := strings.ToUpper(string(statusValue))
	receiptURL := s.cfg.BaseURL + "/receipts/" + payment.ReceiptToken
	body := fmt.Sprintf("Xego payment update\n\nStatus: %s\nMerchant: %s\nAmount: %s\nReceipt: %s", status, payment.MerchantName, domain.FormatNGN(payment.AmountKobo), receiptURL)
	kind := "text"
	payload, _ := json.Marshal(map[string]any{"body": body})
	if time.Since(payment.LastInboundAt) < 23*time.Hour {
		kind = "text"
	} else {
		kind = "template"
		payload, _ = json.Marshal(map[string]any{
			"name": s.cfg.WhatsAppTemplateName, "locale": s.cfg.WhatsAppTemplateLocale,
			"parameters": []string{status, domain.FormatNGN(payment.AmountKobo), payment.MerchantName, receiptURL},
		})
	}
	recipient := payment.Recipient
	if recipient == "" {
		recipient = payment.WhatsAppNumber
	}
	channel := payment.Channel
	if channel == "" {
		channel = ChannelWhatsApp
	}
	// API-initiated payments have no chat session to message: the caller tells
	// the customer where to pay, and the API response carries the status. Return
	// the api sentinel so transitionPayment skips the outbox row entirely.
	if channel == ChannelAPI {
		return store.OutboxSpec{Channel: ChannelAPI}
	}
	// A payment created from a browser web flow already lands its confirmation
	// (message 2) inside the flow; do not also queue the generic outbox status
	// message on top of it. ChannelCheckout is written by createDraftCore for
	// checkout-initiated payments and by the web flow handlers for /w/ flows.
	if channel == ChannelCheckout {
		return store.OutboxSpec{Channel: ChannelCheckout}
	}
	return store.OutboxSpec{UserID: payment.UserID, Channel: channel, Recipient: recipient, Kind: kind, Payload: payload}
}

// Reconcile verifies stale unresolved transactions in bounded batches.
func (s *PaymentService) Reconcile(ctx context.Context) error {
	payments, err := s.store.UnresolvedPayments(ctx, time.Now().Add(-30*time.Second), 100)
	if err != nil {
		return err
	}
	for _, payment := range payments {
		if _, _, err := s.VerifyAndApply(ctx, payment.ProviderReference, "reconciliation"); err != nil {
			s.logger.Warn("payment reconciliation failed", "payment_id", payment.ID, "error", err)
		}
	}
	return s.expireStale(ctx)
}

func (s *PaymentService) expireStale(ctx context.Context) error {
	payments, err := s.store.ExpirablePayments(ctx, time.Now().Add(-s.cfg.SessionTTL), 100)
	if err != nil {
		return err
	}
	for _, payment := range payments {
		_, err := s.store.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusExpired, "expiration",
			map[string]any{"reason": "demo timeout"}, s.resultOutbox(payment, domain.StatusExpired))
		if err != nil {
			s.logger.Warn("expire stale payment", "payment_id", payment.ID, "error", err)
			continue
		}
	}
	return nil
}

// ProviderList returns the names of all registered payment gateways.
func (s *PaymentService) ProviderList() []string {
	names := make([]string, 0, len(s.gateways))
	for name := range s.gateways {
		names = append(names, name)
	}
	return names
}

// IsGatewaySuccessEvent reports whether the given event name is the terminal
// success event for the provider. For Interswitch the terminal event is
// TRANSACTION.COMPLETED; a completed transaction may still have failed, so the
// caller must confirm with an authoritative requery before delivering value.
func (s *PaymentService) IsGatewaySuccessEvent(provider, event string) bool {
	switch provider {
	case ProviderInterswitch:
		return event == "TRANSACTION.COMPLETED"
	default:
		return false
	}
}
