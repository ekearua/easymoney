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
	"whatsapp-payment-demo/internal/store"
)

const (
	// ChannelWhatsApp identifies customer conversations received through WhatsApp Cloud API.
	ChannelWhatsApp = "whatsapp"
	// ChannelTelegram identifies customer conversations received through Telegram Bot API.
	ChannelTelegram = "telegram"
	// ChannelSMS identifies lightweight SMS-originated request-code orders.
	ChannelSMS = "sms"

	pickerPageSize = 8
	recentLimit    = 3
)

// ConversationService implements the customer onboarding and payment state machine.
type ConversationService struct {
	cfg        config.Config
	store      *store.Store
	payments   *PaymentService
	data       *DataService
	messengers map[string]ports.Messenger
}

// NewConversationService constructs the customer-facing workflow.
func NewConversationService(cfg config.Config, repository *store.Store, payments *PaymentService, data *DataService, messengers map[string]ports.Messenger) *ConversationService {
	return &ConversationService{cfg: cfg, store: repository, payments: payments, data: data, messengers: messengers}
}

// Handle processes one deduplicated inbound customer message from any supported channel.
func (s *ConversationService) Handle(ctx context.Context, message store.InboundMessage) error {
	message.Channel = normalizeChannel(message.Channel)
	user, recipient, err := s.resolveUser(ctx, message)
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
	if strings.EqualFold(input, "/start") || strings.EqualFold(input, "start") {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
	}
	if strings.EqualFold(input, "help") || strings.EqualFold(input, "/help") {
		return s.sendHelp(ctx, message.Channel, recipient)
	}
	if !s.onboardingCompleteForChannel(user, message.Channel) {
		return s.handleOnboarding(ctx, message.Channel, recipient, user, session, input)
	}
	if strings.EqualFold(input, "cancel") || input == "cancel_payment" {
		s.abandonSessionPayment(ctx, user, session)
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, message.Channel, recipient)
	}

	switch session.State {
	case "select_merchant":
		return s.handleMerchant(ctx, message.Channel, recipient, user, session, input)
	case "enter_amount":
		return s.handleAmount(ctx, message.Channel, recipient, user, session, input)
	case "select_payment_method":
		return s.handlePaymentMethod(ctx, message.Channel, recipient, user, session, input)
	case "select_transfer_bank":
		return s.handleTransferBank(ctx, message.Channel, recipient, user, session, input)
	case "select_data_network":
		return s.handleDataNetwork(ctx, message.Channel, recipient, user, session, input)
	case "select_data_plan":
		return s.handleDataPlan(ctx, message.Channel, recipient, user, session, input)
	case "enter_data_phone":
		return s.handleDataPhone(ctx, message.Channel, recipient, user, session, input)
	case "confirm_data_order":
		return s.handleDataOrderConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "select_data_payment_method":
		return s.handleDataPaymentMethod(ctx, message.Channel, recipient, user, session, input)
	case "confirm_data_card":
		return s.handleDataCardConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "select_data_transfer_bank":
		return s.handleDataTransferBank(ctx, message.Channel, recipient, user, session, input)
	case "await_data_bank_transfer":
		return s.handleDataBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "confirm_payment":
		return s.handleConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "await_bank_transfer":
		return s.handleBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
	default:
		return s.handleMenu(ctx, message.Channel, recipient, user, session, input)
	}
}

func (s *ConversationService) resolveUser(ctx context.Context, message store.InboundMessage) (store.User, string, error) {
	switch message.Channel {
	case ChannelTelegram:
		recipient := strings.TrimSpace(message.Recipient)
		if recipient == "" {
			recipient = strings.TrimSpace(message.Sender)
		}
		user, err := s.store.GetOrCreateTelegramUser(ctx, recipient, message.Sender, message.Username)
		return user, recipient, err
	default:
		number := normalizePhone(message.Sender)
		user, err := s.store.GetOrCreateUser(ctx, number)
		return user, number, err
	}
}

func (s *ConversationService) onboardingCompleteForChannel(user store.User, channel string) bool {
	if !user.OnboardingComplete {
		return false
	}
	if channel == ChannelTelegram {
		return user.TelegramConfirmedAt.Valid
	}
	return user.NumberConfirmedAt.Valid
}

