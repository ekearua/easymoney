package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
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

var demoInvoiceCustomerNumbers = map[string]bool{
	"+2347061975340": true,
	"+2348033072780": true,
}

// ConversationService implements the customer onboarding and payment state machine.
type ConversationService struct {
	cfg        config.Config
	store      *store.Store
	payments   *PaymentService
	data       *DataService
	messengers map[string]ports.Messenger
	email      ports.EmailSender
}

// NewConversationService constructs the customer-facing workflow.
func NewConversationService(cfg config.Config, repository *store.Store, payments *PaymentService, data *DataService, messengers map[string]ports.Messenger, email ports.EmailSender) *ConversationService {
	return &ConversationService{cfg: cfg, store: repository, payments: payments, data: data, messengers: messengers, email: email}
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
	if strings.EqualFold(input, "menu") || strings.EqualFold(input, "/menu") {
		s.abandonSessionPayment(ctx, user, session)
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, message.Channel, recipient)
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
	case "select_data_transfer_bank":
		return s.handleDataTransferBank(ctx, message.Channel, recipient, user, session, input)
	case "await_data_bank_transfer":
		return s.handleDataBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "confirm_payment":
		return s.handleConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "await_bank_transfer":
		return s.handleBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "merchant_register_email":
		return s.handleMerchantRegistrationEmail(ctx, message.Channel, recipient, user, session, input)
	case "merchant_register_email_code":
		return s.handleMerchantRegistrationEmailCode(ctx, message.Channel, recipient, user, session, input)
	case "merchant_register_name":
		return s.handleMerchantRegistrationName(ctx, message.Channel, recipient, user, session, input)
	case "merchant_register_category":
		return s.handleMerchantRegistrationCategory(ctx, message.Channel, recipient, user, session, input)
	case "merchant_register_description":
		return s.handleMerchantRegistrationDescription(ctx, message.Channel, recipient, user, session, input)
	case "individual_email":
		return s.handleIndividualEmail(ctx, message.Channel, recipient, user, session, input)
	case "individual_email_code":
		return s.handleIndividualEmailCode(ctx, message.Channel, recipient, user, session, input)
	case "individual_legal_name":
		return s.handleIndividualLegalName(ctx, message.Channel, recipient, user, session, input)
	case "individual_dob":
		return s.handleIndividualDOB(ctx, message.Channel, recipient, user, session, input)
	case "individual_address":
		return s.handleIndividualAddress(ctx, message.Channel, recipient, user, session, input)
	case "individual_occupation":
		return s.handleIndividualOccupation(ctx, message.Channel, recipient, user, session, input)
	case "thrift_name":
		return s.handleThriftName(ctx, message.Channel, recipient, user, session, input)
	case "thrift_amount":
		return s.handleThriftAmount(ctx, message.Channel, recipient, user, session, input)
	case "thrift_frequency":
		return s.handleThriftFrequency(ctx, message.Channel, recipient, user, session, input)
	case "thrift_target":
		return s.handleThriftTarget(ctx, message.Channel, recipient, user, session, input)
	case "thrift_join_code":
		return s.startThriftJoin(ctx, message.Channel, recipient, user, session, input)
	case "thrift_join_confirm":
		return s.handleThriftJoinConfirm(ctx, message.Channel, recipient, user, session, input)
	case "thrift_activate_order":
		return s.handleThriftActivateOrder(ctx, message.Channel, recipient, user, session, input)
	case "thrift_concat_review":
		return s.handleThriftConcatReview(ctx, message.Channel, recipient, user, session, input)
	case "thrift_pay_method":
		return s.handleThriftPayMethod(ctx, message.Channel, recipient, user, session, input)
	case "thrift_pay_bank":
		return s.handleThriftPayBank(ctx, message.Channel, recipient, user, session, input)
	case "await_thrift_bank_transfer":
		return s.handleThriftBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
	case "invoice_select_merchant":
		return s.handleInvoiceMerchant(ctx, message.Channel, recipient, user, session, input)
	case "invoice_customer_phone":
		return s.handleInvoiceCustomerPhone(ctx, message.Channel, recipient, user, session, input)
	case "invoice_customer_email":
		return s.handleInvoiceCustomerEmail(ctx, message.Channel, recipient, user, session, input)
	case "invoice_item_name":
		return s.handleInvoiceItemName(ctx, message.Channel, recipient, user, session, input)
	case "invoice_item_quantity":
		return s.handleInvoiceItemQuantity(ctx, message.Channel, recipient, user, session, input)
	case "invoice_item_unit_price":
		return s.handleInvoiceItemUnitPrice(ctx, message.Channel, recipient, user, session, input)
	case "invoice_add_item":
		return s.handleInvoiceAddItem(ctx, message.Channel, recipient, user, session, input)
	case "invoice_delivery_fee":
		return s.handleInvoiceDeliveryFee(ctx, message.Channel, recipient, user, session, input)
	case "invoice_review":
		return s.handleInvoiceReview(ctx, message.Channel, recipient, user, session, input)
	case "invoice_pay_amount":
		return s.handleInvoicePayAmount(ctx, message.Channel, recipient, user, session, input)
	case "invoice_pay_method":
		return s.handleInvoicePayMethod(ctx, message.Channel, recipient, user, session, input)
	case "invoice_pay_bank":
		return s.handleInvoicePayBank(ctx, message.Channel, recipient, user, session, input)
	case "await_invoice_bank_transfer":
		return s.handleInvoiceBankTransferConfirmation(ctx, message.Channel, recipient, user, session, input)
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
	if session.State == "onboard_confirm_account" {
		return s.handleAccountConfirmation(ctx, channel, recipient, user, session, input)
	}
	if user.OnboardingComplete && !s.onboardingCompleteForChannel(user, channel) &&
		session.State != "onboard_confirm_account" && session.State != "onboard_email" && session.State != "onboard_email_code" {
		session.State, session.Data = "onboard_confirm_account", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendAccountConfirmation(ctx, channel, recipient)
	}
	if session.State != "onboard_name" && session.State != "onboard_email" && session.State != "onboard_email_code" && session.State != "onboard_confirm_account" {
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
	if session.State == "onboard_email_code" {
		session.State, session.Data = "onboard_confirm_account", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, "Email saved. One last step."); err != nil {
			return err
		}
		return s.sendAccountConfirmation(ctx, channel, recipient)
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

func (s *ConversationService) handleAccountConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
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
		if err := s.sendText(ctx, channel, recipient, "You're all set. Your account is confirmed for Xego payments."); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient)
	case input == "cancel_number" || input == "cancel_account" || strings.EqualFold(input, "cancel"):
		session.State, session.Data = "onboard_name", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "No problem. Let's restart your Xego setup.\n\nWhat name should we use on your receipts?")
	case input == "change_email" || strings.EqualFold(input, "change email") || strings.EqualFold(input, "email"):
		session.State, session.Data = "onboard_email", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "No problem. Send the email address you want to use for checkout and receipts.")
	default:
		return s.sendAccountConfirmation(ctx, channel, recipient)
	}
}

func (s *ConversationService) startEmailConfirmation(ctx context.Context, channel, recipient string, userID uuid.UUID, email string) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(s.cfg.EmailVerificationTTL)
	if err := s.store.CreateEmailVerificationCode(ctx, userID, email, emailCodeHash(email, code), expiresAt); err != nil {
		return err
	}
	subject := s.cfg.AppName + " email confirmation code"
	body := fmt.Sprintf("Your %s email confirmation code is %s.\n\nIt expires in %s.\n\nIf you did not request this, you can ignore this message.",
		s.cfg.AppName, code, s.cfg.EmailVerificationTTL.Round(time.Minute))
	if s.email != nil {
		if err := s.email.Send(ctx, email, subject, body); err != nil {
			return s.sendText(ctx, channel, recipient, "Xego could not send the confirmation email right now. Please try RESEND in a moment.")
		}
		return s.sendText(ctx, channel, recipient, "We sent a 6-digit confirmation code to "+email+".\n\nEnter the code here to continue. You can also type RESEND or CHANGE EMAIL.")
	}
	if s.cfg.EmailDemoCodeInChat {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Demo email confirmation for %s\n\nCode: %s\n\nEnter this 6-digit code to continue. In production this code should be delivered by email only.", email, code))
	}
	return s.sendText(ctx, channel, recipient, "Email confirmation is enabled, but email delivery is not configured yet. Ask the operator to configure SMTP or enable the demo code fallback.")
}

