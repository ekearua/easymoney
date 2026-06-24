package service

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/whatsapp"
	"whatsapp-payment-demo/internal/store"
)

// ConversationService implements the WhatsApp onboarding and payment state machine.
type ConversationService struct {
	cfg       config.Config
	store     *store.Store
	payments  *PaymentService
	messenger *whatsapp.Client
}

// NewConversationService constructs the customer-facing workflow.
func NewConversationService(cfg config.Config, repository *store.Store, payments *PaymentService, messenger *whatsapp.Client) *ConversationService {
	return &ConversationService{cfg: cfg, store: repository, payments: payments, messenger: messenger}
}

// Handle processes one deduplicated inbound WhatsApp message.
func (s *ConversationService) Handle(ctx context.Context, message whatsapp.InboundMessage) error {
	user, err := s.store.GetOrCreateUser(ctx, normalizePhone(message.From))
	if err != nil {
		return err
	}
	session, err := s.store.LoadSession(ctx, user.ID)
	if err != nil {
		return err
	}
	input := strings.TrimSpace(message.Text)
	if message.Interactive != "" {
		input = message.Interactive
	}
	if strings.EqualFold(input, "help") {
		return s.sendHelp(ctx, user.WhatsAppNumber)
	}
	if !user.OnboardingComplete || !user.NumberConfirmedAt.Valid {
		return s.handleOnboarding(ctx, user, session, input)
	}
	if strings.EqualFold(input, "cancel") || input == "cancel_payment" {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, user.WhatsAppNumber)
	}

	switch session.State {
	case "select_merchant":
		return s.handleMerchant(ctx, user, session, input)
	case "enter_amount":
		return s.handleAmount(ctx, user, session, input)
	case "confirm_payment":
		return s.handleConfirmation(ctx, user, session, input)
	default:
		return s.handleMenu(ctx, user, session, input)
	}
}

func (s *ConversationService) handleOnboarding(ctx context.Context, user store.User, session store.Session, input string) error {
	if user.OnboardingComplete && !user.NumberConfirmedAt.Valid && session.State != "onboard_confirm_number" {
		session.State, session.Data = "onboard_confirm_number", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendNumberConfirmation(ctx, user.WhatsAppNumber)
	}
	if session.State != "onboard_name" && session.State != "onboard_email" && session.State != "onboard_confirm_number" {
		session.State = "onboard_name"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.messenger.SendText(ctx, user.WhatsAppNumber,
			"Welcome to "+s.cfg.AppName+". This is a test-mode payment demo and no real money moves.\n\nWhat name should we use for your receipts?")
	}
	if session.State == "onboard_name" {
		name := strings.TrimSpace(input)
		if len(name) < 2 || len(name) > 80 {
			return s.messenger.SendText(ctx, user.WhatsAppNumber, "Please enter a name between 2 and 80 characters.")
		}
		if err := s.store.UpdateUserName(ctx, user.ID, name); err != nil {
			return err
		}
		session.State = "onboard_email"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "Thanks. Enter an email address for Paystack test checkout and receipts.")
	}
	if session.State == "onboard_confirm_number" {
		switch {
		case input == "confirm_number" || strings.EqualFold(input, "confirm"):
			if err := s.store.ConfirmUserNumber(ctx, user.ID); err != nil {
				return err
			}
			session.State, session.Data = "menu", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			if err := s.messenger.SendText(ctx, user.WhatsAppNumber, "Profile saved. Your WhatsApp number is confirmed for this test-mode demo."); err != nil {
				return err
			}
			return s.sendMenu(ctx, user.WhatsAppNumber)
		case input == "cancel_number" || strings.EqualFold(input, "cancel"):
			session.State, session.Data = "onboard_name", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.messenger.SendText(ctx, user.WhatsAppNumber, "No problem. Let's restart onboarding. What name should we use for your receipts?")
		default:
			return s.sendNumberConfirmation(ctx, user.WhatsAppNumber)
		}
	}
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "That email does not look valid. Please try again.")
	}
	if err := s.store.UpdateUserEmail(ctx, user.ID, strings.ToLower(address.Address)); err != nil {
		return err
	}
	session.State, session.Data = "onboard_confirm_number", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendNumberConfirmation(ctx, user.WhatsAppNumber)
}

func (s *ConversationService) handleMenu(ctx context.Context, user store.User, session store.Session, input string) error {
	switch strings.ToLower(input) {
	case "pay", "menu_pay", "make payment":
		session.State = "select_merchant"
		session.Data = map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMerchants(ctx, user.WhatsAppNumber)
	case "status", "menu_status", "check payment status":
		return s.sendLatestStatus(ctx, user)
	case "history", "menu_history", "recent transactions":
		return s.sendHistory(ctx, user)
	case "help", "menu_help":
		return s.sendHelp(ctx, user.WhatsAppNumber)
	default:
		return s.sendMenu(ctx, user.WhatsAppNumber)
	}
}

