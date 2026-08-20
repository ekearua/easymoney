package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/chatguard"
	"whatsapp-payment-demo/internal/config"
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
	// ChannelAPI identifies payments initiated through the merchant Partner API.
	ChannelAPI = "api"
	// ChannelCheckout identifies payments initiated from a general request-money link.
	ChannelCheckout = "checkout"

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
	email      ports.EmailSender
	identity   ports.IdentityVerifier
	screener   ports.SanctionsScreener

	identityProviderName string

	// AI/media providers (nil when AI_ENABLED=false).
	imageReader  ports.ImageReader
	speechToText ports.SpeechToText
	chatAI       ports.ChatAI

	acceptedMu      sync.RWMutex
	acceptedNumbers map[string]bool
}

// NewConversationService constructs the customer-facing workflow.
func NewConversationService(cfg config.Config, repository *store.Store, payments *PaymentService, data *DataService, messengers map[string]ports.Messenger, email ports.EmailSender, identity ports.IdentityVerifier, screener ports.SanctionsScreener, identityProviderName ...string) *ConversationService {
	accepted := make(map[string]bool, len(cfg.InvoiceAcceptedNumbers))
	for _, n := range cfg.InvoiceAcceptedNumbers {
		accepted[n] = true
	}
	pName := "simulated"
	if len(identityProviderName) > 0 && identityProviderName[0] != "" {
		pName = identityProviderName[0]
	}
	return &ConversationService{cfg: cfg, store: repository, payments: payments, data: data, messengers: messengers, email: email, identity: identity, screener: screener, identityProviderName: pName, acceptedNumbers: accepted}
}

// SetMediaProviders configures the optional AI/media providers for image-to-text,
// speech-to-text, and AI conversation support.
func (s *ConversationService) SetMediaProviders(imageReader ports.ImageReader, speechToText ports.SpeechToText, chatAI ports.ChatAI) {
	s.imageReader = imageReader
	s.speechToText = speechToText
	s.chatAI = chatAI
}

// AcceptedInvoiceNumbers returns the current list of accepted customer phone numbers.
func (s *ConversationService) AcceptedInvoiceNumbers() []string {
	s.acceptedMu.RLock()
	defer s.acceptedMu.RUnlock()
	out := make([]string, 0, len(s.acceptedNumbers))
	for n := range s.acceptedNumbers {
		out = append(out, n)
	}
	return out
}

// SetAcceptedInvoiceNumbers replaces the accepted customer phone number list.
func (s *ConversationService) SetAcceptedInvoiceNumbers(numbers []string) {
	s.acceptedMu.Lock()
	defer s.acceptedMu.Unlock()
	s.acceptedNumbers = make(map[string]bool, len(numbers))
	for _, n := range numbers {
		s.acceptedNumbers[n] = true
	}
}