func (s *ConversationService) handleMenu(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if ref, ok := invoiceReferenceFromPAY(input); ok {
		return s.startInvoicePayment(ctx, channel, recipient, user, session, ref)
	}
	if code, ok := thriftJoinCodeFromInput(input); ok {
		return s.startThriftJoin(ctx, channel, recipient, user, session, code)
	}
	if code, ok := thriftActivateCodeFromInput(input); ok {
		return s.startThriftActivation(ctx, channel, recipient, user, session, code)
	}
	if code, ok := thriftContributeCodeFromInput(input); ok {
		return s.startThriftContribution(ctx, channel, recipient, user, session, code)
	}
	switch strings.ToLower(input) {
	case "menu_main", "main menu", "back":
		return s.sendMenu(ctx, channel, recipient)
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
	case "register merchant", "merchant registration", "menu_register_merchant":
		if !s.cfg.EmailConfirmationEnabled {
			return s.sendText(ctx, channel, recipient, "Merchant registration is not accepting email-verified requests right now. Please try again later.")
		}
		session.Data = map[string]string{}
		email := strings.TrimSpace(user.Email)
		if email == "" {
			session.State = "merchant_register_email"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, "Let's register your business for review.\n\nFirst, send the email address we should verify and use for merchant updates.")
		}
		session.State = "merchant_register_email_code"
		session.Data["email"] = email
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
	case "merchant services", "menu_merchant_services":
		return s.sendMerchantServicesMenu(ctx, channel, recipient)
	case "become individual", "individual", "menu_become_individual":
		return s.startIndividualUpgrade(ctx, channel, recipient, user, session)
	case "create thrift", "menu_create_thrift", "thrift":
		return s.startThriftCreation(ctx, channel, recipient, user, session)
	case "join thrift", "menu_join_thrift":
		session.State, session.Data = "thrift_join_code", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the thrift invite code. It looks like XG-THRIFT-1234ABCD.")
	case "thrift dashboard", "menu_thrift_dashboard":
		return s.sendThriftDashboard(ctx, channel, recipient, user)
	case "thrift contributions", "menu_thrift_services":
		return s.sendThriftMenu(ctx, channel, recipient)
	case "generate invoice", "menu_generate_invoice", "invoice":
		return s.startInvoiceGeneration(ctx, channel, recipient, user, session)
	case "merchant dashboard", "menu_merchant_dashboard":
		return s.sendMerchantDashboard(ctx, channel, recipient, user)
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

func (s *ConversationService) handleMerchantRegistrationEmail(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !s.cfg.EmailConfirmationEnabled {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Merchant registration is not accepting email-verified requests right now. Please try again later.")
	}
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look quite right. Please send a valid merchant contact email, like owner@example.com.")
	}
	email := strings.ToLower(address.Address)
	if err := s.store.UpdateUserEmail(ctx, user.ID, email); err != nil {
		return err
	}
	session.State = "merchant_register_email_code"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

func (s *ConversationService) handleMerchantRegistrationEmailCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !s.cfg.EmailConfirmationEnabled {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Merchant registration is not accepting email-verified requests right now. Please try again later.")
	}
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	email := session.Data["email"]
	if email == "" {
		email = user.Email
	}
	switch strings.ToLower(input) {
	case "resend", "resend code":
		return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
	case "change_email", "change email", "email":
		session.State, session.Data = "merchant_register_email", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "No problem. Send the email address we should verify for this merchant registration.")
	}
	code := normalizeEmailCode(input)
	if len(code) != 6 {
		return s.sendText(ctx, channel, recipient, "Please enter the 6-digit code we sent to "+email+". You can also type RESEND or CHANGE EMAIL.")
	}
	ok, err := s.store.VerifyEmailCode(ctx, user.ID, email, emailCodeHash(email, code))
	if err != nil {
		return err
	}
	if !ok {
		return s.sendText(ctx, channel, recipient, "That code is incorrect or has expired. Please try again, type RESEND, or type CHANGE EMAIL.")
	}
	session.State = "merchant_register_name"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Email verified for merchant registration.\n\nWhat is the business or merchant name?")
}

func (s *ConversationService) handleMerchantRegistrationName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	name := strings.TrimSpace(input)
	if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
		return s.sendText(ctx, channel, recipient, "Please send the merchant or business name. It should be between 2 and 80 characters.")
	}
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.State = "merchant_register_category"
	session.Data["business_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What category best describes this business? Examples: Food, Retail, Services, Health, Education, Logistics.")
}

func (s *ConversationService) handleMerchantRegistrationCategory(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	category := strings.TrimSpace(input)
	if len([]rune(category)) < 2 || len([]rune(category)) > 50 {
		return s.sendText(ctx, channel, recipient, "Please send a short business category, such as Food, Retail, Services, Health, Education, or Logistics.")
	}
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.State = "merchant_register_description"
	session.Data["category"] = category
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Briefly describe what the business sells or does. One sentence is enough.")
}

func (s *ConversationService) handleMerchantRegistrationDescription(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	description := strings.TrimSpace(input)
	if len([]rune(description)) < 10 || len([]rune(description)) > 240 {
		return s.sendText(ctx, channel, recipient, "Please send a description between 10 and 240 characters.")
	}
	if session.Data == nil || strings.TrimSpace(session.Data["business_name"]) == "" || strings.TrimSpace(session.Data["category"]) == "" || strings.TrimSpace(session.Data["email"]) == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That merchant registration session expired. Please start again.")
	}
	request, err := s.store.CreateMerchantRegistration(ctx, user.ID, session.Data["business_name"], session.Data["category"], description, session.Data["email"])
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	if err := s.sendText(ctx, channel, recipient, fmt.Sprintf("Merchant registration submitted.\n\nBusiness: %s\nCategory: %s\nReference: %s\nStatus: Awaiting approval\n\nXego will use your verified email for follow-up.",
		request.BusinessName, request.Category, request.Reference)); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient)
}

func (s *ConversationService) startIndividualUpgrade(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if user.AccountLevel == "merchant" {
		return s.sendText(ctx, channel, recipient, "This account is currently approved as a merchant. For this demo, thrift creation is only available to individual accounts.")
	}
	if profile, err := s.store.IndividualProfileByUser(ctx, user.ID); err == nil && profile.KYCStatus == "approved_simulated" {
		return s.sendText(ctx, channel, recipient, "Your Xego individual profile is already approved for the demo. Choose Create thrift to start a contribution group.")
	}
	if !s.cfg.EmailConfirmationEnabled {
		return s.sendText(ctx, channel, recipient, "Individual onboarding is not accepting email-verified upgrades right now. Please try again later.")
	}
	session.Data = map[string]string{}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		session.State = "individual_email"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Let's set up your Xego individual profile for thrift contributions.\n\nFirst, send the email address we should verify.")
	}
	session.State = "individual_email_code"
	session.Data["email"] = email
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

func (s *ConversationService) handleIndividualEmail(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send a valid email address, like name@example.com.")
	}
	email := strings.ToLower(address.Address)
	if err := s.store.UpdateUserEmail(ctx, user.ID, email); err != nil {
		return err
	}
	session.State = "individual_email_code"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

func (s *ConversationService) handleIndividualEmailCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	email := session.Data["email"]
	if email == "" {
		email = user.Email
	}
	switch strings.ToLower(input) {
	case "resend", "resend code":
		return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
	case "change_email", "change email", "email":
		session.State, session.Data = "individual_email", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the email address to verify for your individual profile.")
	}
	code := normalizeEmailCode(input)
	if len(code) != 6 {
		return s.sendText(ctx, channel, recipient, "Please enter the 6-digit code we sent to "+email+". You can also type RESEND or CHANGE EMAIL.")
	}
	ok, err := s.store.VerifyEmailCode(ctx, user.ID, email, emailCodeHash(email, code))
	if err != nil {
		return err
	}
	if !ok {
		return s.sendText(ctx, channel, recipient, "That code is incorrect or has expired. Please try again, type RESEND, or type CHANGE EMAIL.")
	}
	session.State = "individual_legal_name"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Email verified.\n\nSend your legal name for this demo individual profile.")
}

