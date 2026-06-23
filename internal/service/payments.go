// Package service coordinates domain rules, persistence, and external providers.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// PaymentService owns checkout initialization and authoritative verification.
type PaymentService struct {
	cfg     config.Config
	store   *store.Store
	gateway ports.PaymentGateway
	logger  *slog.Logger
}

// NewPaymentService creates the provider-neutral payment coordinator.
func NewPaymentService(cfg config.Config, repository *store.Store, gateway ports.PaymentGateway, logger *slog.Logger) *PaymentService {
	return &PaymentService{cfg: cfg, store: repository, gateway: gateway, logger: logger}
}

// CreateDraft creates a payment capability and moves it to customer confirmation.
func (s *PaymentService) CreateDraft(ctx context.Context, user store.User, merchant store.Merchant, amountKobo int64) (store.PaymentView, error) {
	token, err := domain.NewReceiptToken()
	if err != nil {
		return store.PaymentView{}, err
	}
	payment := domain.Payment{
		ID:                uuid.New(),
		UserID:            user.ID,
		MerchantID:        merchant.ID,
		AmountKobo:        amountKobo,
		Currency:          "NGN",
		Status:            domain.StatusDraft,
		Provider:          "paystack",
		ProviderReference: domain.NewProviderReference(),
		ReceiptToken:      token,
	}
	if _, err := s.store.CreatePayment(ctx, payment); err != nil {
		return store.PaymentView{}, err
	}
	if _, err := s.store.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "conversation", map[string]any{"merchant": merchant.Slug}); err != nil {
		return store.PaymentView{}, err
	}
	return s.store.PaymentByID(ctx, payment.ID)
}

// InitializeCheckout calls Paystack only after explicit customer confirmation.
func (s *PaymentService) InitializeCheckout(ctx context.Context, payment store.PaymentView) (store.PaymentView, error) {
	if payment.Status != domain.StatusAwaitingConfirmation {
		return store.PaymentView{}, fmt.Errorf("payment is not awaiting confirmation")
	}
	checkout, err := s.gateway.Initialize(ctx, ports.InitializePayment{
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

// VerifyAndApply is the sole path that can mark a payment successful.
func (s *PaymentService) VerifyAndApply(ctx context.Context, reference, source string) (store.PaymentView, bool, error) {
	payment, err := s.store.PaymentByReference(ctx, reference)
	if err != nil {
		return store.PaymentView{}, false, err
	}
	verification, err := s.gateway.Verify(ctx, reference)
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
	return updated, changed, nil
}

func validateVerification(payment store.PaymentView, verification ports.Verification) error {
	if verification.Reference != payment.ProviderReference {
		return errors.New("verified reference does not match payment")
	}
	if verification.AmountKobo != payment.AmountKobo {
		return fmt.Errorf("verified amount %d does not match expected %d", verification.AmountKobo, payment.AmountKobo)
	}
	if !strings.EqualFold(verification.Currency, payment.Currency) {
		return errors.New("verified currency does not match payment")
	}
	if verification.Domain != "test" {
		return errors.New("non-test Paystack transaction rejected by demo")
	}
	if verification.Status == "success" && verification.Channel != "card" {
		return errors.New("non-card Paystack transaction rejected by demo")
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
	body := fmt.Sprintf("%s payment of %s to %s. Receipt: %s", status, domain.FormatNGN(payment.AmountKobo), payment.MerchantName, receiptURL)
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
	return store.OutboxSpec{UserID: payment.UserID, Recipient: payment.WhatsAppNumber, Kind: kind, Payload: payload}
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
