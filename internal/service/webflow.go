package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// ErrWebFlowsDisabled reports that a flow keeps the chat FSM (channel or flow
// excluded from web flows). Callers fall back to the chat conversation.
var ErrWebFlowsDisabled = errors.New("web flows disabled for this flow")

// Flow type identifiers shared between chat dispatch and web handlers.
const (
	WebFlowPay               = "pay"
	WebFlowInvoiceCreate     = "invoice_create"
	WebFlowPayInvoice        = "pay_invoice"
	WebFlowThriftCreate      = "thrift_create"
	WebFlowThriftJoin        = "thrift_join"
	WebFlowThriftContribute  = "thrift_contribute"
	WebFlowData              = "data"
	WebFlowIndividualPay     = "individual_pay"
	WebFlowTopup             = "topup"
	WebFlowOnboard           = "onboard"
	WebFlowIndividualUpgrade = "individual_upgrade"
	WebFlowMerchantRegister  = "merchant_register"
	WebFlowKYBRequest        = "kyb_request"
)

// webFlowMeta is the message-1 copy and catalog for each web flow.
type webFlowMeta struct {
	intro string
	label string
}

var webFlowCatalog = map[string]webFlowMeta{
	WebFlowPay:               {intro: "Pay a merchant securely — choose who to pay in your browser.", label: "Open payments"},
	WebFlowInvoiceCreate:     {intro: "Create an invoice for your customer in your browser.", label: "Create invoice"},
	WebFlowPayInvoice:        {intro: "Pay your Xego invoice in your browser.", label: "Pay invoice"},
	WebFlowThriftCreate:      {intro: "Set up a thrift contribution group in your browser.", label: "Create thrift"},
	WebFlowThriftJoin:        {intro: "Join a thrift contribution group in your browser.", label: "Join thrift"},
	WebFlowThriftContribute:  {intro: "Pay your thrift contribution in your browser.", label: "Pay contribution"},
	WebFlowData:              {intro: "Buy mobile data — pick a network and plan in your browser.", label: "Buy data"},
	WebFlowIndividualPay:     {intro: "Send money to a Nigerian bank account in your browser.", label: "Send money"},
	WebFlowTopup:             {intro: "Top up your Xego wallet in your browser.", label: "Top up"},
	WebFlowOnboard:           {intro: "Finish setting up your Xego profile in your browser.", label: "Complete profile"},
	WebFlowIndividualUpgrade: {intro: "Verify your Xego individual profile in your browser.", label: "Verify profile"},
	WebFlowMerchantRegister:  {intro: "Register your business with Xego in your browser.", label: "Register business"},
	WebFlowKYBRequest:        {intro: "Request a higher business tier in your browser.", label: "Request upgrade"},
}

// WebFlowEnabled reports whether the given channel+flow pair should use the
// browser flow. WhatsApp and Instagram support the button link message, so
// both get full web flows; Telegram and TikTok keep the chat FSM (Telegram by
// rollout decision, TikTok because its DMs restrict external link buttons),
// per config, per-flow override allowed.
func (s *ConversationService) WebFlowEnabled(channel, flowType string) bool {
	if !s.cfg.WebFlowsEnabled {
		return false
	}
	if channel != ChannelWhatsApp && channel != ChannelInstagram {
		return false
	}
	if _, ok := webFlowCatalog[flowType]; !ok {
		return false
	}
	for _, disabled := range s.cfg.WebFlowsDisabledFlows {
		if disabled == flowType {
			return false
		}
	}
	return true
}

