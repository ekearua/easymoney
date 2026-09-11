package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// startLinkAccounts opens the cross-channel account linking flow. Only
// non-WhatsApp channels can start it: the WhatsApp number is the anchor
// identity the caller proves ownership of.
func (s *ConversationService) startLinkAccounts(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.cfg.LinkAccountsEnabled {
		return s.sendText(ctx, channel, recipient, "Account linking isn't available right now. Type MENU to see your options.")
	}
	switch channel {
	case ChannelWhatsApp:
		return s.sendText(ctx, channel, recipient,
			"Your WhatsApp number is your main Xego account. To link it to another channel (Instagram, Telegram, or TikTok), open that chat and choose *Link accounts* from the menu.")
	case ChannelSMS, ChannelAPI, ChannelCheckout:
		return s.sendText(ctx, channel, recipient, "Account linking isn't available on this channel. Use WhatsApp, Instagram, or Telegram instead.")
	}
	// Already linked: this channel handle is attached to a WhatsApp account.
	if user.WhatsAppNumber != "" {
		return s.sendText(ctx, channel, recipient,
			"You're already linked. Your WhatsApp payments, receipts, and wallet are shared across your connected channels. Type MENU to continue.")
	}
	session.State = "link_phone"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		"Link accounts\n\nSend the WhatsApp number your Xego account is registered with, with or without +.\nExample: +2348012345678")
}

// handleLinkPhone validates the submitted phone, checks it owns an Xego
// account, then sends the one-time link code to it on WhatsApp.
func (s *ConversationService) handleLinkPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	phone := normalizePhone(input)
	if len(phone) < 8 || len(phone) > 16 {
		return s.sendText(ctx, channel, recipient, "That doesn't look like a valid phone number. Send the full number, e.g. +2348012345678.")
	}
	canonical, err := s.store.FindUserByWhatsAppNumber(ctx, phone)
	if err != nil {
		return err
	}
	if canonical.ID == uuid.Nil {
		return s.sendText(ctx, channel, recipient,
			"No Xego account is registered with that number yet.\n\nUse the WhatsApp number that received your first Xego menu, then start again.")
	}
	if canonical.ID == user.ID || user.WhatsAppNumber != "" {
		return s.sendText(ctx, channel, recipient, "That number already belongs to this account. You're all linked up.")
	}
	return s.sendLinkCode(ctx, channel, recipient, user, session, phone)
}

// sendLinkCode mints a fresh one-time code, stores its hash, and delivers it
// to the claimed WhatsApp number (demo code in chat when configured).
func (s *ConversationService) sendLinkCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, phone string) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	codeHash, err := linkCodeHash(phone, code)
	if err != nil {
		return err
	}
	if err := s.store.CreateLinkRequest(ctx, user.ID, channel, phone, codeHash, time.Now().Add(s.cfg.LinkCodeTTL)); err != nil {
		if err == store.ErrResendTooSoon {
			return s.sendText(ctx, channel, recipient, "A code was sent very recently. Wait about a minute, then type RESEND.")
		}
		return err
	}
	session.State = "link_code"
	session.Data["link_phone"] = phone
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	if s.cfg.LinkDemoCodeInChat {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("[Demo] Your Xego link code is %s. It expires in a few minutes — send it here to finish linking.", code))
	}
	messenger, err := s.messengerFor(ChannelWhatsApp)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Linking isn't available right now. Please try again in a few minutes.")
	}
	if err := messenger.SendText(ctx, phone, fmt.Sprintf("Your Xego account link code is %s. Reply with it in the chat where you started linking. It expires in a few minutes.", code)); err != nil {
		return s.sendText(ctx, channel, recipient, "We couldn't deliver your code just now. Please try again in a moment.")
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("We sent a 6-digit code to %s on WhatsApp.\n\nSend it here to link this account, or type RESEND to get a new one.", phone))
}

// handleLinkCode verifies the submitted code and merges this channel row into
// the WhatsApp account that owns the phone number.
func (s *ConversationService) handleLinkCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	phone := session.Data["link_phone"]
	if phone == "" {
		session.State, session.Data = "link_phone", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Let's start over. Send the WhatsApp number your Xego account is registered with.")
	}
	if strings.EqualFold(strings.TrimSpace(input), "resend") {
		return s.sendLinkCode(ctx, channel, recipient, user, session, phone)
	}
	ok, err := s.store.VerifyLinkRequest(ctx, user.ID, channel, phone, linkCodeDigest(phone, input))
	if err != nil {
		return err
	}
	if !ok {
		return s.sendText(ctx, channel, recipient, "That code didn't match. Check the code WhatsApp sent you and try again, or type RESEND.")
	}
	canonical, err := s.store.FindUserByWhatsAppNumber(ctx, phone)
	if err != nil {
		return err
	}
	if canonical.ID == uuid.Nil {
		return s.sendText(ctx, channel, recipient, "That WhatsApp number no longer matches an Xego account. Type MENU to return to the main menu.")
	}
	if canonical.ID == user.ID {
		return s.sendText(ctx, channel, recipient, "That number already belongs to this account. You're all linked up.")
	}
	handle, username := channelHandleForUser(user, channel)
	if err := s.store.MergeUsers(ctx, user.ID, canonical.ID, channel, handle, username); err != nil {
		return s.sendText(ctx, channel, recipient, "Something went wrong while linking your accounts. Please try again.")
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	if err := s.sendText(ctx, channel, recipient,
		fmt.Sprintf("You're all linked up. Your %s account is now connected to your Xego WhatsApp account — payments, receipts, and your wallet are shared across both.", channelDisplayName(channel))); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient, canonical)
}

// channelHandleForUser returns the (handle, username) of a channel row so it
// can be transferred onto the surviving account during a merge.
func channelHandleForUser(user store.User, channel string) (string, string) {
	switch channel {
	case ChannelInstagram:
		return user.InstagramIGSID.String, user.InstagramUsername
	case ChannelTikTok:
		return user.TikTokOpenID.String, user.TikTokUsername
	case ChannelTelegram:
		return user.TelegramChatID.String, user.TelegramUsername
	}
	return "", ""
}

func channelDisplayName(channel string) string {
	switch channel {
	case ChannelInstagram:
		return "Instagram"
	case ChannelTikTok:
		return "TikTok"
	case ChannelTelegram:
		return "Telegram"
	}
	return channel
}
