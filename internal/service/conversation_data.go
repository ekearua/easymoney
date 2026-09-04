package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

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
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderInterswitch, channel, recipient)
		if err != nil {
			return friendlyAllowanceErr(err)
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nData: %s %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nXego will activate the data order after payment is verified.",
				order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
			s.payments.HostedCheckoutURL(payment))
	default:
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return friendlyAllowanceErr(err)
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nData: %s %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nXego will activate the data order after payment is verified.",
				order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
			s.payments.HostedCheckoutURL(payment))
	}
}

func (s *ConversationService) handleDataPaymentMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	order, err := s.dataOrderFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That data order session expired. Please start again.")
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderInterswitch, channel, recipient)
		if err != nil {
			return friendlyAllowanceErr(err)
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nData: %s %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nXego will activate the data order after payment is verified.",
				order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
			s.payments.HostedCheckoutURL(payment))
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, _, err := s.data.CreatePaymentForOrder(ctx, user, order, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return friendlyAllowanceErr(err)
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nData: %s %s\nPhone: %s\nAmount: %s\nRequest code: %s\n\nXego will activate the data order after payment is verified.",
				order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), order.RequestCode),
			s.payments.HostedCheckoutURL(payment))
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

func (s *ConversationService) dataOrderFromSession(ctx context.Context, session store.Session) (store.DataOrderView, error) {
	orderID, err := uuid.Parse(session.Data["data_order_id"])
	if err != nil {
		return store.DataOrderView{}, err
	}
	return s.store.DataOrderByID(ctx, orderID)
}

func dataPlanRow(plan store.DataPlan) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "data_plan:" + plan.Code,
		Title:       truncateInteractiveTitle(plan.DisplayName),
		Description: truncateInteractiveDescription(plan.Validity + " - " + domain.FormatNGN(plan.PriceKobo)),
	}
}