// StartWebFlow is message 1 of the two-message web-flow pattern: it mints (or
// reuses) an open browser flow for the user, parks the chat session in
// web_flow_active, and sends the single link message. intro/label override
// the catalog copy when non-empty; payload seeds the flow's first step.
func (s *ConversationService) StartWebFlow(ctx context.Context, channel, recipient string, user store.User, session store.Session, flowType string, intro, label string, payload map[string]string) error {
	if !s.WebFlowEnabled(channel, flowType) {
		return ErrWebFlowsDisabled
	}
	meta, ok := webFlowCatalog[flowType]
	if !ok {
		meta = webFlowMeta{intro: "Complete your Xego request in your browser.", label: "Open"}
	}
	if intro == "" {
		intro = meta.intro
	}
	if label == "" {
		label = meta.label
	}
	token := ""
	if flow, found, err := s.store.OpenWebFlowForUser(ctx, user.ID, flowType); err != nil {
		return err
	} else if found {
		token = flow.Token
		// A targeted restart (e.g. PAY <ref> for a different invoice) must
		// adopt the new target: replace the payload and reset the step, or
		// the customer would keep the previous intent's seeded data.
		if payload != nil {
			if err := s.store.SaveWebFlowProgress(ctx, flow.Token, "", payload); err != nil {
				return err
			}
		}
	} else {
		if payload == nil {
			payload = map[string]string{}
		}
		flow, err := s.store.MintWebFlow(ctx, user.ID, channel, flowType, payload, "", s.cfg.SessionTTL)
		if err != nil {
			return err
		}
		token = flow.Token
	}
	session.State = "web_flow_active"
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.Data["web_flow_token"] = token
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	ctx = withMessageFlow(ctx, flowType)
	return s.sendLink(ctx, channel, recipient, intro, s.cfg.BaseURL+"/w/"+token, label)
}

// FinishWebFlow is message 2 of the pattern. It atomically claims the flow
// (open -> complete) and only the claimer sends the confirmation, so a
// double submit or a gateway callback racing the webhook can never send the
// completion twice. The user's chat session is reset to the menu only when it
// is still parked on this flow.
func (s *ConversationService) FinishWebFlow(ctx context.Context, flow store.WebFlow, message string) error {
	claimedFlow, claimed, err := s.store.CompleteWebFlow(ctx, flow.Token)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	recipient, err := s.store.UserContactForChannel(ctx, claimedFlow.UserID, claimedFlow.Channel)
	if err != nil || recipient == "" {
		if err != nil {
			slog.Warn("web flow completion: resolve recipient", "flow_id", claimedFlow.ID, "error", err)
		}
		return nil
	}
	// Only clear the session when it is still parked on this flow, so an
	// unrelated later conversation is never clobbered by a stale completion.
	if session, err := s.store.LoadSession(ctx, claimedFlow.UserID); err == nil && session.State == "web_flow_active" && session.Data["web_flow_token"] == claimedFlow.Token {
		session.State = "menu"
		session.Data = map[string]string{}
		_ = s.saveSession(ctx, session)
	}
	ctx = withMessageFlow(ctx, claimedFlow.FlowType)
	if err := s.sendText(ctx, claimedFlow.Channel, recipient, message); err != nil {
		slog.Warn("web flow completion message failed", "flow_id", claimedFlow.ID, "error", err)
	}
	return nil
}

// NotifyWebFlowAutoRefund sends the single customer message reporting that an
// abandoned web-flow attempt was auto-refunded because the payment completed
// again. There is no claimable flow here (the reopened flow belongs to the
// retry), so this mirrors message-2 discipline without a CompleteWebFlow claim.
func (s *ConversationService) NotifyWebFlowAutoRefund(ctx context.Context, userID uuid.UUID, channel, message string) error {
	recipient, err := s.store.UserContactForChannel(ctx, userID, channel)
	if err != nil || recipient == "" {
		if err != nil {
			return err
		}
		return nil
	}
	ctx = withMessageFlow(ctx, "web")
	return s.sendText(ctx, channel, recipient, message)
}

// SendEmailVerificationCode emails a fresh 6-digit verification code for the
// user's email. It mirrors the chat confirmation path without sending any
// chat message (web flows verify inside the browser).
func (s *ConversationService) SendEmailVerificationCode(ctx context.Context, userID uuid.UUID, email string) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	hash, err := emailCodeHash(email, code)
	if err != nil {
		return err
	}
	if err := s.store.CreateEmailVerificationCode(ctx, userID, email, hash, time.Now().Add(s.cfg.EmailVerificationTTL)); err != nil {
		if errors.Is(err, store.ErrResendTooSoon) {
			return errors.New("A confirmation code was sent recently. Please wait a minute before requesting another.")
		}
		return err
	}
	if s.email == nil {
		return errors.New("Email delivery is not configured. Ask the operator to enable SMTP.")
	}
	subject := s.cfg.AppName + " email confirmation code"
	body := fmt.Sprintf("Your %s email confirmation code is %s.\n\nIt expires in %s.\n\nIf you did not request this, you can ignore this message.",
		s.cfg.AppName, code, s.cfg.EmailVerificationTTL.Round(time.Minute))
	return s.email.Send(ctx, email, subject, body)
}