func (s *ConversationService) handleOnboarding(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if user.OnboardingComplete && !s.onboardingCompleteForChannel(user, channel) && session.State != "onboard_confirm_account" {
		session.State, session.Data = "onboard_confirm_account", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendAccountConfirmation(ctx, channel, recipient)
	}
	if session.State != "onboard_name" && session.State != "onboard_email" && session.State != "onboard_confirm_account" {
		session.State = "onboard_name"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient,
			"Welcome to "+s.cfg.AppName+" — a simple way to pay merchants.\n\nWhat name should we use on your receipts?")
	}
	if session.State == "onboard_name" {
		name := strings.TrimSpace(input)
		if len(name) < 2 || len(name) > 80 {
			return s.sendText(ctx, channel, recipient, "Please send the name you would like on receipts. It should be between 2 and 80 characters.")
		}
		if err := s.store.UpdateUserName(ctx, user.ID, name); err != nil {
			return err
		}
		session.State = "onboard_email"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Thanks. What email address should we use for checkout and receipts?")
	}
	if session.State == "onboard_confirm_account" {
		switch {
		case input == "confirm_number" || input == "confirm_account" || strings.EqualFold(input, "confirm"):
			var err error
			if channel == ChannelTelegram {
				err = s.store.ConfirmTelegramAccount(ctx, user.ID)
			} else {
				err = s.store.ConfirmUserNumber(ctx, user.ID)
			}
			if err != nil {
				return err
			}
			session.State, session.Data = "menu", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			if err := s.sendText(ctx, channel, recipient, "You’re all set. Your account is confirmed for Xego payments."); err != nil {
				return err
			}
			return s.sendMenu(ctx, channel, recipient)
		case input == "cancel_number" || input == "cancel_account" || strings.EqualFold(input, "cancel"):
			session.State, session.Data = "onboard_name", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, "No problem. Let’s restart your Xego setup.\n\nWhat name should we use on your receipts?")
		default:
			return s.sendAccountConfirmation(ctx, channel, recipient)
		}
	}
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn’t look quite right. Please send a valid address, like name@example.com.")
	}
	if err := s.store.UpdateUserEmail(ctx, user.ID, strings.ToLower(address.Address)); err != nil {
		return err
	}
	session.State, session.Data = "onboard_confirm_account", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendAccountConfirmation(ctx, channel, recipient)
}

func (s *ConversationService) handleMenu(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(input) {
	case "pay", "menu_pay", "make payment":
		session.State = "select_merchant"
		session.Data = map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	case "data", "menu_buy_data", "buy data":
		session.State = "select_data_network"
		session.Data = map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendDataNetworks(ctx, channel, recipient)
	case "status", "menu_status", "check payment status":
		return s.sendLatestStatus(ctx, channel, recipient, user)
	case "history", "menu_history", "recent transactions":
		return s.sendHistory(ctx, channel, recipient, user)
	case "help", "menu_help":
		return s.sendHelp(ctx, channel, recipient)
	default:
		return s.sendMenu(ctx, channel, recipient)
	}
}

func (s *ConversationService) handleMerchant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	if strings.HasPrefix(input, "merchant_page:") {
		page := parsePickerPage(strings.TrimPrefix(input, "merchant_page:"))
		return s.sendMerchantPicker(ctx, channel, recipient, user, session.Data["merchant_query"], page)
	}
	if !strings.HasPrefix(input, "merchant:") {
		query := strings.TrimSpace(input)
		session.Data["merchant_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMerchantPicker(ctx, channel, recipient, user, query, 0)
	}
	slug := strings.TrimPrefix(input, "merchant:")
	merchant, err := s.store.MerchantBySlug(ctx, slug)
	if err != nil {
		return s.sendMerchantPicker(ctx, channel, recipient, user, session.Data["merchant_query"], 0)
	}
	if err := s.store.TouchRecentMerchant(ctx, user.ID, merchant.ID); err != nil {
		return err
	}
	session.State = "enter_amount"
	session.Data["merchant_slug"] = merchant.Slug
	delete(session.Data, "merchant_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("How much would you like to pay %s?\n\nEnter an amount between %s and %s. Example: 2500",
			merchant.Name,
			domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(s.cfg.PaymentMaxKobo)))
}