func (s *ConversationService) handleIndividualLegalName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	name := strings.TrimSpace(input)
	if len([]rune(name)) < 3 || len([]rune(name)) > 100 {
		return s.sendText(ctx, channel, recipient, "Send your legal name, between 3 and 100 characters.")
	}
	session.State = "individual_dob"
	session.Data["legal_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send your date of birth as YYYY-MM-DD. Example: 1992-05-24")
}

func (s *ConversationService) handleIndividualDOB(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	dob, err := time.Parse("2006-01-02", strings.TrimSpace(input))
	if err != nil || dob.After(time.Now().AddDate(-18, 0, 0)) {
		return s.sendText(ctx, channel, recipient, "Send a valid date of birth as YYYY-MM-DD. For this demo, the individual must be at least 18.")
	}
	session.State = "individual_address"
	session.Data["dob"] = dob.Format("2006-01-02")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send your residential address for the demo profile.")
}

func (s *ConversationService) handleIndividualAddress(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address := strings.TrimSpace(input)
	if len([]rune(address)) < 10 || len([]rune(address)) > 240 {
		return s.sendText(ctx, channel, recipient, "Send an address between 10 and 240 characters.")
	}
	session.State = "individual_occupation"
	session.Data["address"] = address
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What is your occupation?")
}

func (s *ConversationService) handleIndividualOccupation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	occupation := strings.TrimSpace(input)
	if len([]rune(occupation)) < 2 || len([]rune(occupation)) > 80 {
		return s.sendText(ctx, channel, recipient, "Send an occupation between 2 and 80 characters.")
	}
	dob, err := time.Parse("2006-01-02", session.Data["dob"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That individual onboarding session expired. Please start again.")
	}
	if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, session.Data["legal_name"], dob, session.Data["address"], occupation); err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	if err := s.sendText(ctx, channel, recipient, "Your Xego individual profile is approved for this demo.\n\nYou can now create thrift contribution groups."); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient)
}

func (s *ConversationService) startThriftCreation(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.userIsApprovedIndividual(ctx, user) {
		return s.sendText(ctx, channel, recipient, "Create thrift is available to approved individual accounts.\n\nChoose Become an individual to complete email verification and demo KYC approval.")
	}
	session.State = "thrift_name"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Let's create a rotational thrift contribution.\n\nWhat should we call this thrift group?\n\nYou can send all details at once: Name, Amount, Frequency, Members\nExample: Office Pool, 5000, monthly, 8")
}

func (s *ConversationService) handleThriftName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 2 {
		parsed := parseThriftConcatInput(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
		if len(parsed.Errors) > 0 {
			return s.sendText(ctx, channel, recipient, strings.Join(parsed.Errors, "\n"))
		}
		return s.handleThriftConcatCreation(ctx, channel, recipient, user, session, parsed)
	}

	name := strings.TrimSpace(input)
	if len([]rune(name)) < 3 || len([]rune(name)) > 80 {
		return s.sendText(ctx, channel, recipient, "Send a thrift group name between 3 and 80 characters.")
	}
	session.State = "thrift_amount"
	session.Data["thrift_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What fixed amount should each member contribute per cycle? Example: 5000")
}

func (s *ConversationService) handleThriftAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	session.State = "thrift_frequency"
	session.Data["thrift_amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: "How often should members contribute?",
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_frequency_weekly", Title: "Weekly"},
			{ID: "thrift_frequency_monthly", Title: "Monthly"},
		},
	})
}

func (s *ConversationService) handleThriftFrequency(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	frequency := ""
	switch strings.ToLower(input) {
	case "thrift_frequency_weekly", "weekly":
		frequency = "weekly"
	case "thrift_frequency_monthly", "monthly":
		frequency = "monthly"
	default:
		return s.sendText(ctx, channel, recipient, "Choose Weekly or Monthly.")
	}
	session.State = "thrift_target"
	session.Data["thrift_frequency"] = frequency
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "How many members should this thrift group have? Send a number from 2 to 12.")
}

func (s *ConversationService) handleThriftTarget(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	target, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || target < 2 || target > 12 {
		return s.sendText(ctx, channel, recipient, "Send a member count from 2 to 12.")
	}
	amount, err := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	if err != nil || session.Data["thrift_name"] == "" || session.Data["thrift_frequency"] == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}
	group, err := s.store.CreateThriftGroup(ctx, user.ID, session.Data["thrift_name"], amount, session.Data["thrift_frequency"], target)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group created.\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\nInvite code: %s\n\nShare this code with members. They can send JOIN %s to Xego. When all members have joined, send ACTIVATE %s to choose payout rotation.",
		group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount, group.InviteCode, group.InviteCode, group.InviteCode))
}

func (s *ConversationService) handleThriftConcatCreation(ctx context.Context, channel, recipient string, user store.User, session store.Session, parsed thriftConcatResult) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.Data["thrift_name"] = parsed.Name

	if parsed.Amount != "" {
		amount, err := domain.ParseNGNAmount(parsed.Amount, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
		if err != nil {
			return s.sendText(ctx, channel, recipient, err.Error())
		}
		session.Data["thrift_amount_kobo"] = strconv.FormatInt(amount, 10)
	}
	if parsed.Frequency != "" {
		session.Data["thrift_frequency"] = parsed.Frequency
	}
	if parsed.Target != "" {
		session.Data["thrift_target"] = parsed.Target
	}

	missing := []string{}
	if session.Data["thrift_amount_kobo"] == "" {
		missing = append(missing, "amount")
	}
	if session.Data["thrift_frequency"] == "" {
		missing = append(missing, "frequency (weekly or monthly)")
	}
	if session.Data["thrift_target"] == "" {
		missing = append(missing, "member count (2-12)")
	}

	if len(missing) > 0 {
		if session.Data["thrift_amount_kobo"] == "" {
			session.State = "thrift_amount"
		} else if session.Data["thrift_frequency"] == "" {
			session.State = "thrift_frequency"
		} else if session.Data["thrift_target"] == "" {
			session.State = "thrift_target"
		}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Got it. Now send the: "+strings.Join(missing, ", "))
	}

	session.State = "thrift_concat_review"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}

	amount, _ := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	target, _ := strconv.Atoi(session.Data["thrift_target"])

	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Review your thrift group:\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\n\nCreate this group?",
			parsed.Name, domain.FormatNGN(amount), parsed.Frequency, target),
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_concat_confirm", Title: "Create"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftConcatReview(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "thrift_concat_confirm" && !strings.EqualFold(input, "create") && !strings.EqualFold(input, "confirm") {
		return s.sendText(ctx, channel, recipient, "Choose Create or Cancel.")
	}

	amount, err := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	if err != nil || session.Data["thrift_name"] == "" || session.Data["thrift_frequency"] == "" || session.Data["thrift_target"] == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}
	target, err := strconv.Atoi(session.Data["thrift_target"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}

	group, err := s.store.CreateThriftGroup(ctx, user.ID, session.Data["thrift_name"], amount, session.Data["thrift_frequency"], target)
	if err != nil {
		return err
	}

	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group created.\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\nInvite code: %s\n\nShare this code with members. They can send JOIN %s to Xego. When all members have joined, send ACTIVATE %s to choose payout rotation.",
		group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount, group.InviteCode, group.InviteCode, group.InviteCode))
}

func (s *ConversationService) startThriftJoin(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	code, ok := thriftCodeFromText(input)
	if !ok {
		return s.sendText(ctx, channel, recipient, "Send a valid thrift invite code, for example XG-THRIFT-1234ABCD.")
	}
	group, err := s.store.ThriftGroupByInviteCode(ctx, code)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find that thrift invite code. Please check it and try again.")
	}
	if group.Status != "inviting" {
		return s.sendText(ctx, channel, recipient, "That thrift group is not accepting new members right now.")
	}
	session.State = "thrift_join_confirm"
	session.Data = map[string]string{"thrift_invite_code": group.InviteCode}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Join thrift group?\n\nName: %s\nCreator: %s\nContribution: %s %s\nMembers: %d of %d\n\nConfirm that you want to join this rotational contribution group.",
			group.Name, group.CreatorName, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.MemberCount, group.TargetMemberCount),
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_join_confirm", Title: "Join"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftJoinConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "thrift_join_confirm" && !strings.EqualFold(input, "join") && !strings.EqualFold(input, "confirm") {
		return s.startThriftJoin(ctx, channel, recipient, user, session, session.Data["thrift_invite_code"])
	}
	group, member, err := s.store.JoinThriftGroup(ctx, session.Data["thrift_invite_code"], user.ID)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego could not join that thrift group: "+err.Error())
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("You're in.\n\nThrift: %s\nMember: %s\nMembers: %d of %d\n\nWhen the creator activates the group, Xego will show your contribution prompt.",
		group.Name, member.UserName, group.MemberCount, group.TargetMemberCount))
}