// VerifyEmailCode checks the 6-digit code for the email. On success the
// user's email is recorded as verified (store side).
func (s *ConversationService) VerifyEmailCode(ctx context.Context, userID uuid.UUID, email, code string) error {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return errors.New("Enter the 6-digit code from the email.")
	}
	ok, err := s.store.VerifyEmailCode(ctx, userID, email, emailCodeDigest(email, code))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("That code is incorrect or has expired. Request a new one and try again.")
	}
	return nil
}

// ApplyIndividualProfile persists the identity profile, screens the customer,
// and advances the tier to L2 (identity on file) when the screen is clean.
// It returns "approved" or "review" (a manual-review case was opened).
func (s *ConversationService) ApplyIndividualProfile(ctx context.Context, user store.User, legalName, dob, address, occupation string) (string, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(dob))
	if err != nil || parsed.After(time.Now().AddDate(-18, 0, 0)) {
		return "", errors.New("Enter a valid date of birth as YYYY-MM-DD. You must be at least 18.")
	}
	if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, strings.TrimSpace(legalName), parsed, strings.TrimSpace(address), strings.TrimSpace(occupation)); err != nil {
		return "", err
	}
	result, err := s.screener.Screen(ctx, ports.ScreeningRequest{
		LegalName:   strings.TrimSpace(legalName),
		DateOfBirth: dob,
		PhoneNumber: user.WhatsAppNumber,
	})
	if err != nil {
		return "", err
	}
	if _, err := s.store.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: user.ID, Provider: s.identityProviderName,
		Decision: result.Decision, MatchedNames: result.MatchedNames,
	}, s.cfg.KYCRescreenPeriod); err != nil {
		return "", err
	}
	if kyc.BlockedByScreening(result.Decision) {
		if _, err := s.store.CreateManualReviewCase(ctx, store.ManualReviewCase{
			UserID: user.ID, CaseType: "screening", Reason: "screening decision " + result.Decision,
		}); err != nil {
			return "", err
		}
		return "review", nil
	}
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		return "", err
	}
	if _, err := s.store.RecomputeRiskScore(ctx, user.ID); err != nil {
		return "", err
	}
	return "approved", nil
}

// VerifyIndividualIdentity verifies an NIN/BVN against the provider and
// advances the user to L3 on success. It returns the outcome message exactly
// as it should appear to the customer.
func (s *ConversationService) VerifyIndividualIdentity(ctx context.Context, user store.User, legalName, dob, idType, idNumber string) (string, error) {
	idType = strings.ToLower(strings.TrimSpace(idType))
	if idType != "nin" && idType != "bvn" {
		return "", errors.New("ID type must be NIN or BVN.")
	}
	if len(strings.TrimSpace(idNumber)) != 11 {
		return "", errors.New("The ID number must be 11 digits.")
	}
	result, err := s.identity.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
		IDType: idType, IDNumber: strings.TrimSpace(idNumber),
		LegalName: legalName, DateOfBirth: dob,
	})
	if err != nil {
		return "", errors.New("We could not reach the verification provider. Please try again in a moment, or skip to stay at Level 2.")
	}
	switch result.Status {
	case "verified":
		if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
			UserID: user.ID, VerificationType: kyc.EvNINBVNVerified,
			Status: "completed", Provider: s.identityProviderName,
			ProviderRef: result.ProviderRef,
			Result:      map[string]any{"id_type": idType, "match_name": result.MatchName, "match_dob": result.MatchDOB},
		}); err != nil {
			return "", err
		}
		if _, err := s.store.AdvanceKYCTier(ctx, user.ID, kyc.TierL3, []string{kyc.EvNINBVNVerified}, nil); err != nil {
			return "", err
		}
		riskProfile, err := s.store.RecomputeRiskScore(ctx, user.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Your %s verified. Your Xego individual profile is now Level 3 (%s verified). Risk profile: %s.",
			strings.ToUpper(idType), strings.ToUpper(idType), strings.ToUpper(riskProfile.RiskBand)), nil
	case "mismatch", "not_found":
		return "", errors.New(result.Message + ". Double-check the number and try again, or skip to stay at Level 2.")
	default:
		return "", errors.New("Verification is still pending. Try again in a moment, or skip to stay at Level 2.")
	}
}

