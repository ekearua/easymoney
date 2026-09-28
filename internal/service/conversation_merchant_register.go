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
	session.State = "merchant_register_details"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Email verified for merchant registration.\n\n"+merchantDetailsPrompt())
}

// handleMerchantRegistrationDetails collects the whole merchant KYB form
// (business name, category, description) in one message, then creates the
// registration request. The legacy name/category/description states route
// here too, so sessions saved before the aggregation complete; anything
// earlier prompts collected (session.Data) is merged in.
func (s *ConversationService) handleMerchantRegistrationDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	details := ParseMerchantRegistrationDetails(input)
	if details.BusinessName == "" {
		details.BusinessName = session.Data["business_name"]
	}
	if details.Category == "" {
		details.Category = session.Data["category"]
	}
	if details.Description == "" {
		details.Description = session.Data["description"]
	}
	var problem string
	if field := ValidateMerchantRegistrationDetails(details); field != "" {
		switch field {
		case "business name":
			problem = "The business name must be between 2 and 80 characters."
			details.BusinessName = ""
		case "business category":
			problem = "The category must be a short label like Food, Retail, Services, Health, Education, or Logistics (2-50 characters)."
			details.Category = ""
		case "description":
			problem = "The description must be between 10 and 240 characters."
			details.Description = ""
		}
	}
	session.Data["business_name"] = details.BusinessName
	session.Data["category"] = details.Category
	session.Data["description"] = details.Description
	if !details.Complete() {
		session.State = "merchant_register_details"
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
		msg += "\n\n" + merchantDetailsPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	if session.Data == nil || strings.TrimSpace(session.Data["email"]) == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That merchant registration session expired. Please start again.")
	}
	request, err := s.store.CreateMerchantRegistration(ctx, user.ID, details.BusinessName, details.Category, details.Description, session.Data["email"])
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

// merchantDetailsPrompt is the aggregated merchant KYB prompt shown once
// instead of the old one-message-per-field chain.
func merchantDetailsPrompt() string {
	return "Send these three for your merchant registration, in one message:\n\n" +
		promptListing([]string{"Business or merchant name", "Category (e.g. Food, Retail, Services, Health, Education, Logistics)", "A short description of what the business sells or does"}) +
		"Example:\nName: Ade's Kitchen\nCategory: Food\nDescription: Home-cooked Nigerian meals delivered across Lagos"
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