func (s *ConversationService) handleAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	session.State = "select_payment_method"
	session.Data["amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
}

func (s *ConversationService) handlePaymentMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		session.State = "confirm_payment"
		session.Data["payment_id"] = payment.ID.String()
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCardReview(ctx, channel, recipient, merchant, amount)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		session.State = "select_transfer_bank"
		delete(session.Data, "bank_query")
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendRecommendedTransferBank(ctx, channel, recipient)
	default:
		return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
	}
}

func (s *ConversationService) handleTransferBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	switch {
	case input == "bank_choose_other":
		session.Data["bank_query"] = ""
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case strings.HasPrefix(input, "bank_page:"):
		page := parsePickerPage(strings.TrimPrefix(input, "bank_page:"))
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], page)
	case !strings.HasPrefix(input, "bank:"):
		query := strings.TrimSpace(input)
		session.Data["bank_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, query, 0)
	}
	accountID, err := uuid.Parse(strings.TrimPrefix(input, "bank:"))
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	account, err := s.store.BankTransferAccountByID(ctx, accountID)
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderBankTransfer, channel, recipient)
	if err != nil {
		return err
	}
	payment, instruction, err := s.payments.InitializeBankTransferSimulation(ctx, payment, account)
	if err != nil {
		return err
	}
	session.State = "await_bank_transfer"
	session.Data["payment_id"] = payment.ID.String()
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
}

func (s *ConversationService) handleDataNetwork(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !strings.HasPrefix(input, "data_network:") {
		return s.sendDataNetworks(ctx, channel, recipient)
	}
	code := strings.TrimPrefix(input, "data_network:")
	network, err := s.store.DataNetworkByCode(ctx, code)
	if err != nil {
		return s.sendDataNetworks(ctx, channel, recipient)
	}
	session.State = "select_data_plan"
	session.Data["data_network"] = network.Code
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendDataPlans(ctx, channel, recipient, network.Code)
}

func (s *ConversationService) handleDataPlan(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !strings.HasPrefix(input, "data_plan:") {
		return s.sendDataPlans(ctx, channel, recipient, session.Data["data_network"])
	}
	code := strings.TrimPrefix(input, "data_plan:")
	plan, err := s.store.DataPlanByCode(ctx, code)
	if err != nil || !strings.EqualFold(plan.NetworkCode, session.Data["data_network"]) {
		return s.sendDataPlans(ctx, channel, recipient, session.Data["data_network"])
	}
	session.State = "enter_data_phone"
	session.Data["data_plan"] = plan.Code
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Who should receive %s?\n\nSend the Nigerian phone number, for example 08031234567.", plan.DisplayName))
}

func (s *ConversationService) handleDataPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	phone, err := domain.NormalizeNigerianPhone(input)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	session.State = "confirm_data_order"
	session.Data["data_phone"] = phone
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	plan, err := s.store.DataPlanByCode(ctx, session.Data["data_plan"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data plan is no longer available. Please start again.")
	}
	return s.sendDataReview(ctx, channel, recipient, plan, phone)
}

func (s *ConversationService) handleDataOrderConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_data_order" && !strings.EqualFold(input, "confirm") && !strings.EqualFold(input, "continue") {
		return s.sendDataReviewFromSession(ctx, channel, recipient, user, session)
	}
	order, err := s.data.CreateOrder(ctx, user, channel, recipient, session.Data["data_plan"], session.Data["data_phone"])
	if err != nil {
		return err
	}
	session.State = "select_data_payment_method"
	session.Data["data_order_id"] = order.ID.String()
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendDataPaymentMethods(ctx, channel, recipient, order)
}

func (s *ConversationService) handleDataPaymentMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	order, err := s.dataOrderFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data order session expired. Please start again.")
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		session.State = "confirm_data_card"
		session.Data["payment_id"] = payment.ID.String()
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendDataCardReview(ctx, channel, recipient, order)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return err
		}
		session.State = "select_data_transfer_bank"
		session.Data["payment_id"] = payment.ID.String()
		delete(session.Data, "bank_query")
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendRecommendedTransferBank(ctx, channel, recipient)
	default:
		return s.sendDataPaymentMethods(ctx, channel, recipient, order)
	}
}