func (s *ConversationService) startThriftActivation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	code, ok := thriftCodeFromText(input)
	if !ok {
		return s.sendText(ctx, channel, recipient, "Send ACTIVATE followed by the thrift invite code. Example: ACTIVATE XG-THRIFT-1234ABCD")
	}
	group, err := s.store.ThriftGroupByInviteCode(ctx, code)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find that thrift group.")
	}
	if group.CreatorUserID != user.ID {
		return s.sendText(ctx, channel, recipient, "Only the thrift creator can activate this group.")
	}
	if group.Status != "inviting" {
		return s.sendText(ctx, channel, recipient, "That thrift group is not waiting for activation.")
	}
	members, err := s.store.ThriftMembers(ctx, group.ID)
	if err != nil {
		return err
	}
	if len(members) != group.TargetMemberCount {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("This thrift group has %d of %d members. You can activate it after all members have joined.", len(members), group.TargetMemberCount))
	}
	ids := make([]string, 0, len(members))
	lines := []string{"Choose payout rotation order.\n\nSend the member numbers in payout order. Example: 1 2 3\n"}
	for i, member := range members {
		ids = append(ids, member.ID.String())
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, displayNameOrFallback(member.UserName, member.WhatsAppNumber)))
	}
	raw, _ := json.Marshal(ids)
	session.State = "thrift_activate_order"
	session.Data = map[string]string{"thrift_group_id": group.ID.String(), "thrift_activation_members": string(raw), "thrift_invite_code": group.InviteCode}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) handleThriftActivateOrder(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_group_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift activation session expired. Please start again.")
	}
	var memberIDStrings []string
	if err := json.Unmarshal([]byte(session.Data["thrift_activation_members"]), &memberIDStrings); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift activation session expired. Please start again.")
	}
	indexes := parseRotationIndexes(input)
	if len(indexes) != len(memberIDStrings) {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Send exactly %d member numbers in order. Example: 1 2 3", len(memberIDStrings)))
	}
	ordered := make([]uuid.UUID, 0, len(indexes))
	seen := map[int]bool{}
	for _, index := range indexes {
		if index < 1 || index > len(memberIDStrings) || seen[index] {
			return s.sendText(ctx, channel, recipient, "The rotation order has an invalid or repeated number. Please send the member numbers once each.")
		}
		seen[index] = true
		id, err := uuid.Parse(memberIDStrings[index-1])
		if err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	inviteCode := session.Data["thrift_invite_code"]
	cycle, err := s.store.ActivateThriftGroup(ctx, groupID, user.ID, ordered)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego could not activate the thrift group: "+err.Error())
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group activated.\n\nGroup: %s\nCycle: %d\nContribution: %s\nPayout recipient: %s\nDue: %s\n\nMembers can send CONTRIBUTE %s to pay this cycle.",
		cycle.GroupName, cycle.CycleNumber, domain.FormatNGN(cycle.ContributionAmountKobo), cycle.PayoutMemberName, cycle.DueAt.Format("02 Jan 2006"), inviteCode))
}

func (s *ConversationService) startThriftContribution(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	code, ok := thriftCodeFromText(input)
	if !ok {
		return s.sendText(ctx, channel, recipient, "Send CONTRIBUTE followed by the thrift invite code. Example: CONTRIBUTE XG-THRIFT-1234ABCD")
	}
	contribution, err := s.store.CurrentThriftContributionForUser(ctx, code, user.ID)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find an active unpaid contribution for you in that thrift group.")
	}
	if contribution.Status == "paid" {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Your contribution for %s cycle %d is already paid.", contribution.GroupName, contribution.CycleNumber))
	}
	session.State = "thrift_pay_method"
	session.Data = map[string]string{"thrift_contribution_id": contribution.ID.String(), "thrift_invite_code": code}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay thrift contribution\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nChoose a payment method.",
			contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftPayMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	contribution, err := s.thriftContributionFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please send CONTRIBUTE and the invite code again.")
	}
	merchant, err := s.store.ThriftSystemMerchant(ctx)
	if err != nil {
		return err
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, contribution.AmountKobo, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.LinkThriftContributionPayment(ctx, contribution.ID, payment.ID); err != nil {
			return err
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
			fmt.Sprintf("Your thrift checkout is ready.\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nXego credits the contribution only after payment is verified.",
				contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
			payment.CheckoutURL)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, contribution.AmountKobo, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.LinkThriftContributionPayment(ctx, contribution.ID, payment.ID); err != nil {
			return err
		}
		session.State = "thrift_pay_bank"
		session.Data["payment_id"] = payment.ID.String()
		delete(session.Data, "bank_query")
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	default:
		return s.startThriftContribution(ctx, channel, recipient, user, session, session.Data["thrift_invite_code"])
	}
}

func (s *ConversationService) handleThriftPayBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	contribution, err := s.thriftContributionFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please send CONTRIBUTE and the invite code again.")
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
	session.State = "await_thrift_bank_transfer"
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendThriftBankTransferInstructions(ctx, channel, recipient, payment, contribution, instruction)
}

func (s *ConversationService) handleThriftBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		contribution, err := s.thriftContributionFromSession(ctx, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendThriftBankTransferInstructions(ctx, channel, recipient, payment, contribution, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	updated, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment)
	if err != nil {
		return err
	}
	contribution, _, err := s.store.ApplyThriftContributionPaymentSuccess(ctx, updated.ID)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thanks. Xego has recorded your thrift contribution.\n\nGroup: %s\nCycle: %d\nAmount: %s\nStatus: %s",
		contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo), strings.ToUpper(contribution.Status)))
}

func (s *ConversationService) startInvoiceGeneration(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(merchants) == 0 {
		return s.sendText(ctx, channel, recipient, "Invoice generation is available after your merchant registration is approved.\n\nChoose Register merchant to submit a business, or check with the operator if you have already submitted one.")
	}
	session.Data = map[string]string{}
	if len(merchants) == 1 {
		session.State = "invoice_customer_phone"
		session.Data["invoice_merchant_slug"] = merchants[0].Slug
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Let's generate an invoice for "+merchants[0].Name+".\n\nSend the customer's WhatsApp number and email, comma-separated.\nExample: +2347061975340, customer@example.com\n\nOr send just the WhatsApp number to enter details one at a time.")
	}
	session.State = "invoice_select_merchant"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants))
	for _, merchant := range merchants {
		rows = append(rows, ports.InteractiveRow{ID: "invoice_merchant:" + merchant.Slug, Title: merchant.Name, Description: merchant.Category})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose which approved merchant should issue this invoice.",
		ButtonLabel: "Choose merchant",
		Sections:    []ports.InteractiveSection{{Title: "Your merchants", Rows: rows}},
	})
}

func (s *ConversationService) handleInvoiceMerchant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !strings.HasPrefix(input, "invoice_merchant:") {
		return s.startInvoiceGeneration(ctx, channel, recipient, user, session)
	}
	slug := strings.TrimPrefix(input, "invoice_merchant:")
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, merchant := range merchants {
		if merchant.Slug == slug {
			session.State = "invoice_customer_phone"
			session.Data = map[string]string{"invoice_merchant_slug": slug}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, "Send the customer's WhatsApp number and email, comma-separated.\nExample: +2347061975340, customer@example.com\n\nOr send just the WhatsApp number to enter details one at a time.")
		}
	}
	return s.startInvoiceGeneration(ctx, channel, recipient, user, session)
}

