package service

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

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
	ok, err := s.store.VerifyEmailCode(ctx, user.ID, email, emailCodeDigest(email, code))
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
	return s.sendMenu(ctx, channel, recipient, user)
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

func (s *ConversationService) NotifyMerchantApproved(ctx context.Context, owner store.User, merchantName, setPasswordURL string) {
	if owner.WhatsAppNumber == "" {
		return
	}
	msg := fmt.Sprintf(
		"Your merchant account has been approved!\n\nBusiness: %s\n\nYou can now generate invoices and receive payments through Xego. Send MENU to get started.",
		merchantName)
	if setPasswordURL != "" {
		msg += fmt.Sprintf("\n\nSet your merchant dashboard password here:\n%s", setPasswordURL)
	}
	_ = s.sendText(ctx, ChannelWhatsApp, owner.WhatsAppNumber, msg)
}

// NotifyMerchantPayment sends a WhatsApp message to the merchant owner when an invoice payment is received.
func (s *ConversationService) NotifyMerchantPayment(ctx context.Context, invoice store.InvoiceView, paymentAmount int64) {
	prefs, err := s.store.MerchantNotificationPrefs(ctx, invoice.MerchantID)
	if err != nil || !prefs.NotifyPayment {
		return
	}
	owner, err := s.store.MerchantOwnerByInvoiceID(ctx, invoice.ID)
	if err != nil || owner.WhatsAppNumber == "" {
		return
	}
	_ = s.sendText(ctx, ChannelWhatsApp, owner.WhatsAppNumber, fmt.Sprintf(
		"Invoice payment received!\n\nInvoice: %s\nCustomer: %s\nPaid now: %s\nTotal collected: %s of %s\nStatus: %s",
		invoice.Reference, invoice.CustomerWhatsAppNumber, domain.FormatNGN(paymentAmount),
		domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(invoice.TotalKobo), strings.ToUpper(invoice.Status)))
}
