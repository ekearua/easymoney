package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/chatguard"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) blockPaymentCredentialMessage(ctx context.Context, message store.InboundMessage, recipient string, result chatguard.Result) {
	if err := s.store.RecordChatGuardEvent(ctx, store.ChatGuardEvent{
		MessageID:    message.ID,
		Channel:      message.Channel,
		Sender:       message.Sender,
		Recipient:    recipient,
		Category:     string(result.Category),
		RedactedText: result.Redacted,
	}); err != nil {
		// Never block the customer because the audit write failed; still refuse.
		_ = err
	}
	_ = s.sendText(ctx, message.Channel, recipient,
		"We can't accept that. Xego never asks for card numbers, PINs, CVVs, or OTPs in chat, and we've logged this for security. Your pending request hasn't changed - send MENU or continue with what you were doing.")
}

func (s *ConversationService) handleOnboarding(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.State == "web_flow_active" {
		// The customer is finishing onboarding in the browser; do not yank
		// them into the chat flow mid-way.
		return s.sendText(ctx, channel, recipient,
			"You're completing your profile in your browser — tap the link we sent to continue, or type MENU to cancel.")
	}
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
	// Two-message onboarding: a fresh WhatsApp user gets the browser profile
	// link (message 1); completing it confirms the account and the welcome
	// message follows (message 2). Users already mid-chat in onboarding keep
	// the chat flow.
	if !user.OnboardingComplete && s.WebFlowEnabled(channel, WebFlowOnboard) && (session.State == "" || session.State == "menu") {
		return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowOnboard, "", "", nil)
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
		return s.sendText(ctx, channel, recipient, "That email doesn't look quite right. Please send a valid address, like name@example.com.")
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
	codeHash, err := emailCodeHash(email, code)
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(s.cfg.EmailVerificationTTL)
	if err := s.store.CreateEmailVerificationCode(ctx, userID, email, codeHash, expiresAt); err != nil {
		if errors.Is(err, store.ErrResendTooSoon) {
			return s.sendText(ctx, channel, recipient, "A confirmation code was sent recently. Please wait a minute, then type RESEND.")
		}
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
	if s.cfg.EmailDemoCodeInChat && s.cfg.Environment != "production" {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Demo email confirmation for %s\n\nCode: %s\n\nEnter this 6-digit code to continue. In production this code should be delivered by email only.", email, code))
	}
	return s.sendText(ctx, channel, recipient, "Email confirmation is enabled, but email delivery is not configured yet. Ask the operator to configure SMTP or enable the demo code fallback.")
}
