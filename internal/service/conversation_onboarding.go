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
	"whatsapp-payment-demo/internal/kyc"
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

// startProfileCompletion is the optional onboarding path reachable from the
// "Complete profile" menu row. WhatsApp users finish name/email in the browser
// flow; other channels and CLI tests reuse the chat onboarding states. A user
// whose name and email are already on file is told so instead of being asked
// again.
func (s *ConversationService) startProfileCompletion(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if s.WebFlowEnabled(channel, WebFlowOnboard) {
		return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowOnboard, "", "", nil)
	}
	if user.DisplayName != "" && user.Email != "" {
		return s.sendText(ctx, channel, recipient, "Your profile already has a name and email on file.")
	}
	session.State, session.Data = "onboard_name", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What name should we use on your receipts?")
}

func (s *ConversationService) handleOnboarding(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.State == "web_flow_active" {
		// The customer is finishing onboarding in the browser; do not yank
		// them into the chat flow mid-way. MENU resets the chat session and
		// CANCEL abandons the open browser flow too (both are handled globally
		// in Handle, so this branch only sees other input).
		return s.sendText(ctx, channel, recipient,
			"You're completing your profile in your browser — tap the link we sent to continue, or type CANCEL to stop.")
	}
	if session.State == "onboard_confirm_account" {
		return s.handleAccountConfirmation(ctx, channel, recipient, user, session, input)
	}
	if user.OnboardingComplete && !s.onboardingCompleteForChannel(user, channel) &&
		!s.userIsApprovedIndividual(ctx, user) &&
		session.State != "onboard_confirm_account" && session.State != "onboard_email" && session.State != "onboard_email_code" {
		session.State, session.Data = "onboard_confirm_account", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendAccountConfirmation(ctx, channel, recipient)
	}
	// Two-message onboarding: a fresh WhatsApp user gets the browser profile
	// link (message 1); completing it confirms the account and the welcome
	// message follows (message 2). A user INSERTed on this very message
	// (FirstContact — set only by GetOrCreateUser) is treated the same even
	// though auto-confirm already stamped their account, so a brand-new
	// number's first text initiates onboarding instead of reaching AI routing.
	// Users already mid-chat in onboarding keep the chat flow.
	if (!user.OnboardingComplete || user.FirstContact) && s.WebFlowEnabled(channel, WebFlowOnboard) && (session.State == "" || session.State == "menu") {
		return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowOnboard, "", "", nil)
	}
	if session.State != "onboard_name" && session.State != "onboard_email" && session.State != "onboard_email_code" &&
		session.State != "onboard_details" && session.State != "onboard_dob" && session.State != "onboard_gender" &&
		session.State != "onboard_address" && session.State != "onboard_confirm_account" {
		session.State = "onboard_name"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient,
			"Welcome to "+s.cfg.AppName+" — a simple way to pay merchants.\n\nWhat name should we use on your receipts?")
	}
	if session.State == "onboard_name" || session.State == "onboard_email" {
		// Aggregated identity pair: one message for the display name and the
		// email to verify. A name-only message still advances (email comes
		// next), so customers who prefer smaller steps are not blocked.
		parts := ParseNameAndEmail(input)
		name := parts.Name
		email := parts.Email
		if name == "" && session.State == "onboard_email" {
			name = session.Data["name"]
		}
		var problem string
		if name == "" {
			problem = "Please send the name you would like on receipts. It should be between 2 and 80 characters."
			name = ""
		} else if n := len([]rune(name)); n < 2 || n > 80 {
			problem = "Please send the name you would like on receipts. It should be between 2 and 80 characters."
			name = ""
		}
		if email != "" {
			if _, err := mail.ParseAddress(email); err != nil {
				problem = "That email doesn't look quite right. Please send a valid address, like name@example.com."
				email = ""
			}
		}
		if name != "" && session.State == "onboard_name" {
			if err := s.store.UpdateUserName(ctx, user.ID, name); err != nil {
				return err
			}
		}
		if name != "" && email != "" {
			// Both captured: save the email and send the code in one turn.
			if err := s.store.UpdateUserEmail(ctx, user.ID, strings.ToLower(email)); err != nil {
				return err
			}
			session.Data["name"] = name
			session.Data["email"] = strings.ToLower(email)
			session.State = "onboard_email_code"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			if err := s.sendText(ctx, channel, recipient, "Thanks, "+name+". Sending your confirmation code now."); err != nil {
				return err
			}
			return s.startEmailConfirmation(ctx, channel, recipient, user.ID, strings.ToLower(email))
		}
		if name != "" {
			session.State = "onboard_email"
			session.Data = map[string]string{"name": name}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			msg := ""
			if problem != "" {
				msg = problem + "\n\n"
			}
			msg += "Thanks, " + name + ". Now send the email address to use for checkout and receipts, together with your name in one message if you like."
			return s.sendText(ctx, channel, recipient, msg)
		}
		if email != "" {
			// Email only (a name is already on file from a previous message).
			if session.Data["name"] == "" {
				problem = "Please start with the name you would like on receipts, then the email — both in one message is fine."
				email = ""
			}
		}
		msg := ""
		if problem != "" {
			msg = problem + "\n\n"
		}
		msg += nameAndEmailPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	// The emailed code is actually verified here (bcrypt-hashed codes,
	// 5-attempt lockout and a resend cooldown live in VerifyEmailCode and
	// CreateEmailVerificationCode), closing the gap where the chat track
	// accepted any input at this state.
	if session.State == "onboard_email_code" {
		email := session.Data["email"]
		if email == "" {
			email = user.Email
		}
		switch strings.ToLower(strings.TrimSpace(input)) {
		case "resend", "resend code":
			return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
		case "change email", "email":
			session.State, session.Data = "onboard_email", map[string]string{"name": session.Data["name"]}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, "No problem. Send the email address you want to use for checkout and receipts.")
		}
		code := normalizeEmailCode(input)
		if len(code) != 6 {
			return s.sendText(ctx, channel, recipient, "Enter the 6-digit code we sent to "+email+". You can also type RESEND or CHANGE EMAIL.")
		}
		ok, err := s.store.VerifyEmailCode(ctx, user.ID, email, emailCodeDigest(email, code))
		if err != nil {
			return err
		}
		if !ok {
			return s.sendText(ctx, channel, recipient, "That code is incorrect or has expired. Please try again, type RESEND, or type CHANGE EMAIL.")
		}
		session.State = "onboard_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, "Email verified."); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, tier1DetailsPrompt())
	}
	if session.State == "onboard_details" || session.State == "onboard_dob" ||
		session.State == "onboard_gender" || session.State == "onboard_address" {
		// One aggregated message for the CBN Tier 1 self-declared set (DOB,
		// gender, address). Legacy single-field states route here too, so a
		// session saved before the aggregation still completes; anything the
		// earlier prompts already collected (session.Data) is merged in.
		details := ParseTier1Details(input)
		if details.DOB == "" {
			details.DOB = session.Data["dob"]
		}
		if details.Gender == "" {
			details.Gender = session.Data["gender"]
		}
		if details.Address == "" {
			details.Address = session.Data["address"]
		}
		var problem string
		if field := ValidateTier1Details(details); field != "" {
			switch field {
			case "date of birth":
				problem = "The date of birth must be a real date in YYYY-MM-DD format, and for this demo you must be at least 18."
				details.DOB = ""
			case "gender":
				problem = "Gender must be MALE or FEMALE."
				details.Gender = ""
			case "residential address":
				problem = "The residential address must be between 10 and 240 characters."
				details.Address = ""
			}
		}
		session.Data["dob"] = details.DOB
		session.Data["gender"] = details.Gender
		session.Data["address"] = details.Address
		if !details.Complete() {
			session.State = "onboard_details"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			msg := ""
			if problem != "" {
				msg = problem + "\n\n"
			}
			if note := capturedNote(details.Captured()); note != "" {
				msg += note + "\n"
			}
			msg += missingParts(details.Missing())
			msg += "\n\n" + tier1DetailsPrompt()
			return s.sendText(ctx, channel, recipient, msg)
		}
		session.State = "onboard_confirm_account"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, "Details saved. One last step."); err != nil {
			return err
		}
		return s.sendAccountConfirmation(ctx, channel, recipient)
	}
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look quite right. Please send a valid address, like name@example.com.")
	}
	email := strings.ToLower(address.Address)
	if err := s.store.UpdateUserEmail(ctx, user.ID, email); err != nil {
		return err
	}
	session.Data["email"] = email
	session.State = "onboard_email_code"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