func (s *ConversationService) isAcceptedInvoiceNumber(phone string) bool {
	s.acceptedMu.RLock()
	defer s.acceptedMu.RUnlock()
	if len(s.acceptedNumbers) == 0 {
		return true
	}
	return s.acceptedNumbers[phone]
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

	// Pre-process media: extract text from images (OCR) and audio (STT).
	// This converts media messages into text before the FSM sees them.
	if message.MediaType != "" && message.MediaID != "" && input == "" {
		input = s.processMedia(ctx, message)
	}

	// C18 chat content guard: Xego never asks for card numbers, PINs, CVVs, or
	// OTPs, so any inbound message carrying them is refused, the customer is
	// told why, and the attempt is logged with the credential redacted.
	if result := chatguard.Inspect(input); result.Blocked {
		s.blockPaymentCredentialMessage(ctx, message, recipient, result)
		return nil
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
	if isInterruptibleState(session.State) {
		if interruptType, interruptArg := isPaymentInterrupt(input); interruptType != "" {
			flowDesc := describeCurrentFlow(session)
			prevData, _ := json.Marshal(session.Data)
			prevState := session.State
			session.State = "confirm_session_switch"
			session.Data = map[string]string{
				"pending_type": interruptType,
				"pending_arg":  interruptArg,
				"prev_state":   prevState,
				"prev_data":    string(prevData),
			}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendInteractive(ctx, message.Channel, ports.InteractiveMessage{
				To:   recipient,
				Body: fmt.Sprintf("You're currently %s.\n\nSwitch to something else?", flowDesc),
				Buttons: []ports.InteractiveButton{
					{ID: "switch_yes", Title: "Switch"},
					{ID: "switch_no", Title: "Continue current"},
				},
			})
		}
	}

	switch session.State {
	case "select_merchant":
		return s.handleMerchant(ctx, message.Channel, recipient, user, session, input)
	case "select_service_or_amount":
		return s.handleServiceOrAmount(ctx, message.Channel, recipient, user, session, input)
	case "select_event_tier":
		return s.handleEventTierSelection(ctx, message.Channel, recipient, user, session, input)
	case "enter_service_quantity":
		return s.handleServiceQuantity(ctx, message.Channel, recipient, user, session, input)
	case "confirm_service_purchase":
		return s.handleConfirmServicePurchase(ctx, message.Channel, recipient, user, session, input)
	case "collect_custom_fields":
		return s.handleCollectCustomFields(ctx, message.Channel, recipient, user, session, input)
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
	case "individual_id_number":
		return s.handleIndividualIDNumber(ctx, message.Channel, recipient, user, session, input)
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
	case "thrift_edit_field":
		return s.handleThriftEditField(ctx, message.Channel, recipient, user, session, input)
	case "thrift_edit_amount_value":
		return s.handleThriftEditAmountValue(ctx, message.Channel, recipient, user, session, input)
	case "thrift_edit_frequency_value":
		return s.handleThriftEditFrequencyValue(ctx, message.Channel, recipient, user, session, input)
	case "thrift_edit_members_value":
		return s.handleThriftEditMembersValue(ctx, message.Channel, recipient, user, session, input)
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
	case "invoice_edit_item":
		return s.handleInvoiceEditItem(ctx, message.Channel, recipient, user, session, input)
	case "invoice_edit_field":
		return s.handleInvoiceEditField(ctx, message.Channel, recipient, user, session, input)
	case "invoice_edit_value":
		return s.handleInvoiceEditValue(ctx, message.Channel, recipient, user, session, input)
	case "invoice_remove_item":
		return s.handleInvoiceRemoveItem(ctx, message.Channel, recipient, user, session, input)
	case "invoice_delivery_fee":
		return s.handleInvoiceDeliveryFee(ctx, message.Channel, recipient, user, session, input)
	case "invoice_due_date":
		return s.handleInvoiceDueDate(ctx, message.Channel, recipient, user, session, input)
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
	case "confirm_session_switch":
		return s.handleSessionSwitchConfirm(ctx, message.Channel, recipient, user, session, input)
	case "ai_assistant":
		return s.handleAIAssistant(ctx, message.Channel, recipient, user, session, input)
	default:
		// AI intent routing: when enabled and a chat AI is available,
		// try to classify the input before falling back to keyword matching.
		if s.chatAI != nil && s.cfg.AIEnabled {
			intent, err := s.chatAI.ClassifyIntent(ctx, input, nil)
			if err == nil && intent.Confidence >= 0.7 && intent.Intent != "none" {
				return s.routeAIIntent(ctx, message.Channel, recipient, user, session, intent)
			}
		}
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

func (s *ConversationService) sendText(ctx context.Context, channel, recipient, body string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	return messenger.SendText(ctx, recipient, body)
}

func (s *ConversationService) sendImage(ctx context.Context, channel, recipient string, imageData []byte, caption string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	return messenger.SendImage(ctx, recipient, imageData, caption)
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

// processMedia extracts text from media messages using OCR (images) or STT (audio).
// Returns empty string if the provider is unavailable or processing fails.
func (s *ConversationService) processMedia(ctx context.Context, message store.InboundMessage) string {
	switch message.MediaType {
	case "image", "photo", "document":
		if s.imageReader == nil {
			return ""
		}
		// For documents, we still try OCR if it looks like an image MIME type.
		if message.MediaType == "document" && !strings.HasPrefix(message.MediaMime, "image/") {
			return ""
		}
		prompt := "Extract all readable text from this image."
		if strings.Contains(strings.ToLower(message.Caption), "receipt") || strings.Contains(strings.ToLower(message.Caption), "proof") || strings.Contains(strings.ToLower(message.Caption), "transfer") {
			prompt = "Extract the payment reference number, amount, date, sender, and recipient from this transfer receipt."
		} else if strings.Contains(strings.ToLower(message.Caption), "nin") || strings.Contains(strings.ToLower(message.Caption), "bvn") || strings.Contains(strings.ToLower(message.Caption), "slip") {
			prompt = "Extract the NIN or BVN number from this identity slip."
		}
		text, err := s.imageReader.ReadImage(ctx, nil, message.MediaMime, prompt)
		if err != nil {
			return ""
		}
		return text

	case "audio", "voice":
		if s.speechToText == nil {
			return ""
		}
		text, err := s.speechToText.Transcribe(ctx, nil, message.MediaMime, "en")
		if err != nil {
			return ""
		}
		return text
	}
	return ""
}

// handleAIAssistant processes free-text questions via the AI chat provider.
func (s *ConversationService) handleAIAssistant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.EqualFold(input, "exit") || strings.EqualFold(input, "menu") || strings.EqualFold(input, "back") {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient)
	}
	if s.chatAI == nil {
		return s.sendText(ctx, channel, recipient, "AI assistant is not available right now. Type MENU to see your options.")
	}
	answer, err := s.chatAI.Answer(ctx, input, nil)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't process that right now. Type MENU to see your options.")
	}
	if answer == "" {
		answer = "I'm not sure how to help with that. Type MENU to see your options."
	}
	return s.sendText(ctx, channel, recipient, answer)
}

// routeAIIntent maps an AI-classified intent to an existing FSM state.
func (s *ConversationService) routeAIIntent(ctx context.Context, channel, recipient string, user store.User, session store.Session, intent ports.IntentResult) error {
	switch intent.Intent {
	case "pay":
		session.State = "select_merchant"
		session.Data = map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	case "buy_data":
		session.State = "select_data_network"
		session.Data = map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendDataNetworks(ctx, channel, recipient)
	case "become_individual":
		return s.startIndividualUpgrade(ctx, channel, recipient, user, session)
	case "thrift":
		session.State = "menu"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "To create a thrift group, choose Create thrift from the menu.")
	case "invoice":
		session.State = "menu"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "To create an invoice, choose Create invoice from the merchant menu.")
	case "verify_id":
		session.State = "individual_id_number"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send your NIN or BVN as: NIN <11-digit number> or BVN <11-digit number>")
	case "menu":
		return s.sendMenu(ctx, channel, recipient)
	case "help":
		return s.sendHelp(ctx, channel, recipient)
	case "cancel":
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient)
	case "ai":
		session.State = "ai_assistant"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "I'm Xego's AI assistant. Ask me anything about payments, data, or thrift groups. Type MENU to exit.")
	default:
		return s.handleMenu(ctx, channel, recipient, user, session, "")
	}
}
