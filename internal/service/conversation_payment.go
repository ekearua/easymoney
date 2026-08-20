package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) handleAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	session.State = "select_payment_method"
	session.Data["amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
}

func (s *ConversationService) handleServiceOrAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return s.sendText(ctx, channel, recipient, "Send the number of a service above, or type CUSTOM to enter an amount.")
	}
	if strings.EqualFold(input, "custom") || strings.EqualFold(input, "custom amount") {
		session.State = "enter_amount"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("How much would you like to pay %s?\n\nEnter an amount between %s and %s. Example: 2500",
				merchant.Name,
				domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(s.cfg.PaymentMaxKobo)))
	}
	services, err := s.store.ListActiveMerchantServices(ctx, merchant.ID)
	if err != nil {
		return err
	}
	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(services) {
		svc := services[n-1]
		session.Data["service_id"] = svc.ID.String()
		session.Data["service_name"] = svc.Name
		session.Data["unit_price_kobo"] = strconv.FormatInt(svc.UnitPriceKobo, 10)
		session.State = "enter_service_quantity"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("%s — %s each\n\nHow many? (Enter a number, default is 1)", svc.Name, domain.FormatNGN(svc.UnitPriceKobo)))
	}
	lower := strings.ToLower(input)
	for _, svc := range services {
		if strings.Contains(strings.ToLower(svc.Name), lower) {
			session.Data["service_id"] = svc.ID.String()
			session.Data["service_name"] = svc.Name
			session.Data["unit_price_kobo"] = strconv.FormatInt(svc.UnitPriceKobo, 10)
			session.State = "enter_service_quantity"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient,
				fmt.Sprintf("%s — %s each\n\nHow many? (Enter a number, default is 1)", svc.Name, domain.FormatNGN(svc.UnitPriceKobo)))
		}
	}
	return s.sendServicePicker(ctx, channel, recipient, merchant, services)
}

func (s *ConversationService) handleServiceQuantity(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	qty := 1
	input = strings.TrimSpace(input)
	if input != "" {
		n, err := strconv.Atoi(input)
		if err == nil && n > 0 {
			qty = n
		}
	}
	unitPrice, _ := strconv.ParseInt(session.Data["unit_price_kobo"], 10, 64)
	total := int64(qty) * unitPrice
	if total < s.cfg.PaymentMinKobo || total > s.cfg.PaymentMaxKobo {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Total is %s which is outside the allowed range (%s–%s). Try a different quantity.",
				domain.FormatNGN(total), domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(s.cfg.PaymentMaxKobo)))
	}
	serviceID := session.Data["service_id"]
	svc, err := s.store.MerchantServiceByID(ctx, uuid.MustParse(serviceID))
	if err != nil {
		return err
	}
	if svc.QuantityAvailable >= 0 && qty > svc.QuantityAvailable {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Sorry, only %d available. Enter a smaller quantity.", svc.QuantityAvailable))
	}
	session.Data["service_quantity"] = strconv.Itoa(qty)
	session.Data["amount_kobo"] = strconv.FormatInt(total, 10)
	customFields, _ := s.store.ListServiceCustomFields(ctx, svc.ID)
	if len(customFields) > 0 {
		session.Data["custom_fields_prompt"] = buildCustomFieldsPrompt(customFields)
		session.State = "collect_custom_fields"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		fieldOrder := buildCustomFieldsNames(customFields)
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Service: %s\nQuantity: %d\nUnit price: %s\nTotal: %s\n\n%s\n\nSend all values separated by commas in this order: %s\nExample: %s",
				svc.Name, qty, domain.FormatNGN(unitPrice), domain.FormatNGN(total), session.Data["custom_fields_prompt"], fieldOrder, buildCustomFieldsExample(customFields)))
	}
	session.State = "confirm_service_purchase"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Service: %s\nQuantity: %d\nUnit price: %s\nTotal: %s\n\nReply CONFIRM to proceed to payment, or CANCEL to go back.",
			svc.Name, qty, domain.FormatNGN(unitPrice), domain.FormatNGN(total)))
}

func buildCustomFieldsNames(fields []store.ServiceCustomField) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.FieldName
	}
	return strings.Join(names, ", ")
}

func buildCustomFieldsExample(fields []store.ServiceCustomField) string {
	vals := make([]string, len(fields))
	for i, f := range fields {
		switch f.FieldType {
		case "number":
			vals[i] = strconv.Itoa(i + 1)
		default:
			vals[i] = "value" + strconv.Itoa(i+1)
		}
	}
	return strings.Join(vals, ", ")
}