func (s *ConversationService) handleInvoiceCustomerPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 2 {
		phone, phoneErr := domain.NormalizeNigerianPhone(fields[0])
		address, emailErr := mail.ParseAddress(fields[1])
		if phoneErr != nil {
			return s.sendText(ctx, channel, recipient, "Send a valid Nigerian WhatsApp number for the customer, for example +2347061975340.")
		}
		if !demoInvoiceCustomerNumbers[phone] {
			return s.sendText(ctx, channel, recipient, "For this demo, invoices can only be sent to these test WhatsApp numbers:\n+2347061975340\n+2348033072780")
		}
		if emailErr != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
			return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send a valid email address, like customer@example.com.")
		}
		session.State = "invoice_item_name"
		session.Data["invoice_customer_phone"] = phone
		session.Data["invoice_customer_email"] = strings.ToLower(address.Address)
		session.Data["invoice_items"] = "[]"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Add invoice items.\n\nSend one item per line: Name, Quantity, Price\nExample: Website design, 1, 25000\n\nOr send just the item name to enter details one at a time.")
	}

	phone, err := domain.NormalizeNigerianPhone(input)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Send a valid Nigerian WhatsApp number for the customer, for example +2347061975340.")
	}
	if !demoInvoiceCustomerNumbers[phone] {
		return s.sendText(ctx, channel, recipient, "For this demo, invoices can only be sent to these test WhatsApp numbers:\n+2347061975340\n+2348033072780")
	}
	session.State = "invoice_customer_email"
	session.Data["invoice_customer_phone"] = phone
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send the customer's email address for the invoice copy.")
}

func (s *ConversationService) handleInvoiceCustomerEmail(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send the customer's email address, like customer@example.com.")
	}
	session.State = "invoice_item_name"
	session.Data["invoice_customer_email"] = strings.ToLower(address.Address)
	session.Data["invoice_items"] = "[]"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Add invoice items.\n\nSend one item per line: Name, Quantity, Price\nExample: Website design, 1, 25000\n\nOr send just the item name to enter details one at a time.")
}

func (s *ConversationService) handleInvoiceItemName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.Contains(input, "\n") {
		return s.handleInvoiceBulkItems(ctx, channel, recipient, user, session, input)
	}
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 3 {
		name, qtyStr, priceStr, ok := parseInvoiceSingleItemConcat(input, s.cfg.PaymentMaxKobo)
		if ok {
			return s.handleInvoiceSingleItemConcat(ctx, channel, recipient, user, session, name, qtyStr, priceStr)
		}
	}

	name := strings.TrimSpace(input)
	if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
		return s.sendText(ctx, channel, recipient, "Send an item description between 2 and 120 characters.")
	}
	session.State = "invoice_item_quantity"
	session.Data["invoice_item_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Quantity for this item? Send a whole number, for example: 1")
}

func (s *ConversationService) handleInvoiceItemQuantity(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	qty, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || qty < 1 || qty > 1000 {
		return s.sendText(ctx, channel, recipient, "Send a quantity between 1 and 1000.")
	}
	session.State = "invoice_item_unit_price"
	session.Data["invoice_item_quantity"] = strconv.Itoa(qty)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Unit price for this item? Send the amount in naira, for example: 2500")
}

func (s *ConversationService) handleInvoiceItemUnitPrice(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	price, err := domain.ParseNGNAmount(input, 100, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	qty, _ := strconv.Atoi(session.Data["invoice_item_quantity"])
	items, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	items = append(items, store.InvoiceItem{
		Description:   session.Data["invoice_item_name"],
		Quantity:      qty,
		UnitPriceKobo: price,
		LineTotalKobo: int64(qty) * price,
		SortOrder:     len(items) + 1,
	})
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}
	delete(session.Data, "invoice_item_name")
	delete(session.Data, "invoice_item_quantity")
	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: invoiceItemsSummary("Item added. Current invoice items:", items) + "\n\nAdd another item?",
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_add_yes", Title: "Add item"},
			{ID: "invoice_add_no", Title: "Continue"},
		},
	})
}

func (s *ConversationService) handleInvoiceSingleItemConcat(ctx context.Context, channel, recipient string, user store.User, session store.Session, name, qtyStr, priceStr string) error {
	qty, _ := strconv.Atoi(qtyStr)
	price, err := domain.ParseNGNAmount(priceStr, 100, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}

	existing, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	existing = append(existing, store.InvoiceItem{
		Description:   name,
		Quantity:      qty,
		UnitPriceKobo: price,
		LineTotalKobo: int64(qty) * price,
		SortOrder:     len(existing) + 1,
	})
	if err := putInvoiceItems(&session, existing); err != nil {
		return err
	}

	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: invoiceItemsSummary("Item added. Current invoice items:", existing) + "\n\nAdd another item?",
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_add_yes", Title: "Add item"},
			{ID: "invoice_add_no", Title: "Continue"},
		},
	})
}

func (s *ConversationService) handleInvoiceBulkItems(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	parsed := parseInvoiceBulkItems(input, s.cfg.PaymentMaxKobo)

	var allErrors []string
	for i, item := range parsed {
		for _, e := range item.Errors {
			allErrors = append(allErrors, fmt.Sprintf("Item %d (%s): %s", i+1, item.Name, e))
		}
	}
	if len(allErrors) > 0 {
		return s.sendText(ctx, channel, recipient, "Some items have issues:\n"+strings.Join(allErrors, "\n")+"\n\nPlease fix and try again, or send items one at a time.")
	}

	if len(parsed) == 0 {
		return s.sendText(ctx, channel, recipient, "Send at least one item. Format: name, quantity, price (one per line).")
	}
	if len(parsed) > 10 {
		return s.sendText(ctx, channel, recipient, "You can add up to 10 items at once. Please split into smaller batches.")
	}

	var items []store.InvoiceItem
	for _, p := range parsed {
		qty, _ := strconv.Atoi(p.Quantity)
		price, _ := domain.ParseNGNAmount(p.Price, 100, s.cfg.PaymentMaxKobo)
		items = append(items, store.InvoiceItem{
			Description:   p.Name,
			Quantity:      qty,
			UnitPriceKobo: price,
			LineTotalKobo: int64(qty) * price,
			SortOrder:     len(items) + 1,
		})
	}

	existing, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	items = append(existing, items...)
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}

	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: invoiceItemsSummary("Items added. Current invoice items:", items) + "\n\nAdd another item?",
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_add_yes", Title: "Add item"},
			{ID: "invoice_add_no", Title: "Continue"},
		},
	})
}

func (s *ConversationService) handleInvoiceAddItem(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(input) {
	case "invoice_add_yes", "yes", "add", "add item":
		session.State = "invoice_item_name"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the next item name or description.")
	case "invoice_add_no", "no", "continue", "done":
		session.State = "invoice_delivery_fee"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Delivery fee, if any? Send 0 if there is no delivery fee.")
	default:
		return s.sendText(ctx, channel, recipient, "Choose Add item or Continue.")
	}
}

func (s *ConversationService) handleInvoiceDeliveryFee(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	fee := int64(0)
	if !strings.EqualFold(strings.TrimSpace(input), "none") && strings.TrimSpace(input) != "0" {
		amount, err := domain.ParseNGNAmount(input, 0, s.cfg.PaymentMaxKobo)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "Send the delivery fee as a naira amount, or 0 if none.")
		}
		fee = amount
	}
	session.State = "invoice_review"
	session.Data["invoice_delivery_fee_kobo"] = strconv.FormatInt(fee, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceReview(ctx, channel, recipient, user, session)
}

func (s *ConversationService) handleInvoiceReview(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "invoice_confirm" && !strings.EqualFold(input, "confirm") {
		return s.sendInvoiceReview(ctx, channel, recipient, user, session)
	}
	merchant, items, fee, err := s.invoiceDraft(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	dueAt := time.Now().Add(7 * 24 * time.Hour)
	invoice, err := s.store.CreateInvoice(ctx, store.InvoiceSpec{
		MerchantID:             merchant.ID,
		CreatedByUserID:        user.ID,
		CustomerWhatsAppNumber: session.Data["invoice_customer_phone"],
		CustomerEmail:          session.Data["invoice_customer_email"],
		DeliveryFeeKobo:        fee,
		DueAt:                  &dueAt,
		Items:                  items,
	})
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	s.notifyInvoiceCustomer(ctx, invoice)
	link := s.cfg.BaseURL + "/invoices/" + invoice.Reference
	if err := s.sendText(ctx, channel, recipient, fmt.Sprintf("Invoice generated.\n\nMerchant: %s\nCustomer: %s\nTotal: %s\nReference: %s\nLink: %s\n\nThe customer can open the link or send PAY %s to Xego on WhatsApp to pay. Split payment is decided by the paying customer; Xego marks the invoice paid when total collected reaches the invoice total.",
		invoice.MerchantName, invoice.CustomerWhatsAppNumber, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link, invoice.Reference)); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient)
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
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	default:
		return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
	}
}