func (s *ConversationService) handleDataCardConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_payment" && !strings.EqualFold(input, "confirm") && !strings.EqualFold(input, "continue") {
		return s.sendText(ctx, channel, recipient, "Choose Continue or Cancel to proceed.")
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That payment session expired. Please start again.")
	}
	order, err := s.dataOrderFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data order session expired. Please start again.")
	}
	payment, err = s.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego couldn't start secure card checkout right now. Please try again in a moment.")
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure checkout is ready.\n\nData: %s %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nXego will activate the data order after payment is verified.",
			order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
		payment.CheckoutURL)
}

func (s *ConversationService) handleDataTransferBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	order, err := s.dataOrderFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data order session expired. Please start again.")
	}
	switch {
	case input == "bank_choose_other":
		session.Data["bank_query"] = ""
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case strings.HasPrefix(input, "bank_page:"):
		page := parsePickerPage(strings.TrimPrefix(input, "bank_page:"))
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], page)
	case !strings.HasPrefix(input, "bank:"):
		query := strings.TrimSpace(input)
		session.Data["bank_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, query, 0)
	}
	accountID, err := uuid.Parse(strings.TrimPrefix(input, "bank:"))
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	account, err := s.store.BankTransferAccountByID(ctx, accountID)
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That payment session expired. Please start again.")
	}
	payment, instruction, err := s.payments.InitializeBankTransferSimulation(ctx, payment, account)
	if err != nil {
		return err
	}
	session.State = "await_data_bank_transfer"
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendDataBankTransferInstructions(ctx, channel, recipient, payment, order, instruction)
}

func (s *ConversationService) handleDataBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		order, err := s.dataOrderFromSession(ctx, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That data order session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendDataBankTransferInstructions(ctx, channel, recipient, payment, order, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	if _, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment); err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Thanks. Xego has received your transfer confirmation. Your data order will be fulfilled after payment confirmation is processed.")
}

func (s *ConversationService) sendCardReview(ctx context.Context, channel, recipient string, merchant store.Merchant, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("Review your Xego payment:\n\nMerchant: %s\nAmount: %s\n\nContinue to secure card checkout?", merchant.Name, domain.FormatNGN(amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Continue"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	if _, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment); err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Thanks. Xego has received your transfer confirmation. You’ll receive the final update shortly.")
}

func (s *ConversationService) handleConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_payment" && !strings.EqualFold(input, "confirm") && !strings.EqualFold(input, "continue") {
		return s.sendText(ctx, channel, recipient, "Choose Continue or Cancel to proceed.")
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That payment session expired. Please start again.")
	}
	payment, err = s.payments.InitializeCheckout(ctx, payment)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego couldn’t start secure card checkout right now. Please try again in a moment.")
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure card checkout is ready.\n\nMerchant: %s\nAmount: %s\n\nXego will verify the result before issuing your receipt.", payment.MerchantName, domain.FormatNGN(payment.AmountKobo)),
		payment.CheckoutURL)
}

func (s *ConversationService) sendMenu(ctx context.Context, channel, recipient string) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Welcome back to Xego. What would you like to do?",
		ButtonLabel: "Open menu",
		Sections: []ports.InteractiveSection{{
			Title: "Xego",
			Rows: []ports.InteractiveRow{
				{ID: "menu_pay", Title: "Make payment", Description: "Pay a merchant securely"},
				{ID: "menu_buy_data", Title: "Buy Data", Description: "MTN, Airtel, Glo, 9mobile"},
				{ID: "menu_status", Title: "Payment status", Description: "Check your latest payment"},
				{ID: "menu_history", Title: "Recent payments", Description: "View your latest attempts"},
				{ID: "menu_help", Title: "Help", Description: "How Xego payments work"},
			},
		}},
	})
}

func (s *ConversationService) sendPaymentMethods(ctx context.Context, channel, recipient string, merchant store.Merchant, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("How would you like to pay %s to %s?\n\nFor bank transfer, Xego will give you collection account details and a unique reference to enter in your bank app.", domain.FormatNGN(amount), merchant.Name),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
		},
	})
}