func buildCustomFieldsPrompt(fields []store.ServiceCustomField) string {
	var sb strings.Builder
	sb.WriteString("This service requires the following info:")
	for _, f := range fields {
		req := ""
		if f.IsRequired {
			req = " (required)"
		}
		sb.WriteString(fmt.Sprintf("\n• %s%s", f.FieldName, req))
	}
	return sb.String()
}

func (s *ConversationService) handleCollectCustomFields(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "skip") {
		session.State = "confirm_service_purchase"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		unitP, _ := strconv.ParseInt(session.Data["unit_price_kobo"], 10, 64)
		amt, _ := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Service: %s\nQuantity: %s\nUnit price: %s\nTotal: %s\n\nReply CONFIRM to proceed to payment, or CANCEL to go back.",
				session.Data["service_name"], session.Data["service_quantity"], domain.FormatNGN(unitP), domain.FormatNGN(amt)))
	}
	serviceID, _ := uuid.Parse(session.Data["service_id"])
	fields, err := s.store.ListServiceCustomFields(ctx, serviceID)
	if err != nil {
		return err
	}
	parts := strings.Split(input, ",")
	var fieldValues []string
	fieldMap := make(map[string]string)
	for i, f := range fields {
		var val string
		if i < len(parts) {
			val = strings.TrimSpace(parts[i])
		}
		if f.IsRequired && val == "" {
			return s.sendText(ctx, channel, recipient,
				fmt.Sprintf("%s is required. Please send all values again separated by commas.", f.FieldName))
		}
		fieldValues = append(fieldValues, val)
		fieldMap[f.FieldName] = val
	}
	data, _ := json.Marshal(fieldMap)
	session.Data["custom_data_json"] = string(data)
	session.State = "confirm_service_purchase"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	prompt := ""
	for _, f := range fields {
		prompt += fmt.Sprintf("%s: %s\n", f.FieldName, fieldMap[f.FieldName])
	}
	unitP, _ := strconv.ParseInt(session.Data["unit_price_kobo"], 10, 64)
	amt, _ := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Service: %s\nQuantity: %s\nUnit price: %s\nTotal: %s\n%s\nReply CONFIRM to proceed to payment, or CANCEL to go back.",
			session.Data["service_name"], session.Data["service_quantity"], domain.FormatNGN(unitP), domain.FormatNGN(amt), prompt))
}

func (s *ConversationService) handleConfirmServicePurchase(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "confirm") {
		merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
		if err != nil {
			return err
		}
		amount, _ := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
		session.State = "select_payment_method"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
	}
	session.State = "select_merchant"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Purchase cancelled. Select a merchant to start again.")
}

func (s *ConversationService) handlePaymentMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
	}
	serviceIDStr := session.Data["service_id"]
	serviceQtyStr := session.Data["service_quantity"]
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderPaystack, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.recordServicePurchase(ctx, payment.ID, serviceIDStr, serviceQtyStr, session.Data["custom_data_json"]); err != nil {
			return err
		}
		session.State = "confirm_payment"
		session.Data["payment_id"] = payment.ID.String()
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCardReview(ctx, channel, recipient, merchant, amount)
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		session.State = "select_transfer_bank"
		session.Data["service_id"] = serviceIDStr
		session.Data["service_quantity"] = serviceQtyStr
		delete(session.Data, "bank_query")
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	default:
		return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
	}
}

func (s *ConversationService) recordServicePurchase(ctx context.Context, paymentID uuid.UUID, serviceIDStr, qtyStr, customDataJSON string) error {
	if serviceIDStr == "" || qtyStr == "" {
		return nil
	}
	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return nil
	}
	qty, _ := strconv.Atoi(qtyStr)
	if qty <= 0 {
		qty = 1
	}
	svc, err := s.store.MerchantServiceByID(ctx, serviceID)
	if err != nil {
		return err
	}
	total := int64(qty) * svc.UnitPriceKobo
	purchaseID, err := s.store.CreateServicePurchase(ctx, serviceID, paymentID, qty, svc.UnitPriceKobo, total)
	if err != nil {
		return err
	}
	if customDataJSON != "" {
		var fieldMap map[string]string
		if err := json.Unmarshal([]byte(customDataJSON), &fieldMap); err == nil && len(fieldMap) > 0 {
			_ = s.store.SavePurchaseCustomData(ctx, purchaseID, fieldMap)
		}
	}
	return nil
}

