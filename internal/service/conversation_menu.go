package service

import (
	"context"
	"strings"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) handleMenu(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if ref, ok := invoiceReferenceFromPAY(input); ok {
		return s.startInvoicePayment(ctx, channel, recipient, user, session, ref)
	}
	if strings.HasPrefix(strings.ToLower(input), "pay_invoice:") {
		ref := strings.TrimSpace(input[len("pay_invoice:"):])
		if ref != "" {
			return s.startInvoicePayment(ctx, channel, recipient, user, session, ref)
		}
	}
	if code, ok := thriftJoinNameFromInput(input); ok {
		return s.startThriftJoin(ctx, channel, recipient, user, session, code)
	}
	if code, ok := thriftActivateNameFromInput(input); ok {
		return s.startThriftActivation(ctx, channel, recipient, user, session, code)
	}
	if code, ok := thriftContributeNameFromInput(input); ok {
		return s.startThriftContribution(ctx, channel, recipient, user, session, code)
	}
	if strings.HasPrefix(strings.ToLower(input), "thrift_select:") {
		name := strings.TrimSpace(input[len("thrift_select:"):])
		group, err := s.store.ThriftGroupByName(ctx, name)
		if err != nil {
			return s.sendThriftDashboard(ctx, channel, recipient, user)
		}
		return s.sendThriftDetails(ctx, channel, recipient, user, group)
	}
	if strings.HasPrefix(strings.ToLower(input), "thrift_edit:") {
		name := strings.TrimSpace(input[len("thrift_edit:"):])
		group, err := s.store.ThriftGroupByName(ctx, name)
		if err != nil {
			return s.startThriftEdit(ctx, channel, recipient, user, session)
		}
		session.State = "thrift_edit_field"
		session.Data = map[string]string{"thrift_edit_id": group.ID.String(), "thrift_name": group.Name, "edit_changes": ""}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendThriftEditMenu(ctx, channel, recipient, group)
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
	case "pay individual", "menu_pay_individual", "send money":
		return s.startPayIndividual(ctx, channel, recipient, user, session)
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
		return s.sendText(ctx, channel, recipient, "Send the thrift group name to join. Example: JOIN Office Pool")
	case "thrift dashboard", "menu_thrift_dashboard":
		return s.sendThriftDashboard(ctx, channel, recipient, user)
	case "thrift contributions", "menu_thrift_services":
		return s.sendThriftMenu(ctx, channel, recipient)
	case "edit thrift", "menu_edit_thrift":
		return s.startThriftEdit(ctx, channel, recipient, user, session)
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
	case "ask xego", "menu_ai", "ai", "assistant":
		if s.chatAI == nil || !s.cfg.AIEnabled {
			return s.sendText(ctx, channel, recipient, "AI assistant is not available right now. Type MENU to see your options.")
		}
		session.State = "ai_assistant"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "I'm Xego's AI assistant. Ask me anything about payments, data, or thrift groups. Type MENU to exit.")
	default:
		return s.sendMenu(ctx, channel, recipient)
	}
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
		{ID: "menu_pay_individual", Title: "Pay an individual", Description: "Send money to someone's bank"},
		{ID: "menu_buy_data", Title: "Buy Data", Description: "MTN, Airtel, Glo, 9mobile"},
		{ID: "menu_merchant_services", Title: "Merchant services", Description: "Register, invoice, dashboard"},
		{ID: "menu_thrift_services", Title: "Thrift contributions", Description: "Create, join, contribute"},
		{ID: "menu_status", Title: "Payment status", Description: "Check your latest payment"},
		{ID: "menu_history", Title: "Recent payments", Description: "View your latest attempts"},
		{ID: "menu_ai", Title: "Ask Xego", Description: "Chat with our AI assistant"},
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
		{ID: "menu_join_thrift", Title: "Join thrift", Description: "Use a group name"},
		{ID: "menu_edit_thrift", Title: "Edit thrift", Description: "Update before activation"},
		{ID: "menu_thrift_dashboard", Title: "Thrift dashboard", Description: "Groups and cycles"},
		{ID: "menu_main", Title: "Back to main menu", Description: "Return to Xego menu"},
	}
}

func (s *ConversationService) sendHelp(ctx context.Context, channel, recipient string) error {
	return s.sendText(ctx, channel, recipient,
		"Xego lets you pay merchants, buy mobile data, pay invoices, and use demo thrift contribution groups.\n\nThrift commands:\nJOIN <group name> joins an inviting group.\nSTART <group name> (or ACTIVATE) lets the creator set payout rotation.\nCONTRIBUTE <group name> starts this cycle's payment.\n\nYou can also create a thrift group in one message:\nName, Amount, Frequency, Members\nExample: Office Pool, 5000, monthly, 8\n\nFor bank transfer, enter the payment reference exactly in your bank app's narration, remark, or reference field. This helps Xego match the transfer to your payment.\n\nMerchant registration and individual thrift setup use an email confirmation code before collecting higher-trust details.\n\nInvoice items can be sent in bulk. Send one item per line:\nName, Quantity, Price\nExample: Website design, 1, 25000\n\nSMS data requests use: DATA <NETWORK> <PLAN_CODE> <PHONE>. Example: DATA MTN MTN1GB 08031234567.\n\nIf you're in the middle of a payment and need to switch to something else (like paying an invoice), just send the new payment command. Xego will ask if you want to switch or continue your current payment.\n\nWe never ask for card details, PINs, OTPs, or CVVs in chat. Type MENU anytime to return to the main menu.")
}