func (s *ConversationService) startInvoicePayment(ctx context.Context, channel, recipient string, user store.User, session store.Session, reference string) error {
	invoice, err := s.store.InvoiceByReference(ctx, reference)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find that invoice. Check the invoice reference and send it like this: PAY XG-INV-1234ABCD")
	}
	if invoice.Status == "paid" || invoice.AmountPaidKobo >= invoice.TotalKobo {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invoice %s is already fully paid.\n\nMerchant: %s\nTotal: %s", invoice.Reference, invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo)))
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	session.State = "invoice_pay_amount"
	session.Data = map[string]string{"invoice_reference": invoice.Reference}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Invoice %s\n\nMerchant: %s\nTotal: %s\nPaid so far: %s\nRemaining: %s\n\nHow much would you like to pay now?\nSend FULL to pay the remaining balance, or enter a naira amount for a split/partial payment.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(remaining)))
}

func (s *ConversationService) handleInvoicePayAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please send PAY followed by the invoice reference again.")
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	if remaining <= 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice is already fully paid.")
	}
	amount := remaining
	if !strings.EqualFold(strings.TrimSpace(input), "full") {
		parsed, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, remaining)
		if err != nil {
			return s.sendText(ctx, channel, recipient, fmt.Sprintf("Enter an amount between %s and %s, or send FULL to pay the remaining balance.", domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(remaining)))
		}
		amount = parsed
	}
	session.State = "invoice_pay_method"
	session.Data["invoice_pay_amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay invoice %s\n\nMerchant: %s\nAmount now: %s\nRemaining after this payment: %s\n\nChoose a payment method.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount), domain.FormatNGN(remaining-amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleInvoicePayMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, amount, err := s.invoicePaymentSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice payment session expired. Please send PAY followed by the invoice reference again.")
	}
	merchant, err := s.store.MerchantBySlug(ctx, invoice.MerchantSlug)
	if err != nil {
		return err
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amount); err != nil {
			return err
		}
		session.Data["payment_id"] = payment.ID.String()
		payment, err = s.payments.InitializeCheckout(ctx, payment)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego couldn't start secure card checkout right now. Please try again in a moment.")
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nInvoice: %s\nMerchant: %s\nAmount: %s\n\nXego will update the invoice only after payment is verified.",
				invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount)),
			payment.CheckoutURL)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amount); err != nil {
			return err
		}
		session.State = "invoice_pay_bank"
		session.Data["payment_id"] = payment.ID.String()
		delete(session.Data, "bank_query")
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	default:
		session.State = "invoice_pay_amount"
		_ = s.saveSession(ctx, session)
		return s.handleInvoicePayAmount(ctx, channel, recipient, user, session, "full")
	}
}

func (s *ConversationService) handleInvoicePayBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, _, err := s.invoicePaymentSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice payment session expired. Please send PAY followed by the invoice reference again.")
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
	session.State = "await_invoice_bank_transfer"
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceBankTransferInstructions(ctx, channel, recipient, payment, invoice, instruction)
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
	delete(session.Data, "data_plan_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendDataPlanPicker(ctx, channel, recipient, network.Code, "", 0)
}

func (s *ConversationService) handleDataPlan(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	networkCode := session.Data["data_network"]
	switch {
	case strings.HasPrefix(input, "data_plan_page:"):
		page := parsePickerPage(strings.TrimPrefix(input, "data_plan_page:"))
		return s.sendDataPlanPicker(ctx, channel, recipient, networkCode, session.Data["data_plan_query"], page)
	case !strings.HasPrefix(input, "data_plan:"):
		query := strings.TrimSpace(input)
		session.Data["data_plan_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendDataPlanPicker(ctx, channel, recipient, networkCode, query, 0)
	}
	code := strings.TrimPrefix(input, "data_plan:")
	plan, err := s.store.DataPlanByCode(ctx, code)
	if err != nil || !strings.EqualFold(plan.NetworkCode, networkCode) {
		return s.sendDataPlanPicker(ctx, channel, recipient, networkCode, session.Data["data_plan_query"], 0)
	}
	session.State = "enter_data_phone"
	session.Data["data_plan"] = plan.Code
	delete(session.Data, "data_plan_query")
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
	input = strings.ToLower(input)
	if input != "method_card" && input != "card" && input != "paystack" && input != "card checkout" &&
		input != "method_bank_transfer" && input != "bank" && input != "bank transfer" && input != "transfer" {
		return s.sendDataReviewFromSession(ctx, channel, recipient, user, session)
	}
	order, err := s.data.CreateOrder(ctx, user, channel, recipient, session.Data["data_plan"], session.Data["data_phone"])
	if err != nil {
		return err
	}
	session.Data["data_order_id"] = order.ID.String()
	switch input {
	case "method_card", "card", "paystack", "card checkout":
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		session.Data["payment_id"] = payment.ID.String()
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
	default:
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
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	}
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
		session.Data["payment_id"] = payment.ID.String()
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
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	default:
		return s.sendDataPaymentMethods(ctx, channel, recipient, order)
	}
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

func (s *ConversationService) handleInvoiceBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		invoice, _, err := s.invoicePaymentSession(ctx, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendInvoiceBankTransferInstructions(ctx, channel, recipient, payment, invoice, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	updated, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment)
	if err != nil {
		return err
	}
	invoice, changed, err := s.store.ApplyInvoicePaymentSuccess(ctx, updated.ID)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	if changed {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thanks. Xego has recorded your transfer confirmation.\n\nInvoice: %s\nPaid now: %s\nInvoice status: %s\nTotal collected: %s of %s",
			invoice.Reference, domain.FormatNGN(updated.AmountKobo), strings.ToUpper(invoice.Status), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(invoice.TotalKobo)))
	}
	return s.sendText(ctx, channel, recipient, "Thanks. Xego has recorded your transfer confirmation.")
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
			Rows:  mainMenuRows(),
		}},
	})
}

func (s *ConversationService) sendMerchantServicesMenu(ctx context.Context, channel, recipient string) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Merchant services\n\nChoose a merchant action. Merchant-only features unlock after approval.",
		ButtonLabel: "Merchant menu",
		Sections: []ports.InteractiveSection{{
			Title: "Merchants",
			Rows:  merchantServicesRows(),
		}},
	})
}

func (s *ConversationService) sendThriftMenu(ctx context.Context, channel, recipient string) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Thrift contributions\n\nCreate, join, or manage rotational contribution groups.",
		ButtonLabel: "Thrift menu",
		Sections: []ports.InteractiveSection{{
			Title: "Thrift",
			Rows:  thriftMenuRows(),
		}},
	})
}

func mainMenuRows() []ports.InteractiveRow {
	return []ports.InteractiveRow{
		{ID: "menu_pay", Title: "Make payment", Description: "Pay a merchant securely"},
		{ID: "menu_buy_data", Title: "Buy Data", Description: "MTN, Airtel, Glo, 9mobile"},
		{ID: "menu_merchant_services", Title: "Merchant services", Description: "Register, invoice, dashboard"},
		{ID: "menu_thrift_services", Title: "Thrift contributions", Description: "Create, join, contribute"},
		{ID: "menu_status", Title: "Payment status", Description: "Check your latest payment"},
		{ID: "menu_history", Title: "Recent payments", Description: "View your latest attempts"},
		{ID: "menu_help", Title: "Help", Description: "How Xego payments work"},
	}
}

func merchantServicesRows() []ports.InteractiveRow {
	return []ports.InteractiveRow{
		{ID: "menu_register_merchant", Title: "Register merchant", Description: "Submit a business for review"},
		{ID: "menu_generate_invoice", Title: "Generate invoice", Description: "Approved merchants only"},
		{ID: "menu_merchant_dashboard", Title: "Merchant dashboard", Description: "Invoice status summary"},
		{ID: "menu_main", Title: "Back to main menu", Description: "Return to Xego menu"},
	}
}