func (s *ConversationService) handleMerchant(ctx context.Context, user store.User, session store.Session, input string) error {
	slug := strings.TrimPrefix(input, "merchant:")
	merchant, err := s.store.MerchantBySlug(ctx, slug)
	if err != nil {
		return s.sendMerchants(ctx, user.WhatsAppNumber)
	}
	session.State = "enter_amount"
	session.Data["merchant_slug"] = merchant.Slug
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber,
		fmt.Sprintf("How much would you like to pay %s? Enter an amount from %s to %s.", merchant.Name,
			domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(s.cfg.PaymentMaxKobo)))
}

func (s *ConversationService) handleAmount(ctx context.Context, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, err.Error())
	}
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchants(ctx, user.WhatsAppNumber)
	}
	payment, err := s.payments.CreateDraft(ctx, user, merchant, amount)
	if err != nil {
		return err
	}
	session.State = "confirm_payment"
	session.Data["payment_id"] = payment.ID.String()
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:   user.WhatsAppNumber,
		Body: fmt.Sprintf("Confirm a TEST payment of %s to %s. No real money will move.", domain.FormatNGN(amount), merchant.Name),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Confirm"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleConfirmation(ctx context.Context, user store.User, session store.Session, input string) error {
	if input != "confirm_payment" && !strings.EqualFold(input, "confirm") {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "Choose Confirm or Cancel to continue.")
	}
	paymentID, err := uuid.Parse(session.Data["payment_id"])
	if err != nil {
		return s.resetWithMessage(ctx, user, session, "That payment session expired. Please start again.")
	}
	payment, err := s.store.PaymentByID(ctx, paymentID)
	if err != nil || payment.UserID != user.ID {
		return s.resetWithMessage(ctx, user, session, "That payment could not be found. Please start again.")
	}
	payment, err = s.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		return s.resetWithMessage(ctx, user, session, "Paystack checkout is temporarily unavailable. Please try again.")
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendCheckout(ctx, user.WhatsAppNumber,
		fmt.Sprintf("Complete your TEST card payment of %s to %s on Paystack. We confirm the result server-side.", domain.FormatNGN(payment.AmountKobo), payment.MerchantName),
		payment.CheckoutURL)
}

func (s *ConversationService) sendMenu(ctx context.Context, to string) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:          to,
		Body:        "What would you like to do?",
		ButtonLabel: "Open menu",
		Sections: []ports.InteractiveSection{{
			Title: "Payment demo",
			Rows: []ports.InteractiveRow{
				{ID: "menu_pay", Title: "Make payment", Description: "Pay a fictional merchant"},
				{ID: "menu_status", Title: "Payment status", Description: "Check your latest payment"},
				{ID: "menu_history", Title: "Recent transactions", Description: "View your five latest attempts"},
				{ID: "menu_help", Title: "Help", Description: "Learn how the demo works"},
			},
		}},
	})
}

func (s *ConversationService) sendNumberConfirmation(ctx context.Context, to string) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:   to,
		Body: fmt.Sprintf("We will use %s as your account identity for this Paystack test-mode demo. Confirm this WhatsApp number?", to),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_number", Title: "Confirm"},
			{ID: "cancel_number", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendMerchants(ctx context.Context, to string) error {
	merchants, err := s.store.ListActiveMerchants(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants))
	for _, merchant := range merchants {
		rows = append(rows, ports.InteractiveRow{
			ID: "merchant:" + merchant.Slug, Title: merchant.Name,
			Description: merchant.Category + " · " + merchant.Description,
		})
	}
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To: to, Body: "Choose a fictional demo merchant.", ButtonLabel: "View merchants",
		Sections: []ports.InteractiveSection{{Title: "Demo merchants", Rows: rows}},
	})
}

func (s *ConversationService) sendLatestStatus(ctx context.Context, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 1)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "You do not have any payment attempts yet.")
	}
	payment := payments[0]
	return s.messenger.SendText(ctx, user.WhatsAppNumber,
		fmt.Sprintf("%s · %s · %s\nReceipt/status: %s/receipts/%s",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status)),
			s.cfg.BaseURL, payment.ReceiptToken))
}

func (s *ConversationService) sendHistory(ctx context.Context, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "You do not have any payment attempts yet.")
	}
	lines := []string{"Your recent test payments:"}
	for _, payment := range payments {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status))))
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber, strings.Join(lines, "\n"))
}

func (s *ConversationService) sendHelp(ctx context.Context, to string) error {
	return s.messenger.SendText(ctx, to,
		"This demo lets you select a fictional merchant, enter an NGN amount, and complete a Paystack TEST card checkout. We never ask for card details in WhatsApp. Type MENU at any time.")
}

func (s *ConversationService) resetWithMessage(ctx context.Context, user store.User, session store.Session, body string) error {
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber, body)
}

func (s *ConversationService) saveSession(ctx context.Context, session store.Session) error {
	session.ExpiresAt = time.Now().Add(s.cfg.SessionTTL)
	return s.store.SaveSession(ctx, session)
}

func normalizePhone(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		return value
	}
	return "+" + value
}
