package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/chatguard"
	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
)

const (
	// ChannelWhatsApp identifies customer conversations received through WhatsApp Cloud API.
	ChannelWhatsApp = "whatsapp"
	// ChannelTelegram identifies customer conversations received through Telegram Bot API.
	ChannelTelegram = "telegram"
	// ChannelInstagram identifies customer conversations received through the
	// Instagram Messaging API (Meta Graph, IGSID identity).
	ChannelInstagram = "instagram"
	// ChannelTikTok identifies customer conversations received through the
	// TikTok Business Messaging API (open_id/union_id identity).
	ChannelTikTok = "tiktok"
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

	identityProviderName  string
	screeningProviderName string

	// AI/media providers (nil when AI_ENABLED=false).
	imageReader  ports.ImageReader
	speechToText ports.SpeechToText
	chatAI       ports.ChatAI

	// mediaDownloader fetches raw chat-media bytes before OCR/STT so the AI
	// providers receive real payloads instead of nil data.
	mediaDownloader ports.MediaDownloader

	// AIMaxRPM enforcement: AI calls are throttled per conversation key
	// (recipient/ID). nil limiter or maxRPM <= 0 leaves AI calls unlimited.
	aiLimiter ratelimit.Limiter
	aiMaxRPM  int

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
	return &ConversationService{cfg: cfg, store: repository, payments: payments, data: data, messengers: messengers, email: email, identity: identity, screener: screener, identityProviderName: pName, screeningProviderName: pName, acceptedNumbers: accepted}
}

// SetScreeningProviderName overrides the provider label recorded with
// sanctions/PEP screening results. Defaults to the identity provider name so
// existing callers keep their current audit label until explicitly wired.
func (s *ConversationService) SetScreeningProviderName(name string) {
	if strings.TrimSpace(name) != "" {
		s.screeningProviderName = name
	}
}

// AIUsageReporter is implemented by AI providers that can report the token
// usage of their most recent call (e.g. the OpenAI adapter), so extraction
// spend can be attributed per channel in the admin media report.
type AIUsageReporter interface {
	LastPromptTokens() int64
}

// SetMediaProviders configures the optional AI/media providers for image-to-text,
// speech-to-text, and AI conversation support.
func (s *ConversationService) SetMediaProviders(imageReader ports.ImageReader, speechToText ports.SpeechToText, chatAI ports.ChatAI) {
	s.imageReader = imageReader
	s.speechToText = speechToText
	s.chatAI = chatAI
}

// SetAIRateLimiter enforces the AI requests-per-minute cap per conversation
// key (the recipient address, so one customer cannot saturate the AI budget).
// A nil limiter or maxRPM <= 0 leaves AI calls unlimited (safe for tests and
// the simulated provider).
func (s *ConversationService) SetAIRateLimiter(limiter ratelimit.Limiter, maxRPM int) {
	s.aiLimiter = limiter
	s.aiMaxRPM = maxRPM
}

// SetMediaDownloader configures the channel provider used to fetch raw media
// bytes for OCR/STT. Without it, chat media degrades to empty text.
func (s *ConversationService) SetMediaDownloader(downloader ports.MediaDownloader) {
	s.mediaDownloader = downloader
}

