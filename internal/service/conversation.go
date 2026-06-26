package service

import (
	"context"
	"fmt"
	"net/mail"
	"strconv"
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
		s.abandonSessionPayment(ctx, user, session)
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
	case "select_payment_method":
		return s.handlePaymentMethod(ctx, user, session, input)
	case "select_transfer_bank":
		return s.handleTransferBank(ctx, user, session, input)
	case "confirm_payment":
		return s.handleConfirmation(ctx, user, session, input)
	case "await_bank_transfer":
		return s.handleBankTransferConfirmation(ctx, user, session, input)
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
			"Welcome to "+s.cfg.AppName+" — a simple way to pay merchants from WhatsApp.\n\nThis demo runs in Paystack test mode, so no real money moves.\n\nWhat name should we use on your receipts?")
	}
	if session.State == "onboard_name" {
		name := strings.TrimSpace(input)
		if len(name) < 2 || len(name) > 80 {
			return s.messenger.SendText(ctx, user.WhatsAppNumber, "Please send the name you would like on receipts. It should be between 2 and 80 characters.")
		}
		if err := s.store.UpdateUserName(ctx, user.ID, name); err != nil {
			return err
		}
		session.State = "onboard_email"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "Thanks. What email address should we use for checkout and receipts?")
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
			if err := s.messenger.SendText(ctx, user.WhatsAppNumber, "You’re all set. Your WhatsApp number is confirmed for Xego test payments."); err != nil {
				return err
			}
			return s.sendMenu(ctx, user.WhatsAppNumber)
		case input == "cancel_number" || strings.EqualFold(input, "cancel"):
			session.State, session.Data = "onboard_name", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.messenger.SendText(ctx, user.WhatsAppNumber, "No problem. Let’s restart your Xego setup.\n\nWhat name should we use on your receipts?")
		default:
			return s.sendNumberConfirmation(ctx, user.WhatsAppNumber)
		}
	}
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "That email doesn’t look quite right. Please send a valid address, like name@example.com.")
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
		fmt.Sprintf("How much would you like to pay %s?\n\nEnter an amount between %s and %s. Example: 2500",
			merchant.Name,
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
	session.State = "select_payment_method"
	session.Data["amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendPaymentMethods(ctx, user.WhatsAppNumber, merchant, amount)
}

func (s *ConversationService) handlePaymentMethod(ctx context.Context, user store.User, session store.Session, input string) error {
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchants(ctx, user.WhatsAppNumber)
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderPaystack)
		if err != nil {
			return err
		}
		session.State = "confirm_payment"
		session.Data["payment_id"] = payment.ID.String()
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCardReview(ctx, user.WhatsAppNumber, merchant, amount)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		session.State = "select_transfer_bank"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBanks(ctx, user.WhatsAppNumber)
	default:
		return s.sendPaymentMethods(ctx, user.WhatsAppNumber, merchant, amount)
	}
}

func (s *ConversationService) handleTransferBank(ctx context.Context, user store.User, session store.Session, input string) error {
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchants(ctx, user.WhatsAppNumber)
	}
	accountID, err := uuid.Parse(strings.TrimPrefix(input, "bank:"))
	if err != nil {
		return s.sendTransferBanks(ctx, user.WhatsAppNumber)
	}
	account, err := s.store.BankTransferAccountByID(ctx, accountID)
	if err != nil {
		return s.sendTransferBanks(ctx, user.WhatsAppNumber)
	}
	payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderBankTransfer)
	if err != nil {
		return err
	}
	payment, instruction, err := s.payments.InitializeBankTransferSimulation(ctx, payment, account)
	if err != nil {
		return err
	}
	session.State = "await_bank_transfer"
	session.Data["payment_id"] = payment.ID.String()
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendBankTransferInstructions(ctx, user.WhatsAppNumber, payment, instruction)
}

func (s *ConversationService) sendCardReview(ctx context.Context, to string, merchant store.Merchant, amount int64) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:   to,
		Body: fmt.Sprintf("Review your Xego test payment:\n\nMerchant: %s\nAmount: %s\n\nNo real money will move. Continue to Paystack test checkout?", merchant.Name, domain.FormatNGN(amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Continue"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleBankTransferConfirmation(ctx context.Context, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, user, session, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, user.WhatsAppNumber, payment, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, user, session, "That transfer session expired. Please start again.")
	}
	if _, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment); err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber, "Thanks. Xego is simulating bank confirmation now. You’ll receive the final update shortly.")
}

func (s *ConversationService) handleConfirmation(ctx context.Context, user store.User, session store.Session, input string) error {
	if input != "confirm_payment" && !strings.EqualFold(input, "confirm") && !strings.EqualFold(input, "continue") {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "Choose Continue or Cancel to proceed.")
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, user, session, "That payment session expired. Please start again.")
	}
	payment, err = s.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		return s.resetWithMessage(ctx, user, session, "Xego couldn’t start Paystack test checkout right now. Please try again in a moment.")
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendCheckout(ctx, user.WhatsAppNumber,
		fmt.Sprintf("Your secure Paystack test checkout is ready.\n\nMerchant: %s\nAmount: %s\n\nXego will verify the result before issuing your receipt.", payment.MerchantName, domain.FormatNGN(payment.AmountKobo)),
		payment.CheckoutURL)
}