func (s *ConversationService) sendDataNetworks(ctx context.Context, channel, recipient string) error {
	networks, err := s.store.ListActiveDataNetworks(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(networks))
	for _, network := range networks {
		rows = append(rows, ports.InteractiveRow{ID: "data_network:" + network.Code, Title: network.Name, Description: "Buy " + network.Name + " data"})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose the mobile network for this data purchase.",
		ButtonLabel: "Choose network",
		Sections:    []ports.InteractiveSection{{Title: "Networks", Rows: rows}},
	})
}

func (s *ConversationService) sendDataPlans(ctx context.Context, channel, recipient, networkCode string) error {
	plans, err := s.store.ListActiveDataPlans(ctx, networkCode)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(plans))
	for _, plan := range plans {
		rows = append(rows, ports.InteractiveRow{
			ID:          "data_plan:" + plan.Code,
			Title:       truncateInteractiveTitle(plan.DisplayName),
			Description: truncateInteractiveDescription(plan.Validity + " - " + domain.FormatNGN(plan.PriceKobo)),
		})
	}
	if len(rows) == 0 {
		return s.sendText(ctx, channel, recipient, "No active data plans are available for that network right now.")
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose a data plan. Xego will show the phone number and amount again before payment.",
		ButtonLabel: "Choose plan",
		Sections:    []ports.InteractiveSection{{Title: strings.ToUpper(networkCode) + " plans", Rows: rows}},
	})
}

func (s *ConversationService) sendDataReview(ctx context.Context, channel, recipient string, plan store.DataPlan, phone string) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Review your Xego data order:\n\nNetwork: %s\nPlan: %s\nPhone: %s\nAmount: %s\n\nContinue?",
			plan.NetworkName, plan.DisplayName, phone, domain.FormatNGN(plan.PriceKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_data_order", Title: "Continue"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendDataReviewFromSession(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	plan, err := s.store.DataPlanByCode(ctx, session.Data["data_plan"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data plan is no longer available. Please start again.")
	}
	return s.sendDataReview(ctx, channel, recipient, plan, session.Data["data_phone"])
}

func (s *ConversationService) sendDataPaymentMethods(ctx context.Context, channel, recipient string, order store.DataOrderView) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("How would you like to pay %s for %s %s to %s?\n\nRequest code: %s",
			domain.FormatNGN(order.AmountKobo), order.NetworkName, order.PlanName, order.BeneficiaryPhone, order.RequestCode),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
		},
	})
}

func (s *ConversationService) sendDataCardReview(ctx context.Context, channel, recipient string, order store.DataOrderView) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Review your Xego data payment:\n\nNetwork: %s\nPlan: %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nContinue to secure card checkout?",
			order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Continue"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendAccountConfirmation(ctx context.Context, channel, recipient string) error {
	label := "WhatsApp number"
	if channel == ChannelTelegram {
		label = "Telegram account"
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("Xego will use this %s as your account identity.\n\nConfirm this account?", label),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_account", Title: "Confirm"},
			{ID: "cancel_account", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendTransferBanks(ctx context.Context, channel, recipient string) error {
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
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose the Xego collection bank you want to transfer to. Pick the bank that is easiest for you to pay into.",
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Nigerian banks", Rows: rows}},
	})
}

func (s *ConversationService) sendRecommendedTransferBank(ctx context.Context, channel, recipient string) error {
	account, err := s.store.RecommendedBankTransferAccount(ctx)
	if err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Recommended collection bank\n\nBank: %s\nAccount name: %s\nAccount number: %s\n\nUse this bank if it is convenient. Xego will generate a unique payment reference after this step; copy that reference into your bank app's narration, remark, or payment reference field.",
			account.BankName, account.AccountName, account.AccountNumber),
		Buttons: []ports.InteractiveButton{
			{ID: "bank:" + account.ID.String(), Title: "Use this bank"},
			{ID: "bank_choose_other", Title: "Choose another"},
		},
	})
}