func thriftMenuRows() []ports.InteractiveRow {
	return []ports.InteractiveRow{
		{ID: "menu_become_individual", Title: "Become individual", Description: "Enable thrift groups"},
		{ID: "menu_create_thrift", Title: "Create thrift", Description: "Rotational contributions"},
		{ID: "menu_join_thrift", Title: "Join thrift", Description: "Use an invite code"},
		{ID: "menu_thrift_dashboard", Title: "Thrift dashboard", Description: "Groups and cycles"},
		{ID: "menu_main", Title: "Back to main menu", Description: "Return to Xego menu"},
	}
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

func (s *ConversationService) sendDataPlanPicker(ctx context.Context, channel, recipient, networkCode, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	plans, hasMore, err := s.store.SearchDataPlans(ctx, networkCode, query, page*pickerPageSize, pickerPageSize)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "No active data plans are available for that network right now.")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find a matching data plan.\n\nSearch tip: type another size or keyword like 1GB, 2GB, weekly, monthly, SME, social, or night. Type MENU to return to the main menu.")
	}
	rows := make([]ports.InteractiveRow, 0, len(plans)+2)
	for _, plan := range plans {
		rows = append(rows, dataPlanRow(plan))
	}
	rows = appendPickerNavigation(rows, "data_plan_page:", page, hasMore)
	body := fmt.Sprintf("Choose a %s data plan.\n\nSearch tip: if you don't see what you want, type a size or keyword like 1GB, 2GB, weekly, monthly, SME, social, or night.", strings.ToUpper(networkCode))
	if query != "" {
		body = fmt.Sprintf("%s data plan search results for %q.\n\nChoose a plan, tap Next page, or type another search like 1GB, weekly, monthly, SME, social, or night.", strings.ToUpper(networkCode), query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        body,
		ButtonLabel: "Choose plan",
		Sections:    []ports.InteractiveSection{{Title: strings.ToUpper(networkCode) + " plans", Rows: rows}},
	})
}

func (s *ConversationService) sendDataReview(ctx context.Context, channel, recipient string, plan store.DataPlan, phone string) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Review your Xego data order:\n\nNetwork: %s\nPlan: %s\nPhone: %s\nAmount: %s\n\nChoose how you would like to pay.",
			plan.NetworkName, plan.DisplayName, phone, domain.FormatNGN(plan.PriceKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
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
			{ID: "change_email", Title: "Change email"},
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

func (s *ConversationService) sendInvoiceBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, invoice store.InvoiceView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details for invoice %s\n\nMerchant: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nPayment reference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above.\n3. Put the payment reference exactly in the narration, remark, or payment reference field.\n4. After sending, tap I have transferred.\n\nXego adds this receipt to the invoice after confirmation. The invoice is fully paid only when total collected reaches %s.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference, domain.FormatNGN(invoice.TotalKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendThriftBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, contribution store.ThriftContributionView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details for thrift contribution\n\nGroup: %s\nCycle: %d\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nPayment reference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above.\n3. Put the payment reference exactly in narration, remark, or payment reference.\n4. After sending, tap I have transferred.\n\nXego credits the thrift cycle only after this payment is confirmed.",
			contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference),
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

func (s *ConversationService) sendInvoiceReview(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	merchant, items, fee, err := s.invoiceDraft(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	subtotal := invoiceSubtotal(items)
	total := subtotal + fee
	body := invoiceItemsSummary("Review invoice", items)
	body += fmt.Sprintf("\n\nMerchant: %s\nCustomer WhatsApp: %s\nCustomer email: %s\nSubtotal: %s\nDelivery fee: %s\nTotal: %s\nDue: 7 days from today\n\nGenerate this invoice?",
		merchant.Name, session.Data["invoice_customer_phone"], session.Data["invoice_customer_email"], domain.FormatNGN(subtotal), domain.FormatNGN(fee), domain.FormatNGN(total))
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: body,
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_confirm", Title: "Generate"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
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

func (s *ConversationService) sendMerchantDashboard(ctx context.Context, channel, recipient string, user store.User) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(merchants) == 0 {
		return s.sendText(ctx, channel, recipient, "Merchant dashboard is available after your merchant registration is approved.")
	}
	invoices, err := s.store.RecentInvoicesForMerchantOwner(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	lines := []string{"Merchant dashboard"}
	for _, merchant := range merchants {
		lines = append(lines, fmt.Sprintf("\n%s — %s", merchant.Name, merchant.Category))
	}
	if len(invoices) == 0 {
		lines = append(lines, "\nNo invoices generated yet. Choose Generate invoice to create one.")
		return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
	}
	lines = append(lines, "\nRecent invoices:")
	for _, invoice := range invoices {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s/%s — %s",
			invoice.Reference, strings.ToUpper(invoice.Status), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(invoice.TotalKobo), invoice.CustomerWhatsAppNumber))
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) sendThriftDashboard(ctx context.Context, channel, recipient string, user store.User) error {
	groups, err := s.store.RecentThriftGroupsForUser(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have any thrift groups yet.\n\nChoose Become individual, then Create thrift, or join a group with an invite code.")
	}
	lines := []string{"Your thrift dashboard:"}
	for _, group := range groups {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s %s — members %d/%d — code %s",
			group.Name, strings.ToUpper(group.Status), domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.MemberCount, group.TargetMemberCount, group.InviteCode))
		if group.Status == "inviting" && group.CreatorUserID == user.ID && group.MemberCount == group.TargetMemberCount {
			lines = append(lines, "  Send: ACTIVATE "+group.InviteCode)
		}
		if group.Status == "active" {
			lines = append(lines, "  To pay this cycle, send: CONTRIBUTE "+group.InviteCode)
		}
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) sendHelp(ctx context.Context, channel, recipient string) error {
	return s.sendText(ctx, channel, recipient,
		"Xego lets you pay merchants, buy mobile data, pay invoices, and use demo thrift contribution groups.\n\nThrift commands:\nJOIN XG-THRIFT-1234ABCD joins an inviting group.\nACTIVATE XG-THRIFT-1234ABCD lets the creator set payout rotation.\nCONTRIBUTE XG-THRIFT-1234ABCD starts this cycle's payment.\n\nYou can also create a thrift group in one message:\nName, Amount, Frequency, Members\nExample: Office Pool, 5000, monthly, 8\n\nFor bank transfer, enter the payment reference exactly in your bank app's narration, remark, or reference field. This helps Xego match the transfer to your payment.\n\nMerchant registration and individual thrift setup use an email confirmation code before collecting higher-trust details.\n\nInvoice items can be sent in bulk. Send one item per line:\nName, Quantity, Price\nExample: Website design, 1, 25000\n\nSMS data requests use: DATA <NETWORK> <PLAN_CODE> <PHONE>. Example: DATA MTN MTN1GB 08031234567.\n\nWe never ask for card details, PINs, OTPs, or CVVs in chat. Type MENU anytime to return to the main menu.")
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

func (s *ConversationService) invoiceDraft(ctx context.Context, user store.User, session store.Session) (store.Merchant, []store.InvoiceItem, int64, error) {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return store.Merchant{}, nil, 0, err
	}
	slug := session.Data["invoice_merchant_slug"]
	var merchant store.Merchant
	found := false
	for _, candidate := range merchants {
		if candidate.Slug == slug {
			merchant = candidate
			found = true
			break
		}
	}
	if !found {
		return store.Merchant{}, nil, 0, fmt.Errorf("merchant not owned by user")
	}
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return store.Merchant{}, nil, 0, fmt.Errorf("missing invoice items")
	}
	fee, _ := strconv.ParseInt(session.Data["invoice_delivery_fee_kobo"], 10, 64)
	return merchant, items, fee, nil
}

func (s *ConversationService) invoicePaymentSession(ctx context.Context, session store.Session) (store.InvoiceView, int64, error) {
	invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
	if err != nil {
		return store.InvoiceView{}, 0, err
	}
	amount, err := strconv.ParseInt(session.Data["invoice_pay_amount_kobo"], 10, 64)
	if err != nil || amount <= 0 {
		return store.InvoiceView{}, 0, fmt.Errorf("invalid invoice payment amount")
	}
	return invoice, amount, nil
}

func (s *ConversationService) thriftContributionFromSession(ctx context.Context, session store.Session) (store.ThriftContributionView, error) {
	id, err := uuid.Parse(session.Data["thrift_contribution_id"])
	if err != nil {
		return store.ThriftContributionView{}, err
	}
	return s.store.ThriftContributionByID(ctx, id)
}

func (s *ConversationService) userIsApprovedIndividual(ctx context.Context, user store.User) bool {
	if user.AccountLevel != "individual" {
		return false
	}
	profile, err := s.store.IndividualProfileByUser(ctx, user.ID)
	return err == nil && profile.KYCStatus == "approved_simulated"
}