// nameAndEmailPrompt is the aggregated onboarding identity prompt: the
// display name and the email to verify, in one message.
func nameAndEmailPrompt() string {
	return "Welcome to " + "Xego" + " — let's get your name and email in one message.\n\n" +
		promptListing([]string{"The name for your receipts", "Your email (we'll send it a 6-digit confirmation code)"}) +
		"Example:\nName: Ada Obi\nEmail: ada@example.com"
}

// tier1DetailsPrompt is the aggregated CBN Tier 1 collection prompt shown
// once instead of the old one-message-per-field chain.
func tier1DetailsPrompt() string {
	return "As part of the CBN Tier 1 check, send these three in one message:\n\n" +
		promptListing([]string{"Date of birth as YYYY-MM-DD", "Gender: MALE or FEMALE", "Your residential address"}) +
		"Example:\nDOB: 1992-05-24\nGender: Female\n14 Admiralty Way, Lekki, Lagos"
}

// handleNewChannelForApprovedUser is the non-blocking cross-channel handoff
// for a customer whose GLOBAL identity already cleared the approved/money-out
// tier. Their KYC tier is global, so a fresh channel is never a wall: they get
// a one-time, non-blocking intro and then the menu (money-out rendered from
// global fields). The channel is durably stamped via MarkChannelOnboarded
// (channel's *_confirmed_at only, KYC-neutral) so onboardingCompleteForChannel
// goes true and the gate in Handle stays closed — the intro fires exactly once,
// not on every subsequent message from that channel.
func (s *ConversationService) handleNewChannelForApprovedUser(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if err := s.sendText(ctx, channel, recipient, "It's the same you — your "+channelDisplayName(channel)+" is now linked to your Xego account, so there's nothing to sign up for again. Type MENU to see what you can do."); err != nil {
		return err
	}
	// Durable channel stamp *after* the intro so a transient send failure leaves
	// the gate open and the intro retries on the next message.
	if err := s.store.MarkChannelOnboarded(ctx, user.ID, channel); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient, user)
}