func (s *ConversationService) sendTransferBankPicker(ctx context.Context, channel, recipient, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	accounts, hasMore, err := s.store.SearchBankTransferAccounts(ctx, query, page*pickerPageSize, pickerPageSize)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "No collection banks are available right now. Please try again shortly.")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find that bank. Type another bank name, or type MENU to return to the main menu.")
	}
	rows := make([]ports.InteractiveRow, 0, len(accounts)+2)
	for _, account := range accounts {
		rows = append(rows, bankRow(account))
	}
	rows = appendPickerNavigation(rows, "bank_page:", page, hasMore)
	body := "Choose the Xego collection bank you want to transfer to. Pick the bank that is easiest for you to pay into.\n\nAfter choosing, Xego will show the exact amount and a unique reference to enter in your bank app."
	if query != "" {
		body = fmt.Sprintf("Bank search results for %q.\n\nChoose a collection bank, or type another bank name to search again.", query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        body,
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Collection banks", Rows: rows}},
	})
}

func (s *ConversationService) sendBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details\n\nMerchant: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nReference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above to this account.\n3. Put the reference exactly as shown in the narration, remark, or payment reference field.\n4. After sending, tap I have transferred.\n\nThe reference is how Xego matches your transfer to this payment.",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendDataBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, order store.DataOrderView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details for your data order\n\nNetwork: %s\nPlan: %s\nPhone: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nPayment reference: %s\nRequest code: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above.\n3. Put the payment reference exactly in the narration, remark, or payment reference field.\n4. After sending, tap I have transferred.\n\nXego uses the payment reference to match the transfer, and the request code to track this data order.",
			order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference, order.RequestCode),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendMerchants(ctx context.Context, channel, recipient string) error {
	merchants, err := s.store.ListActiveMerchants(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants))
	for _, merchant := range merchants {
		rows = append(rows, ports.InteractiveRow{
			ID:          "merchant:" + merchant.Slug,
			Title:       merchant.Name,
			Description: merchant.Category + " · " + merchant.Description,
		})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient, Body: "Choose who you’d like to pay.", ButtonLabel: "View merchants",
		Sections: []ports.InteractiveSection{{Title: "Merchants", Rows: rows}},
	})
}

func (s *ConversationService) sendMerchantPicker(ctx context.Context, channel, recipient string, user store.User, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	var sections []ports.InteractiveSection
	mainLimit := pickerPageSize
	hasRecents := false
	if query == "" {
		recents, err := s.store.RecentMerchantsForUser(ctx, user.ID, recentLimit)
		if err != nil {
			return err
		}
		if len(recents) > 0 {
			hasRecents = true
			mainLimit = pickerPageSize - len(recents)
			if mainLimit < 3 {
				mainLimit = 3
			}
			if page == 0 {
				rows := make([]ports.InteractiveRow, 0, len(recents))
				for _, merchant := range recents {
					rows = append(rows, merchantRow(merchant))
				}
				sections = append(sections, ports.InteractiveSection{Title: "Recent merchants", Rows: rows})
			}
		}
	}
	offset := page * mainLimit
	var merchants []store.Merchant
	var hasMore bool
	var err error
	if hasRecents && query == "" {
		merchants, hasMore, err = s.store.SearchMerchantsExcludingUserRecents(ctx, user.ID, query, offset, mainLimit)
	} else {
		merchants, hasMore, err = s.store.SearchMerchants(ctx, query, offset, mainLimit)
	}
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants)+2)
	for _, merchant := range merchants {
		rows = append(rows, merchantRow(merchant))
	}
	rows = appendPickerNavigation(rows, "merchant_page:", page, hasMore)
	if len(rows) > 0 {
		sections = append(sections, ports.InteractiveSection{Title: "Merchants", Rows: rows})
	}
	if len(sections) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "No merchants are available right now. Please try again shortly.")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find that merchant. Type another merchant name, or type MENU to return to the main menu.")
	}
	body := "Choose who you'd like to pay.\n\nYou can also type a merchant name or category to search."
	if query != "" {
		body = fmt.Sprintf("Merchant search results for %q.\n\nChoose a merchant, or type another merchant name to search again.", query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient, Body: body, ButtonLabel: "View merchants",
		Sections: sections,
	})
}

func (s *ConversationService) sendLatestStatus(ctx context.Context, channel, recipient string, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 1)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.sendText(ctx, channel, recipient, "You don’t have any Xego payments yet. Choose Make payment to try one.")
	}
	payment := payments[0]
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Latest Xego payment\n\nMerchant: %s\nAmount: %s\nStatus: %s\nReceipt/status: %s/receipts/%s",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status)),
			s.cfg.BaseURL, payment.ReceiptToken))
}