func (s *ConversationService) sendMenu(ctx context.Context, to string) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:          to,
		Body:        "Welcome back to Xego. What would you like to do?",
		ButtonLabel: "Open menu",
		Sections: []ports.InteractiveSection{{
			Title: "Xego",
			Rows: []ports.InteractiveRow{
				{ID: "menu_pay", Title: "Make payment", Description: "Pay a demo merchant securely"},
				{ID: "menu_status", Title: "Payment status", Description: "Check your latest payment"},
				{ID: "menu_history", Title: "Recent payments", Description: "View your latest attempts"},
				{ID: "menu_help", Title: "Help", Description: "How Xego test payments work"},
			},
		}},
	})
}

func (s *ConversationService) sendPaymentMethods(ctx context.Context, to string, merchant store.Merchant, amount int64) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:   to,
		Body: fmt.Sprintf("How would you like to pay %s to %s?", domain.FormatNGN(amount), merchant.Name),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
		},
	})
}

func (s *ConversationService) sendNumberConfirmation(ctx context.Context, to string) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:   to,
		Body: fmt.Sprintf("Xego will use %s as your account number for this demo.\n\nConfirm this WhatsApp number?", to),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_number", Title: "Confirm"},
			{ID: "cancel_number", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendTransferBanks(ctx context.Context, to string) error {
	accounts, err := s.store.ListActiveBankTransferAccounts(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, ports.InteractiveRow{
			ID:          "bank:" + account.ID.String(),
			Title:       account.BankName,
			Description: account.AccountName + " · " + account.AccountNumber,
		})
	}
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To:          to,
		Body:        "Choose the Xego collection bank you want to simulate transferring to.",
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Nigerian banks", Rows: rows}},
	})
}

func (s *ConversationService) sendBankTransferInstructions(ctx context.Context, to string, payment store.PaymentView, instruction store.BankTransferInstruction) error {
	return s.messenger.SendInteractive(ctx, ports.InteractiveMessage{
		To: to,
		Body: fmt.Sprintf("Bank transfer simulation\n\nMerchant: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nReference: %s\n\nUse these as demo instructions only. No real money should be sent.",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
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
		To: to, Body: "Choose who you’d like to pay.", ButtonLabel: "View merchants",
		Sections: []ports.InteractiveSection{{Title: "Demo merchants", Rows: rows}},
	})
}

func (s *ConversationService) sendLatestStatus(ctx context.Context, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 1)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "You don’t have any Xego payments yet. Choose Make payment to try one.")
	}
	payment := payments[0]
	return s.messenger.SendText(ctx, user.WhatsAppNumber,
		fmt.Sprintf("Latest Xego payment\n\nMerchant: %s\nAmount: %s\nStatus: %s\nReceipt/status: %s/receipts/%s",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status)),
			s.cfg.BaseURL, payment.ReceiptToken))
}

func (s *ConversationService) sendHistory(ctx context.Context, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.messenger.SendText(ctx, user.WhatsAppNumber, "You don’t have any Xego payments yet.")
	}
	lines := []string{"Your recent Xego test payments:"}
	for _, payment := range payments {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status))))
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber, strings.Join(lines, "\n"))
}

func (s *ConversationService) sendHelp(ctx context.Context, to string) error {
	return s.messenger.SendText(ctx, to,
		"Xego lets you choose a merchant, enter an NGN amount, and complete a secure Paystack test checkout.\n\nWe never ask for card details, PINs, OTPs, or CVVs in WhatsApp. Type MENU anytime to return to the main menu.")
}

func (s *ConversationService) resetWithMessage(ctx context.Context, user store.User, session store.Session, body string) error {
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.messenger.SendText(ctx, user.WhatsAppNumber, body)
}

func (s *ConversationService) sessionMerchantAndAmount(ctx context.Context, session store.Session) (store.Merchant, int64, error) {
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		return store.Merchant{}, 0, err
	}
	amount, err := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
	if err != nil || amount <= 0 {
		return store.Merchant{}, 0, fmt.Errorf("invalid session amount")
	}
	return merchant, amount, nil
}

func (s *ConversationService) paymentFromSession(ctx context.Context, user store.User, session store.Session) (store.PaymentView, error) {
	paymentID, err := uuid.Parse(session.Data["payment_id"])
	if err != nil {
		return store.PaymentView{}, err
	}
	payment, err := s.store.PaymentByID(ctx, paymentID)
	if err != nil {
		return store.PaymentView{}, err
	}
	if payment.UserID != user.ID {
		return store.PaymentView{}, fmt.Errorf("payment does not belong to user")
	}
	return payment, nil
}

func (s *ConversationService) abandonSessionPayment(ctx context.Context, user store.User, session store.Session) {
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return
	}
	switch payment.Status {
	case domain.StatusDraft, domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusPending:
		_, _ = s.store.TransitionPayment(ctx, payment.ID, domain.StatusAbandoned, "conversation.cancel", map[string]any{"reason": "customer_cancelled"})
	}
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