// aiAllowed reports whether the next AI call for key is within the configured
// requests-per-minute cap. Fail-open: when no limiter is wired the call is
// allowed, and rate-limit storage errors never block a customer.
func (s *ConversationService) aiAllowed(ctx context.Context, key string) bool {
	if s.aiLimiter == nil || s.aiMaxRPM <= 0 {
		return true
	}
	allowed, _ := s.aiLimiter.Allow(ctx, "ai:"+key, s.aiMaxRPM, time.Minute)
	return allowed
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
	// Tag this request with the messaging-cost flow name so outbound messages
	// are attributed to the right flow in message_log.
	ctx = withMessageFlow(ctx, flowForState(session.State))
	input := strings.TrimSpace(message.Text)
	if message.Interactive != "" {
		input = message.Interactive
	}

	// Pre-process media: extract text from images (OCR) and audio (STT).
	// This converts media messages into text before the FSM sees them.
	if message.MediaType != "" && (message.MediaID != "" || message.MediaURL != "") && input == "" {
		input = s.processMedia(ctx, message, "media:"+user.ID.String())
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
	// menu and cancel are global interrupts: they work in every session,
	// including the onboarding/registration states gated below, so a customer
	// stuck mid-signup (or parked on a browser flow whose page was closed) can
	// always bail out.
	if strings.EqualFold(input, "menu") || strings.EqualFold(input, "/menu") {
		s.abandonSessionPayment(ctx, user, session)
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, message.Channel, recipient, user)
	}
	if strings.EqualFold(input, "cancel") || input == "cancel_payment" {
		return s.handleCancel(ctx, message.Channel, recipient, user, session)
	}
	if strings.EqualFold(input, "help") || strings.EqualFold(input, "/help") {
		return s.sendHelp(ctx, message.Channel, recipient)
	}
	// Cross-channel identity carry-over: a customer whose global identity
	// already cleared the money-out tier is never re-hijacked into per-channel
	// onboarding on a fresh channel (telegram/instagram/tiktok). Their KYC
	// tier is global, so a new channel gets a one-time non-blocking intro and
	// the menu, instead of being forced through account confirmation again.
	// Only users who have NOT yet reached an approved global tier still go
	// through per-channel onboarding.
	if !s.onboardingCompleteForChannel(user, message.Channel) {
		if s.userIsApprovedIndividual(ctx, user) {
			return s.handleNewChannelForApprovedUser(ctx, message.Channel, recipient, user, session)
		}
		return s.handleOnboarding(ctx, message.Channel, recipient, user, session, input)
	}
	// A brand-new service request while another session is already active
	// asks the customer whether to switch (abandoning the current one) or
	// stay. Both free-text commands and interactive menu row selections are
	// recognized so the duplicate-session confusion stays impossible.
	if sessionSwitchable(session.State) {
		if interruptType, interruptArg := serviceSwitchIntent(input); interruptType != "" {
			flowDesc := describeCurrentFlow(session)
			prevData, _ := json.Marshal(session.Data)
			prevState := session.State
			session.State = "confirm_session_switch"
			session.Data = map[string]string{
				"pending_type":    interruptType,
				"pending_arg":     interruptArg,
				"pending_command": input,
				"prev_state":      prevState,
				"prev_data":       string(prevData),
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
	case "onboard_name", "onboard_email", "onboard_email_code":
		// Reachable via the optional "Complete profile" menu row after
		// first contact auto-confirms the WhatsApp number. Incomplete
		// onboarding still gates earlier in Handle for legacy users.
		return s.handleOnboarding(ctx, message.Channel, recipient, user, session, input)
	case "onboard_confirm_account":
		return s.handleAccountConfirmation(ctx, message.Channel, recipient, user, session, input)
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
	case "confirm_payment":
		return s.handleConfirmation(ctx, message.Channel, recipient, user, session, input)
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
	case "kyb_request_select_merchant":
		return s.handleKYBRequestSelectMerchant(ctx, message.Channel, recipient, user, session, input)
	case "kyb_request_confirm":
		return s.handleKYBRequestConfirm(ctx, message.Channel, recipient, user, session, input)
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
	case "pay_individual_phone":
		return s.handlePayIndividualPhone(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_amount":
		return s.handlePayIndividualAmount(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_bank_code":
		return s.handlePayIndividualBankCode(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_bank_pick":
		return s.handlePayIndividualBankPick(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_account":
		return s.handlePayIndividualAccount(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_confirm":
		return s.handlePayIndividualConfirm(ctx, message.Channel, recipient, user, session, input)
	case "pay_individual_method":
		return s.handlePayIndividualMethod(ctx, message.Channel, recipient, user, session, input)
	case "await_individual_bank_transfer":
		return s.handleAwaitIndividualBankTransfer(ctx, message.Channel, recipient, user, session, input)
	case "await_individual_payment":
		// Card checkout: webhook will complete. Acknowledge if user sends text.
		return s.sendText(ctx, message.Channel, recipient, "Your card payment is being processed. We'll notify you when it's complete.")
	case "wallet_topup_amount":
		return s.handleWalletTopupAmount(ctx, message.Channel, recipient, user, session, input)
	case "wallet_topup_method":
		return s.handleWalletTopupMethod(ctx, message.Channel, recipient, user, session, input)
	case "web_flow_active":
		// The customer is completing a flow in the browser; acknowledge chat
		// input without restarting anything. MENU resets the session and
		// CANCEL additionally abandons the open browser flow.
		return s.sendText(ctx, message.Channel, recipient,
			"You're completing this in your browser — tap the link we sent to continue, or type CANCEL to stop.")
	case "confirm_session_switch":
		return s.handleSessionSwitchConfirm(ctx, message.Channel, recipient, user, session, input)
	case "ai_assistant":
		return s.handleAIAssistant(ctx, message.Channel, recipient, user, session, input)
	case "link_phone":
		return s.handleLinkPhone(ctx, message.Channel, recipient, user, session, input)
	case "link_code":
		return s.handleLinkCode(ctx, message.Channel, recipient, user, session, input)
	default:
		// Deterministic payment-instruction parsing first: free-text
		// instructions ("send 5000 to 08039999900 GTBank 0123456789",
		// "faya 2000 to Ada") arrive as chat text, OCR'd photo text, or a
		// transcribed voice note and are routed without the AI provider.
		// Interactive selections carry explicit IDs and must never be parsed;
		// per-step media is consumed by the state-specific cases above, so
		// only a fresh whole instruction reaches this branch.
		if message.Interactive == "" {
			hint, err := s.ParseIndividualPayText(ctx, input)
			if err != nil {
				return err
			}
			if hint.Routeable() {
				return s.routePaymentHint(ctx, message.Channel, recipient, user, session, hint)
			}
		}
		// AI intent routing: when enabled and a chat AI is available,
		// try to classify free-text input before falling back to keyword
		// matching.  Interactive selections (list row taps, button presses)
		// carry explicit IDs that are already handled by the state-specific
		// cases above and by handleMenu, so they must never be classified.
		if s.chatAI != nil && s.cfg.AIEnabled && message.Interactive == "" && s.aiAllowed(ctx, "intent:"+recipient) {
			intent, err := s.chatAI.ClassifyIntent(ctx, input, nil)
			if err == nil && intent.Confidence >= 0.7 && intent.Intent != "none" {
				return s.routeAIIntent(ctx, message.Channel, recipient, user, session, intent)
			}
		}
		return s.handleMenu(ctx, message.Channel, recipient, user, session, input)
	}
}

func (s *ConversationService) resolveUser(ctx context.Context, message store.InboundMessage) (store.User, string, error) {
	// Every resolve path returns the surviving primary account: a channel row
	// that was merged into a WhatsApp account via account linking is a
	// tombstone (merged_into_id set) and all reads follow the primary so
	// sessions, payments, and wallet stay on the surviving row.
	var (
		user      store.User
		recipient string
		err       error
	)
	switch message.Channel {
	case ChannelTelegram:
		recipient = strings.TrimSpace(message.Recipient)
		if recipient == "" {
			recipient = strings.TrimSpace(message.Sender)
		}
		user, err = s.store.GetOrCreateTelegramUser(ctx, recipient, message.Sender, message.Username)
	case ChannelInstagram:
		igsid := strings.TrimSpace(message.Sender)
		user, err = s.store.GetOrCreateInstagramUser(ctx, igsid, message.Username)
		recipient = igsid
	case ChannelTikTok:
		openID := strings.TrimSpace(message.Sender)
		// Identity is keyed on the open_id; the union_id is kept for cross-app
		// resolution. The conversation_id travels as message.Recipient and is
		// the provider address outbound sends reply to.
		user, err = s.store.GetOrCreateTikTokUser(ctx, openID, message.UnionID, message.Username)
		recipient = strings.TrimSpace(message.Recipient)
		if recipient == "" {
			recipient = openID
		}
	default:
		number := normalizePhone(message.Sender)
		user, err = s.store.GetOrCreateUser(ctx, number)
		recipient = number
	}
	if err != nil {
		return store.User{}, "", err
	}
	if user.MergedIntoID.Valid {
		primary, err := s.store.ResolvePrimaryUser(ctx, user.ID)
		if err != nil {
			return store.User{}, "", err
		}
		return primary, recipient, nil
	}
	return user, recipient, nil
}

func (s *ConversationService) onboardingCompleteForChannel(user store.User, channel string) bool {
	if !user.OnboardingComplete {
		return false
	}
	switch channel {
	case ChannelTelegram:
		return user.TelegramConfirmedAt.Valid
	case ChannelInstagram:
		return user.InstagramConfirmedAt.Valid
	case ChannelTikTok:
		return user.TikTokConfirmedAt.Valid
	}
	return user.NumberConfirmedAt.Valid
}

func (s *ConversationService) sendText(ctx context.Context, channel, recipient, body string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	if err := messenger.SendText(ctx, recipient, body); err != nil {
		return err
	}
	s.recordMessage(ctx, channel, recipient, "text")
	return nil
}

func (s *ConversationService) sendImage(ctx context.Context, channel, recipient string, imageData []byte, caption string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	if err := messenger.SendImage(ctx, recipient, imageData, caption); err != nil {
		return err
	}
	s.recordMessage(ctx, channel, recipient, "image")
	return nil
}

func (s *ConversationService) sendInteractive(ctx context.Context, channel string, message ports.InteractiveMessage) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	if err := messenger.SendInteractive(ctx, message); err != nil {
		return err
	}
	s.recordMessage(ctx, channel, message.To, "interactive")
	return nil
}

func (s *ConversationService) sendCheckout(ctx context.Context, channel, recipient, body, url string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	if err := messenger.SendCheckout(ctx, recipient, body, url); err != nil {
		return err
	}
	s.recordMessage(ctx, channel, recipient, "checkout")
	return nil
}

// sendLink sends the single message-1 link of a web flow (custom button
// label) and records it against the messaging cost meter.
func (s *ConversationService) sendLink(ctx context.Context, channel, recipient, body, url, label string) error {
	messenger, err := s.messengerFor(channel)
	if err != nil {
		return err
	}
	if err := messenger.SendLink(ctx, recipient, body, url, label); err != nil {
		return err
	}
	s.recordMessage(ctx, channel, recipient, "link")
	return nil
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
	if strings.EqualFold(channel, ChannelInstagram) {
		return ChannelInstagram
	}
	if strings.EqualFold(channel, ChannelTikTok) {
		return ChannelTikTok
	}
	return ChannelWhatsApp
}

// processMedia extracts text from media messages using OCR (images) or STT (audio).
// The provider floor (imageReader/speechToText) and AI rate cap are checked
// before any download so throttled or disabled AI never wastes a download.
// Returns empty string if the media cannot be fetched or processed — the
// caller then falls back to keyword handling.
func (s *ConversationService) processMedia(ctx context.Context, message store.InboundMessage, rateKey string) string {
	// Every inbound media message of a supported kind counts as one extraction
	// attempt in the admin channel-media report, whether or not the AI floor
	// let it through. Rows without a provider media identity still get a row,
	// keyed by the synthetic ID the enqueue path would have carried.
	mediaID := message.MediaID
	if mediaID == "" && message.MediaURL != "" {
		mediaID = message.MediaURL
	}
	if mediaID == "" {
		mediaID = "media-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	reportOutcome := func(ok bool, tokens int64) {
		// Best-effort observability write: a failed tally never blocks the
		// customer's message from being processed.
		if s.store != nil {
			_ = s.store.RecordChatMediaOutcome(ctx, mediaID, ok, tokens)
		}
	}
	switch message.MediaType {
	case "image", "photo", "document":
		if s.imageReader == nil || !s.aiAllowed(ctx, rateKey) {
			reportOutcome(false, 0)
			return ""
		}
	case "audio", "voice":
		if s.speechToText == nil || !s.aiAllowed(ctx, rateKey) {
			reportOutcome(false, 0)
			return ""
		}
	default:
		return ""
	}
	if s.mediaDownloader == nil {
		reportOutcome(false, 0)
		return ""
	}
	data, mime, err := s.mediaDownloader.Download(ctx, message.Channel, message.MediaID, message.MediaURL)
	if err != nil || len(data) == 0 {
		reportOutcome(false, 0)
		return ""
	}
	if mime == "" {
		mime = message.MediaMime
	}
	switch message.MediaType {
	case "image", "photo", "document":
		// For documents, we still try OCR if it looks like an image MIME type.
		if message.MediaType == "document" && !strings.HasPrefix(mime, "image/") {
			reportOutcome(false, 0)
			return ""
		}
		prompt := "Extract all readable text from this image."
		if strings.Contains(strings.ToLower(message.Caption), "receipt") || strings.Contains(strings.ToLower(message.Caption), "proof") || strings.Contains(strings.ToLower(message.Caption), "transfer") {
			prompt = "Extract the payment reference number, amount, date, sender, and recipient from this transfer receipt."
		} else if strings.Contains(strings.ToLower(message.Caption), "nin") || strings.Contains(strings.ToLower(message.Caption), "bvn") || strings.Contains(strings.ToLower(message.Caption), "slip") {
			prompt = "Extract the NIN or BVN number from this identity slip."
		}
		text, err := s.imageReader.ReadImage(ctx, data, mime, prompt)
		tokens := int64(0)
		if reporter, ok := s.imageReader.(AIUsageReporter); ok {
			tokens = reporter.LastPromptTokens()
		}
		if err != nil {
			reportOutcome(false, tokens)
			return ""
		}
		text = strings.TrimSpace(text)
		reportOutcome(text != "", tokens)
		if text == "" {
			return ""
		}
		return text

	case "audio", "voice":
		text, err := s.speechToText.Transcribe(ctx, data, mime, "en")
		tokens := int64(0)
		if reporter, ok := s.speechToText.(AIUsageReporter); ok {
			tokens = reporter.LastPromptTokens()
		}
		if err != nil {
			reportOutcome(false, tokens)
			return ""
		}
		text = strings.TrimSpace(text)
		reportOutcome(text != "", tokens)
		if text == "" {
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
		return s.sendMenu(ctx, channel, recipient, user)
	}
	if s.chatAI == nil {
		return s.sendText(ctx, channel, recipient, "AI assistant is not available right now. Type MENU to see your options.")
	}
	if !s.aiAllowed(ctx, "assistant:"+recipient) {
		return s.sendText(ctx, channel, recipient, "I'm a little busy right now. Try again in a moment, or type MENU to see your options.")
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
	case "pay_individual":
		// AI-fallback route for instructions the deterministic parser could
		// not classify: carry any extracted entities into the individual
		// flow's prefill and prompt only for what is still missing.
		return s.startPayIndividualFromHint(ctx, channel, recipient, user, session, s.intentToHint(ctx, intent.Entities))
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
		return s.sendMenu(ctx, channel, recipient, user)
	case "help":
		return s.sendHelp(ctx, channel, recipient)
	case "status":
		return s.sendLatestStatus(ctx, channel, recipient, user)
	case "cancel":
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient, user)
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