func (s *ConversationService) sendHistory(ctx context.Context, channel, recipient string, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.sendText(ctx, channel, recipient, "You don’t have any Xego payments yet.")
	}
	lines := []string{"Your recent Xego payments:"}
	for _, payment := range payments {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status))))
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) sendHelp(ctx context.Context, channel, recipient string) error {
	return s.sendText(ctx, channel, recipient,
		"Xego lets you pay merchants and buy mobile data for MTN, Airtel, Glo, and 9mobile.\n\nFor bank transfer, enter the payment reference exactly in your bank app's narration, remark, or reference field. This helps Xego match the transfer to your payment.\n\nSMS data requests use: DATA <NETWORK> <PLAN_CODE> <PHONE>. Example: DATA MTN MTN1GB 08031234567.\n\nWe never ask for card details, PINs, OTPs, or CVVs in chat. Type MENU anytime to return to the main menu.")
}

func (s *ConversationService) resetWithMessage(ctx context.Context, channel, recipient string, user store.User, session store.Session, body string) error {
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, body)
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

func (s *ConversationService) dataOrderFromSession(ctx context.Context, session store.Session) (store.DataOrderView, error) {
	orderID, err := uuid.Parse(session.Data["data_order_id"])
	if err != nil {
		return store.DataOrderView{}, err
	}
	return s.store.DataOrderByID(ctx, orderID)
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

func (s *ConversationService) sendText(ctx context.Context, channel, recipient, body string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	return messenger.SendText(ctx, recipient, body)
}

func (s *ConversationService) sendInteractive(ctx context.Context, channel string, message ports.InteractiveMessage) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	return messenger.SendInteractive(ctx, message)
}

func (s *ConversationService) sendCheckout(ctx context.Context, channel, recipient, body, url string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	return messenger.SendCheckout(ctx, recipient, body, url)
}

func (s *ConversationService) messengerFor(channel string) (ports.Messenger, error) {
	messenger, ok := s.messengers[normalizeChannel(channel)]
	if !ok || messenger == nil {
		return nil, fmt.Errorf("messenger channel %q is not configured", channel)
	}
	return messenger, nil
}

func (s *ConversationService) saveSession(ctx context.Context, session store.Session) error {
	session.ExpiresAt = time.Now().Add(s.cfg.SessionTTL)
	return s.store.SaveSession(ctx, session)
}

func merchantRow(merchant store.Merchant) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "merchant:" + merchant.Slug,
		Title:       truncateInteractiveTitle(merchant.Name),
		Description: truncateInteractiveDescription(merchant.Category + " - " + merchant.Description),
	}
}

func bankRow(account store.BankTransferAccount) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "bank:" + account.ID.String(),
		Title:       truncateInteractiveTitle(account.BankName),
		Description: truncateInteractiveDescription(account.AccountName + " - " + account.AccountNumber),
	}
}

func appendPickerNavigation(rows []ports.InteractiveRow, prefix string, page int, hasMore bool) []ports.InteractiveRow {
	if page > 0 {
		rows = append(rows, ports.InteractiveRow{
			ID:          prefix + strconv.Itoa(page-1),
			Title:       "Previous page",
			Description: "Show earlier options",
		})
	}
	if hasMore {
		rows = append(rows, ports.InteractiveRow{
			ID:          prefix + strconv.Itoa(page+1),
			Title:       "Next page",
			Description: "Show more options",
		})
	}
	return rows
}

func parsePickerPage(value string) int {
	page, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return normalizePickerPage(page)
}

func normalizePickerPage(page int) int {
	if page < 0 {
		return 0
	}
	return page
}

func truncateInteractiveTitle(value string) string {
	return truncateRunes(strings.TrimSpace(value), 24)
}

func truncateInteractiveDescription(value string) string {
	return truncateRunes(strings.TrimSpace(value), 72)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func normalizePhone(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		return value
	}
	return "+" + value
}

func normalizeChannel(channel string) string {
	if strings.EqualFold(channel, ChannelTelegram) {
		return ChannelTelegram
	}
	return ChannelWhatsApp
}