func (s *ConversationService) handleAccountConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch {
	case input == "confirm_number" || input == "confirm_account" || strings.EqualFold(input, "confirm"):
		var err error
		switch channel {
		case ChannelTelegram:
			err = s.store.ConfirmTelegramAccount(ctx, user.ID)
		case ChannelInstagram:
			err = s.store.ConfirmInstagramAccount(ctx, user.ID)
		case ChannelFacebookMessenger:
			err = s.store.ConfirmFacebookMessengerAccount(ctx, user.ID)
		case ChannelTikTok:
			err = s.store.ConfirmTikTokAccount(ctx, user.ID)
		default:
			err = s.store.ConfirmUserNumber(ctx, user.ID)
		}
		if err != nil {
			return err
		}
		// CBN Tier-1 data (legal name, date of birth, gender, residential
		// address) is collected during onboarding; persist it when present so
		// the L0 -> L1 advance can carry tier1_basic_details.
		if dob, err := time.Parse("2006-01-02", strings.TrimSpace(session.Data["dob"])); err == nil {
			name := strings.TrimSpace(session.Data["name"])
			if name == "" {
				name = strings.TrimSpace(user.DisplayName)
			}
			if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, name, dob, session.Data["address"], "", session.Data["gender"]); err != nil {
				return err
			}
		}
		// Completing onboarding reaches the base KYC tier (L1: channel +
		// CBN Tier-1 data confirmed), which activates the wallet, provisions a
		// standing account number, and unlocks every wallet option. Idempotent
		// — a user already at a higher tier is untouched.
		if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL1, []string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil); err != nil {
			return err
		}
		// Reserve the standing account number so the customer can be paid by
		// bank transfer from Level 1 on. The reserved 9029-* number is the
		// local default and is upgraded by the provider only when Interswitch
		// credentials are present.
		if _, err := s.store.EnsureStandingAccount(ctx, user.ID); err != nil {
			return err
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, "You're all set. Your account is confirmed (CBN Tier 1) and your Xego wallet is now active."); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient, user)
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