func (s *ConversationService) handleTransferBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
	if err != nil {
		session.State = "select_merchant"
		_ = s.saveSession(ctx, session)
		return s.sendMerchantPicker(ctx, channel, recipient, user, "", 0)
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
	payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amount, ProviderBankTransfer, channel, recipient)
	if err != nil {
		return err
	}
	if err := s.recordServicePurchase(ctx, payment.ID, session.Data["service_id"], session.Data["service_quantity"], session.Data["custom_data_json"]); err != nil {
		return err
	}
	payment, instruction, err := s.payments.InitializeBankTransferSimulation(ctx, payment, account)
	if err != nil {
		return err
	}
	session.State = "await_bank_transfer"
	session.Data["payment_id"] = payment.ID.String()
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
}

func (s *ConversationService) sendCardReview(ctx context.Context, channel, recipient string, merchant store.Merchant, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("Review your Xego payment:\n\nMerchant: %s\nAmount: %s\n\nContinue to secure card checkout?", merchant.Name, domain.FormatNGN(amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Continue"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
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
	return s.sendText(ctx, channel, recipient, "Thanks. Xego has received your transfer confirmation. You'll receive the final update shortly.")
}

func (s *ConversationService) sendPaymentMethods(ctx context.Context, channel, recipient string, merchant store.Merchant, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("How would you like to pay %s to %s?\n\nFor bank transfer, Xego will give you collection account details and a unique reference to enter in your bank app.", domain.FormatNGN(amount), merchant.Name),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
		},
	})
}

func (s *ConversationService) sendTransferBanks(ctx context.Context, channel, recipient string) error {
	accounts, err := s.store.ListActiveBankTransferAccounts(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, ports.InteractiveRow{
			ID:          "bank:" + account.ID.String(),
			Title:       account.BankName,
			Description: account.AccountName + " · " + account.AccountNumber,
		})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose the Xego collection bank you want to transfer to. Pick the bank that is easiest for you to pay into.",
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Nigerian banks", Rows: rows}},
	})
}

func (s *ConversationService) sendRecommendedTransferBank(ctx context.Context, channel, recipient string) error {
	account, err := s.store.RecommendedBankTransferAccount(ctx)
	if err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Recommended collection bank\n\nBank: %s\nAccount name: %s\nAccount number: %s\n\nUse this bank if it is convenient. Xego will generate a unique payment reference after this step; copy that reference into your bank app's narration, remark, or payment reference field.",
			account.BankName, account.AccountName, account.AccountNumber),
		Buttons: []ports.InteractiveButton{
			{ID: "bank:" + account.ID.String(), Title: "Use this bank"},
			{ID: "bank_choose_other", Title: "Choose another"},
		},
	})
}

func (s *ConversationService) sendTransferBankPicker(ctx context.Context, channel, recipient, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	accounts, hasMore, err := s.store.SearchBankTransferAccounts(ctx, query, page*pickerPageSize, pickerPageSize)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "No collection banks are available right now. Please try again shortly.")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find that bank. Type another bank name, or type MENU to return to the main menu.")
	}
	rows := make([]ports.InteractiveRow, 0, len(accounts)+2)
	for _, account := range accounts {
		rows = append(rows, bankRow(account))
	}
	rows = appendPickerNavigation(rows, "bank_page:", page, hasMore)
	body := "Choose the Xego collection bank you want to transfer to. Pick the bank that is easiest for you to pay into.\n\nAfter choosing, Xego will show the exact amount and a unique reference to enter in your bank app."
	if query != "" {
		body = fmt.Sprintf("Bank search results for %q.\n\nChoose a collection bank, or type another bank name to search again.", query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        body,
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Collection banks", Rows: rows}},
	})
}

func (s *ConversationService) sendBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details\n\nMerchant: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nReference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above to this account.\n3. Put the reference exactly as shown in the narration, remark, or payment reference field.\n4. After sending, tap I have transferred.\n\nThe reference is how Xego matches your transfer to this payment.",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func bankRow(account store.BankTransferAccount) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "bank:" + account.ID.String(),
		Title:       truncateInteractiveTitle(account.BankName),
		Description: truncateInteractiveDescription(account.AccountName + " - " + account.AccountNumber),
	}
}