// NotifyInvoiceCustomer delivers the invoice to the customer (email and/or
// WhatsApp), mirroring the chat generation flow.
func (s *ConversationService) NotifyInvoiceCustomer(ctx context.Context, invoice store.InvoiceView) {
	s.notifyInvoiceCustomer(ctx, invoice)
}

// InvoiceNumberAccepted reports whether the phone is allowed to receive
// invoices (true when no accepted-number list is configured).
func (s *ConversationService) InvoiceNumberAccepted(phone string) bool {
	return s.isAcceptedInvoiceNumber(phone)
}

// InvoiceRejectedMessage returns the copy shown when a phone is not accepted.
func (s *ConversationService) InvoiceRejectedMessage() string {
	return s.rejectedPhoneMessage()
}

// flowForState maps a conversation FSM state to the messaging-cost flow name.
func flowForState(state string) string {
	switch {
	case strings.HasPrefix(state, "onboard"):
		return WebFlowOnboard
	case strings.HasPrefix(state, "invoice_pay"):
		return WebFlowPayInvoice
	case strings.HasPrefix(state, "invoice"):
		return WebFlowInvoiceCreate
	case strings.HasPrefix(state, "thrift"):
		return "thrift"
	case strings.HasPrefix(state, "await_individual"), strings.HasPrefix(state, "pay_individual"):
		return WebFlowIndividualPay
	case strings.HasPrefix(state, "wallet_topup"):
		return WebFlowTopup
	case strings.HasPrefix(state, "merchant_register"):
		return WebFlowMerchantRegister
	case strings.HasPrefix(state, "individual_"):
		return WebFlowIndividualUpgrade
	case strings.HasPrefix(state, "kyb_request"):
		return WebFlowKYBRequest
	case strings.HasPrefix(state, "select_data"), strings.HasPrefix(state, "enter_data"),
		strings.HasPrefix(state, "confirm_data"), strings.HasPrefix(state, "await_data"):
		return WebFlowData
	case strings.HasPrefix(state, "select_merchant"), strings.HasPrefix(state, "select_service"),
		strings.HasPrefix(state, "select_event"), strings.HasPrefix(state, "enter_amount"),
		strings.HasPrefix(state, "enter_service"), strings.HasPrefix(state, "collect_custom"),
		strings.HasPrefix(state, "confirm_service"), strings.HasPrefix(state, "select_payment_method"),
		strings.HasPrefix(state, "select_transfer"), strings.HasPrefix(state, "await_bank"),
		strings.HasPrefix(state, "confirm_payment"), strings.HasPrefix(state, "confirm_service"):
		return WebFlowPay
	case state == "ai_assistant":
		return "ai"
	case state == "web_flow_active":
		return "web"
	case state == "menu" || state == "":
		return "menu"
	case strings.HasPrefix(state, "confirm_session_switch"):
		return "session"
	default:
		return "chat"
	}
}

type messageFlowKey struct{}

func withMessageFlow(ctx context.Context, flow string) context.Context {
	return context.WithValue(ctx, messageFlowKey{}, flow)
}

func messageFlowFrom(ctx context.Context) string {
	flow, _ := ctx.Value(messageFlowKey{}).(string)
	if flow == "" {
		flow = "chat"
	}
	return flow
}

// recordMessage writes the outbound message to the cost meter.
func (s *ConversationService) recordMessage(ctx context.Context, channel, recipient, messageType string) {
	if !s.cfg.MessageLogEnabled {
		return
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" || strings.EqualFold(channel, "api") {
		return
	}
	if err := s.store.RecordMessage(ctx, store.MessageLogEntry{
		Channel: channel, Recipient: recipient,
		Flow: messageFlowFrom(ctx), MessageType: messageType,
	}); err != nil {
		slog.Warn("record outbound message failed", "error", err)
	}
}

// validEmail is a tiny validator used by web forms before sending codes.
func validEmail(input string) (string, bool) {
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return "", false
	}
	return strings.ToLower(address.Address), true
}