func thriftJoinCodeFromInput(input string) (string, bool) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(input)))
	if len(fields) == 2 && fields[0] == "JOIN" {
		return thriftCodeFromText(fields[1])
	}
	if len(fields) == 1 {
		return thriftCodeFromText(fields[0])
	}
	return "", false
}

func thriftActivateCodeFromInput(input string) (string, bool) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(input)))
	if len(fields) == 2 && fields[0] == "ACTIVATE" {
		return thriftCodeFromText(fields[1])
	}
	return "", false
}

func thriftContributeCodeFromInput(input string) (string, bool) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(input)))
	if len(fields) == 2 && fields[0] == "CONTRIBUTE" {
		return thriftCodeFromText(fields[1])
	}
	return "", false
}

func thriftCodeFromText(input string) (string, bool) {
	code := strings.ToUpper(strings.TrimSpace(input))
	code = strings.Trim(code, ".,;: ")
	if strings.HasPrefix(code, "XG-THRIFT-") && len(code) >= len("XG-THRIFT-1234") {
		return code, true
	}
	return "", false
}

func parseRotationIndexes(input string) []int {
	cleaned := strings.NewReplacer(",", " ", ";", " ", "-", " ").Replace(input)
	fields := strings.Fields(cleaned)
	indexes := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		indexes = append(indexes, value)
	}
	return indexes
}

func displayNameOrFallback(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return fallback
}

func invoiceItemsFromSession(session store.Session) ([]store.InvoiceItem, error) {
	raw := strings.TrimSpace(session.Data["invoice_items"])
	if raw == "" {
		raw = "[]"
	}
	var items []store.InvoiceItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func putInvoiceItems(session *store.Session, items []store.InvoiceItem) error {
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.Data["invoice_items"] = string(raw)
	return nil
}

func invoiceSubtotal(items []store.InvoiceItem) int64 {
	total := int64(0)
	for _, item := range items {
		line := item.LineTotalKobo
		if line == 0 {
			line = int64(item.Quantity) * item.UnitPriceKobo
		}
		total += line
	}
	return total
}

func invoiceItemsSummary(prefix string, items []store.InvoiceItem) string {
	lines := []string{prefix}
	for i, item := range items {
		line := item.LineTotalKobo
		if line == 0 {
			line = int64(item.Quantity) * item.UnitPriceKobo
		}
		lines = append(lines, fmt.Sprintf("%d. %s — Qty %d × %s = %s", i+1, item.Description, item.Quantity, domain.FormatNGN(item.UnitPriceKobo), domain.FormatNGN(line)))
	}
	return strings.Join(lines, "\n")
}

func invoiceReferenceFromPAY(input string) (string, bool) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(input)))
	if len(fields) != 2 || fields[0] != "PAY" || !strings.HasPrefix(fields[1], "XG-INV-") {
		return "", false
	}
	return fields[1], true
}

func (s *ConversationService) notifyInvoiceCustomer(ctx context.Context, invoice store.InvoiceView) {
	link := s.cfg.BaseURL + "/invoices/" + invoice.Reference
	body := fmt.Sprintf("Xego invoice from %s\n\nAmount: %s\nReference: %s\nLink: %s\n\nTo pay in WhatsApp, reply with:\nPAY %s\n\nYou may pay the full balance or choose a split/partial amount during payment.",
		invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link, invoice.Reference)
	if s.email != nil && invoice.CustomerEmail != "" {
		_ = s.email.Send(ctx, invoice.CustomerEmail, "Xego invoice "+invoice.Reference, body)
	}
	if invoice.CustomerWhatsAppNumber != "" {
		_ = s.sendText(ctx, ChannelWhatsApp, invoice.CustomerWhatsAppNumber, body)
	}
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

func dataPlanRow(plan store.DataPlan) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "data_plan:" + plan.Code,
		Title:       truncateInteractiveTitle(plan.DisplayName),
		Description: truncateInteractiveDescription(plan.Validity + " - " + domain.FormatNGN(plan.PriceKobo)),
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

func newEmailCode() (string, error) {
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func emailCodeHash(email, code string) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + ":" + normalizeEmailCode(code)))
	return sum[:]
}

func normalizeEmailCode(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
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

type thriftConcatResult struct {
	Name      string
	Amount    string
	Frequency string
	Target    string
	Errors    []string
}

type invoiceBulkItem struct {
	Name     string
	Quantity string
	Price    string
	Errors   []string
}

func parseCommaSeparatedFields(input string) []string {
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseThriftConcatInput(input string, minKobo, maxKobo int64) thriftConcatResult {
	fields := parseCommaSeparatedFields(input)
	result := thriftConcatResult{}

	for _, field := range fields {
		lower := strings.ToLower(field)
		if result.Frequency == "" && (lower == "weekly" || lower == "monthly") {
			result.Frequency = lower
			continue
		}
		if result.Amount == "" {
			if _, err := domain.ParseNGNAmount(field, minKobo, maxKobo); err == nil {
				result.Amount = field
				continue
			}
		}
	}

	var targetCandidates []string
	for _, field := range fields {
		if strings.ToLower(field) == result.Frequency || field == result.Amount {
			continue
		}
		if target, err := strconv.Atoi(field); err == nil && target >= 2 && target <= 12 {
			targetCandidates = append(targetCandidates, field)
		}
	}
	if len(targetCandidates) == 1 {
		result.Target = targetCandidates[0]
	}

	for _, field := range fields {
		if field == result.Frequency || field == result.Amount || field == result.Target {
			continue
		}
		if result.Name == "" {
			result.Name = field
		}
	}

	if result.Name != "" && (len([]rune(result.Name)) < 3 || len([]rune(result.Name)) > 80) {
		result.Errors = append(result.Errors, "Name should be between 3 and 80 characters.")
	}
	if result.Amount != "" {
		if _, err := domain.ParseNGNAmount(result.Amount, minKobo, maxKobo); err != nil {
			result.Errors = append(result.Errors, "Amount: "+err.Error())
		}
	}
	if result.Frequency != "" && result.Frequency != "weekly" && result.Frequency != "monthly" {
		result.Errors = append(result.Errors, "Frequency must be Weekly or Monthly.")
	}
	if result.Target != "" {
		target, err := strconv.Atoi(result.Target)
		if err != nil || target < 2 || target > 12 {
			result.Errors = append(result.Errors, "Member count should be between 2 and 12.")
		}
	}

	return result
}

func parseInvoiceSingleItemConcat(input string, maxPriceKobo int64) (name, qtyStr, priceStr string, ok bool) {
	fields := parseCommaSeparatedFields(input)
	if len(fields) < 3 {
		return "", "", "", false
	}

	priceStr = fields[len(fields)-1]
	qtyStr = fields[len(fields)-2]

	qty, err := strconv.Atoi(qtyStr)
	if err != nil || qty < 1 || qty > 1000 {
		return "", "", "", false
	}

	if _, err := domain.ParseNGNAmount(priceStr, 100, maxPriceKobo); err != nil {
		return "", "", "", false
	}

	name = strings.Join(fields[:len(fields)-2], ", ")
	if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
		return "", "", "", false
	}

	return name, qtyStr, priceStr, true
}

func parseInvoiceBulkItems(input string, maxPriceKobo int64) []invoiceBulkItem {
	lines := strings.Split(input, "\n")
	var items []invoiceBulkItem
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := parseCommaSeparatedFields(line)
		if len(fields) < 1 {
			continue
		}
		item := invoiceBulkItem{}

		if len(fields) >= 3 {
			item.Price = fields[len(fields)-1]
			item.Quantity = fields[len(fields)-2]
			item.Name = strings.Join(fields[:len(fields)-2], ", ")
		} else if len(fields) == 2 {
			item.Name = fields[0]
			item.Quantity = fields[1]
		} else {
			item.Name = fields[0]
		}

		if len([]rune(item.Name)) < 2 || len([]rune(item.Name)) > 120 {
			item.Errors = append(item.Errors, "Item name should be between 2 and 120 characters.")
		}
		if item.Quantity != "" {
			qty, err := strconv.Atoi(item.Quantity)
			if err != nil || qty < 1 || qty > 1000 {
				item.Errors = append(item.Errors, "Quantity should be between 1 and 1000.")
			}
		}
		if item.Price != "" {
			if _, err := domain.ParseNGNAmount(item.Price, 100, maxPriceKobo); err != nil {
				item.Errors = append(item.Errors, "Price: "+err.Error())
			}
		}

		items = append(items, item)
	}
	return items
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
