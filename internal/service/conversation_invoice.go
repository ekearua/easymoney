package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) startInvoiceGeneration(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(merchants) == 0 {
		return s.sendText(ctx, channel, recipient, "Invoice generation is available after your merchant registration is approved.\n\nChoose Register merchant to submit a business, or check with the operator if you have already submitted one.")
	}
	session.Data = map[string]string{}
	if len(merchants) == 1 {
		session.State = "invoice_customer_phone"
		session.Data["invoice_merchant_slug"] = merchants[0].Slug
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Let's generate an invoice for "+merchants[0].Name+".\n\nSend the customer's WhatsApp number and email, comma-separated.\nExample: +2347061975340, customer@example.com\n\nOr send just the WhatsApp number to enter details one at a time.")
	}
	session.State = "invoice_select_merchant"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants))
	for _, merchant := range merchants {
		rows = append(rows, ports.InteractiveRow{ID: "invoice_merchant:" + merchant.Slug, Title: merchant.Name, Description: merchant.Category})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose which approved merchant should issue this invoice.",
		ButtonLabel: "Choose merchant",
		Sections:    []ports.InteractiveSection{{Title: "Your merchants", Rows: rows}},
	})
}

func (s *ConversationService) handleInvoiceMerchant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if !strings.HasPrefix(input, "invoice_merchant:") {
		return s.startInvoiceGeneration(ctx, channel, recipient, user, session)
	}
	slug := strings.TrimPrefix(input, "invoice_merchant:")
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, merchant := range merchants {
		if merchant.Slug == slug {
			session.State = "invoice_customer_phone"
			session.Data = map[string]string{"invoice_merchant_slug": slug}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, "Send the customer's WhatsApp number and email, comma-separated.\nExample: +2347061975340, customer@example.com\n\nOr send just the WhatsApp number to enter details one at a time.")
		}
	}
	return s.startInvoiceGeneration(ctx, channel, recipient, user, session)
}

func (s *ConversationService) handleInvoiceCustomerPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 2 {
		phone, phoneErr := domain.NormalizeNigerianPhone(fields[0])
		address, emailErr := mail.ParseAddress(fields[1])
		if phoneErr != nil {
			return s.sendText(ctx, channel, recipient, "Send a valid Nigerian WhatsApp number for the customer, for example +2347061975340.")
		}
		if !s.isAcceptedInvoiceNumber(phone) {
			return s.sendText(ctx, channel, recipient, s.rejectedPhoneMessage())
		}
		if emailErr != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
			return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send a valid email address, like customer@example.com.")
		}
		session.State = "invoice_item_name"
		session.Data["invoice_customer_phone"] = phone
		session.Data["invoice_customer_email"] = strings.ToLower(address.Address)
		session.Data["invoice_items"] = "[]"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Add invoice items.\n\nSend one item per line: Name, Quantity, Price\nExample: Website design, 1, 25000\n\nOr send just the item name to enter details one at a time.")
	}

	phone, err := domain.NormalizeNigerianPhone(input)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Send a valid Nigerian WhatsApp number for the customer, for example +2347061975340.")
	}
	if !s.isAcceptedInvoiceNumber(phone) {
		return s.sendText(ctx, channel, recipient, s.rejectedPhoneMessage())
	}
	session.State = "invoice_customer_email"
	session.Data["invoice_customer_phone"] = phone
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send the customer's email address for the invoice copy.")
}

func (s *ConversationService) handleInvoiceCustomerEmail(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send the customer's email address, like customer@example.com.")
	}
	session.State = "invoice_item_name"
	session.Data["invoice_customer_email"] = strings.ToLower(address.Address)
	session.Data["invoice_items"] = "[]"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Add invoice items.\n\nSend one item per line: Name, Quantity, Price\nExample: Website design, 1, 25000\n\nOr send just the item name to enter details one at a time.")
}

func (s *ConversationService) handleInvoiceItemName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.Contains(input, "\n") {
		return s.handleInvoiceBulkItems(ctx, channel, recipient, user, session, input)
	}
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 3 {
		name, qtyStr, priceStr, ok := parseInvoiceSingleItemConcat(input, s.cfg.PaymentMaxKobo)
		if ok {
			return s.handleInvoiceSingleItemConcat(ctx, channel, recipient, user, session, name, qtyStr, priceStr)
		}
	}

	name := strings.TrimSpace(input)
	if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
		return s.sendText(ctx, channel, recipient, "Send an item description between 2 and 120 characters.")
	}
	session.State = "invoice_item_quantity"
	session.Data["invoice_item_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Quantity for this item? Send a whole number, for example: 1")
}

func (s *ConversationService) handleInvoiceItemQuantity(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	qty, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || qty < 1 || qty > 1000 {
		return s.sendText(ctx, channel, recipient, "Send a quantity between 1 and 1000.")
	}
	session.State = "invoice_item_unit_price"
	session.Data["invoice_item_quantity"] = strconv.Itoa(qty)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Unit price for this item? Send the amount in naira, for example: 2500")
}

func (s *ConversationService) handleInvoiceItemUnitPrice(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	price, err := domain.ParseNGNAmount(input, 100, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	qty, _ := strconv.Atoi(session.Data["invoice_item_quantity"])
	items, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	items = append(items, store.InvoiceItem{
		Description:   session.Data["invoice_item_name"],
		Quantity:      qty,
		UnitPriceKobo: price,
		LineTotalKobo: int64(qty) * price,
		SortOrder:     len(items) + 1,
	})
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}
	delete(session.Data, "invoice_item_name")
	delete(session.Data, "invoice_item_quantity")
	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceAddItemPrompt(ctx, channel, recipient, session)
}

func (s *ConversationService) handleInvoiceSingleItemConcat(ctx context.Context, channel, recipient string, user store.User, session store.Session, name, qtyStr, priceStr string) error {
	qty, _ := strconv.Atoi(qtyStr)
	price, err := domain.ParseNGNAmount(priceStr, 100, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}

	existing, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	existing = append(existing, store.InvoiceItem{
		Description:   name,
		Quantity:      qty,
		UnitPriceKobo: price,
		LineTotalKobo: int64(qty) * price,
		SortOrder:     len(existing) + 1,
	})
	if err := putInvoiceItems(&session, existing); err != nil {
		return err
	}

	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceAddItemPrompt(ctx, channel, recipient, session)
}

func (s *ConversationService) handleInvoiceBulkItems(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	parsed := parseInvoiceBulkItems(input, s.cfg.PaymentMaxKobo)

	var allErrors []string
	for i, item := range parsed {
		for _, e := range item.Errors {
			allErrors = append(allErrors, fmt.Sprintf("Item %d (%s): %s", i+1, item.Name, e))
		}
	}
	if len(allErrors) > 0 {
		return s.sendText(ctx, channel, recipient, "Some items have issues:\n"+strings.Join(allErrors, "\n")+"\n\nPlease fix and try again, or send items one at a time.")
	}

	if len(parsed) == 0 {
		return s.sendText(ctx, channel, recipient, "Send at least one item. Format: name, quantity, price (one per line).")
	}
	if len(parsed) > 10 {
		return s.sendText(ctx, channel, recipient, "You can add up to 10 items at once. Please split into smaller batches.")
	}

	var items []store.InvoiceItem
	for _, p := range parsed {
		qty, _ := strconv.Atoi(p.Quantity)
		price, _ := domain.ParseNGNAmount(p.Price, 100, s.cfg.PaymentMaxKobo)
		items = append(items, store.InvoiceItem{
			Description:   p.Name,
			Quantity:      qty,
			UnitPriceKobo: price,
			LineTotalKobo: int64(qty) * price,
			SortOrder:     len(items) + 1,
		})
	}

	existing, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	items = append(existing, items...)
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}

	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceAddItemPrompt(ctx, channel, recipient, session)
}

func (s *ConversationService) handleInvoiceAddItem(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "invoice_add_yes", "yes", "add", "add item":
		session.State = "invoice_item_name"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the next item name or description.")
	case "invoice_add_no", "no", "continue", "done":
		session.State = "invoice_delivery_fee"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Delivery fee and due date? Send both or one at a time.\n\nExamples:\n• 500, 15 Aug\n• 0, now\n• 500 (due immediately)\n• now (no fee, immediate)")
	case "invoice_edit_yes", "edit", "edit item":
		items, err := invoiceItemsFromSession(session)
		if err != nil || len(items) == 0 {
			return s.sendText(ctx, channel, recipient, "No items to edit. Send an item name to add one.")
		}
		session.State = "invoice_edit_item"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, invoiceItemsNumbered(items)+"\n\nSend the item number to edit (e.g. 1)")
	case "invoice_remove_yes", "remove", "remove item":
		items, err := invoiceItemsFromSession(session)
		if err != nil || len(items) == 0 {
			return s.sendText(ctx, channel, recipient, "No items to remove.")
		}
		session.State = "invoice_remove_item"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, invoiceItemsNumbered(items)+"\n\nSend the item number to remove (e.g. 1)")
	default:
		return s.sendInvoiceAddItemPrompt(ctx, channel, recipient, session)
	}
}

func (s *ConversationService) handleInvoiceEditItem(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	idx, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || idx < 1 || idx > len(items) {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Send a number between 1 and %d.", len(items)))
	}
	session.State = "invoice_edit_field"
	session.Data["invoice_edit_index"] = strconv.Itoa(idx - 1)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	item := items[idx-1]
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Editing item %d: %s (Qty %d × %s)\n\nWhat do you want to change?\n• name\n• quantity\n• price", idx, item.Description, item.Quantity, domain.FormatNGN(item.UnitPriceKobo)))
}

func (s *ConversationService) handleInvoiceEditField(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	field := strings.ToLower(strings.TrimSpace(input))
	switch field {
	case "name", "description", "item name":
		session.State = "invoice_edit_value"
		session.Data["invoice_edit_field"] = "name"
	case "quantity", "qty", "count":
		session.State = "invoice_edit_value"
		session.Data["invoice_edit_field"] = "quantity"
	case "price", "unit price", "amount":
		session.State = "invoice_edit_value"
		session.Data["invoice_edit_field"] = "price"
	default:
		return s.sendText(ctx, channel, recipient, "Send name, quantity, or price.")
	}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	idx, _ := strconv.Atoi(session.Data["invoice_edit_index"])
	item := items[idx]
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Send the new %s for \"%s\".", field, item.Description))
}

func (s *ConversationService) handleInvoiceEditValue(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	idx, _ := strconv.Atoi(session.Data["invoice_edit_index"])
	if idx < 0 || idx >= len(items) {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	item := &items[idx]
	field := session.Data["invoice_edit_field"]
	switch field {
	case "name":
		name := strings.TrimSpace(input)
		if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
			return s.sendText(ctx, channel, recipient, "Send an item description between 2 and 120 characters.")
		}
		item.Description = name
	case "quantity":
		qty, err := strconv.Atoi(strings.TrimSpace(input))
		if err != nil || qty < 1 || qty > 1000 {
			return s.sendText(ctx, channel, recipient, "Send a quantity between 1 and 1000.")
		}
		item.Quantity = qty
		item.LineTotalKobo = int64(qty) * item.UnitPriceKobo
	case "price":
		price, err := domain.ParseNGNAmount(input, 100, s.cfg.PaymentMaxKobo)
		if err != nil {
			return s.sendText(ctx, channel, recipient, err.Error())
		}
		item.UnitPriceKobo = price
		item.LineTotalKobo = int64(item.Quantity) * price
	}
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}
	delete(session.Data, "invoice_edit_index")
	delete(session.Data, "invoice_edit_field")
	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, invoiceItemsActionList(recipient, invoiceItemsSummary("Item updated. Current invoice items:", items)+"\n\nWhat next?"))
}

func (s *ConversationService) handleInvoiceRemoveItem(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	idx, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || idx < 1 || idx > len(items) {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Send a number between 1 and %d.", len(items)))
	}
	removed := items[idx-1]
	items = append(items[:idx-1], items[idx:]...)
	for i := range items {
		items[i].SortOrder = i + 1
	}
	if len(items) == 0 {
		items = []store.InvoiceItem{}
	}
	if err := putInvoiceItems(&session, items); err != nil {
		return err
	}
	session.State = "invoice_add_item"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	summary := fmt.Sprintf("Removed: %s", removed.Description)
	if len(items) > 0 {
		return s.sendInteractive(ctx, channel, invoiceItemsActionList(recipient, summary+"\n\n"+invoiceItemsSummary("Current invoice items:", items)+"\n\nWhat next?"))
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: summary + "\n\nNo items left. Send an item name or description to add one.",
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_add_yes", Title: "Add item"},
		},
	})
}

func invoiceItemsNumbered(items []store.InvoiceItem) string {
	lines := []string{}
	for i, item := range items {
		lines = append(lines, fmt.Sprintf("%d. %s — Qty %d × %s", i+1, item.Description, item.Quantity, domain.FormatNGN(item.UnitPriceKobo)))
	}
	return strings.Join(lines, "\n")
}

func invoiceItemsActionList(to, body string) ports.InteractiveMessage {
	return ports.InteractiveMessage{
		To:          to,
		Body:        body,
		ButtonLabel: "Actions",
		Sections: []ports.InteractiveSection{{
			Title: "Invoice items",
			Rows: []ports.InteractiveRow{
				{ID: "invoice_add_yes", Title: "Add item", Description: "Add another item"},
				{ID: "invoice_edit_yes", Title: "Edit item", Description: "Change an existing item"},
				{ID: "invoice_remove_yes", Title: "Remove item", Description: "Remove an item"},
				{ID: "invoice_add_no", Title: "Continue", Description: "Proceed to delivery fee and due date"},
			},
		}},
	}
}

func (s *ConversationService) sendInvoiceAddItemPrompt(ctx context.Context, channel, recipient string, session store.Session) error {
	items, err := invoiceItemsFromSession(session)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "That invoice session expired. Please start again.")
	}
	if len(items) > 0 {
		return s.sendInteractive(ctx, channel, invoiceItemsActionList(recipient, invoiceItemsSummary("Current invoice items:", items)))
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: "Add invoice items.\n\nSend one item per line: Name, Quantity, Price\nExample: Website design, 1, 25000\n\nOr send just the item name to enter details one at a time.",
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_add_yes", Title: "Add item"},
		},
	})
}

func (s *ConversationService) handleInvoiceDeliveryFee(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	fee := int64(0)
	var dueAt time.Time
	dueAtSet := false

	parts := strings.Split(input, ",")
	feeStr := strings.TrimSpace(parts[0])
	dueStr := ""
	if len(parts) > 1 {
		dueStr = strings.TrimSpace(parts[1])
	}

	switch strings.ToLower(feeStr) {
	case "", "none", "0", "no fee", "no delivery":
		fee = 0
	default:
		amount, err := domain.ParseNGNAmount(feeStr, 0, s.cfg.PaymentMaxKobo)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "Send the delivery fee as a naira amount, or 0 if none.\n\nExamples:\n• 500, 15 Aug\n• 0, now\n• 500 (due immediately)\n• now (no fee, immediate)")
		}
		fee = amount
	}

	if dueStr == "" {
		dueStr = feeStr
		if fee == 0 {
			dueStr = ""
		}
	}

	switch strings.ToLower(dueStr) {
	case "", "now", "skip", "none", "immediate":
		dueAt = time.Now()
		dueAtSet = true
	default:
		parsed, err := time.Parse("2006-01-02", dueStr)
		if err != nil {
			parsed, err = time.Parse("2 Jan 2006", dueStr)
		}
		if err != nil {
			parsed, err = time.Parse("2 Jan", dueStr)
		}
		if err != nil {
			return s.sendText(ctx, channel, recipient, "I couldn't understand the date. Send:\n• 500, 15 Aug\n• 0, now\n• 500 (due immediately)\n• now (no fee, immediate)")
		}
		if parsed.Year() == 0 {
			parsed = parsed.AddDate(time.Now().Year(), 0, 0)
		}
		dueAt = parsed
		dueAtSet = true
	}

	if !dueAtSet {
		dueAt = time.Now()
	}

	session.State = "invoice_review"
	session.Data["invoice_delivery_fee_kobo"] = strconv.FormatInt(fee, 10)
	session.Data["invoice_due_date"] = dueAt.Format(time.RFC3339)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceReview(ctx, channel, recipient, user, session)
}

func (s *ConversationService) handleInvoiceDueDate(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	var dueAt time.Time
	switch strings.ToLower(input) {
	case "", "now", "skip", "none", "immediate":
		dueAt = time.Now()
	default:
		parsed, err := time.Parse("2006-01-02", input)
		if err != nil {
			parsed, err = time.Parse("2 Jan 2006", input)
		}
		if err != nil {
			parsed, err = time.Parse("2 Jan", input)
		}
		if err != nil {
			return s.sendText(ctx, channel, recipient, "I couldn't understand that date. Send a date like 15 Aug or 2026-08-15, or send NOW for immediate due date.")
		}
		if parsed.Year() == 0 {
			parsed = parsed.AddDate(time.Now().Year(), 0, 0)
		}
		dueAt = parsed
	}
	session.State = "invoice_review"
	session.Data["invoice_due_date"] = dueAt.Format(time.RFC3339)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceReview(ctx, channel, recipient, user, session)
}

func (s *ConversationService) handleInvoiceReview(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "invoice_confirm" && !strings.EqualFold(input, "confirm") {
		return s.sendInvoiceReview(ctx, channel, recipient, user, session)
	}
	merchant, items, fee, dueAt, err := s.invoiceDraft(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	invoice, err := s.store.CreateInvoice(ctx, store.InvoiceSpec{
		MerchantID:             merchant.ID,
		CreatedByUserID:        user.ID,
		CustomerWhatsAppNumber: session.Data["invoice_customer_phone"],
		CustomerEmail:          session.Data["invoice_customer_email"],
		DeliveryFeeKobo:        fee,
		DueAt:                  &dueAt,
		Items:                  items,
	})
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	s.notifyInvoiceCustomer(ctx, invoice)
	link := s.cfg.BaseURL + "/invoices/" + invoice.Reference
	if err := s.sendText(ctx, channel, recipient, fmt.Sprintf("Invoice generated.\n\nMerchant: %s\nCustomer: %s\nTotal: %s\nReference: %s\nLink: %s\n\nThe customer can open the link or send PAY %s to Xego on WhatsApp to pay. Split payment is decided by the paying customer; Xego marks the invoice paid when total collected reaches the invoice total.",
		invoice.MerchantName, invoice.CustomerWhatsAppNumber, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link, invoice.Reference)); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient)
}

func (s *ConversationService) startInvoicePayment(ctx context.Context, channel, recipient string, user store.User, session store.Session, reference string) error {
	invoice, err := s.store.InvoiceByReference(ctx, reference)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find that invoice. Check the invoice reference and send it like this: PAY XG-INV-1234ABCD")
	}
	if invoice.Status == "paid" || invoice.AmountPaidKobo >= invoice.TotalKobo {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invoice %s is already fully paid.\n\nMerchant: %s\nTotal: %s", invoice.Reference, invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo)))
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	session.State = "invoice_pay_amount"
	session.Data = map[string]string{"invoice_reference": invoice.Reference}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Invoice %s\n\nMerchant: %s\nTotal: %s\nPaid so far: %s\nRemaining: %s\n\nHow much would you like to pay now?\nSend FULL to pay the remaining balance, or enter a naira amount for a split/partial payment.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(remaining)))
}

func (s *ConversationService) handleInvoicePayAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please send PAY followed by the invoice reference again.")
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	if remaining <= 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice is already fully paid.")
	}
	merchant, err := s.store.MerchantByID(ctx, invoice.MerchantID)
	if err != nil {
		return err
	}
	isFullPay := strings.EqualFold(strings.TrimSpace(input), "full")
	if !merchant.AllowPartialPayments && !isFullPay {
		return s.sendText(ctx, channel, recipient, "This merchant does not accept partial payments. Please send FULL to pay the remaining balance.")
	}
	var amount int64
	if isFullPay {
		amount = remaining
	} else {
		parsed, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, remaining)
		if err != nil {
			return s.sendText(ctx, channel, recipient, fmt.Sprintf("Enter an amount between %s and %s, or send FULL to pay the remaining balance.", domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(remaining)))
		}
		amount = parsed
	}
	if merchant.AllowPartialPayments && amount < remaining {
		if merchant.MinInvoiceAmountKobo > 0 && invoice.TotalKobo < merchant.MinInvoiceAmountKobo {
			return s.sendText(ctx, channel, recipient, fmt.Sprintf("This invoice is below the minimum of %s for installment payments. Please send FULL to pay in full.", domain.FormatNGN(merchant.MinInvoiceAmountKobo)))
		}
		if merchant.UpfrontPercent > 0 {
			upfrontMin := invoice.TotalKobo * int64(merchant.UpfrontPercent) / 100
			alreadyPaid := invoice.AmountPaidKobo
			if alreadyPaid == 0 && amount < upfrontMin {
				return s.sendText(ctx, channel, recipient, fmt.Sprintf("The upfront payment must be at least %d%% of the invoice total (%s). Enter a higher amount or send FULL.", merchant.UpfrontPercent, domain.FormatNGN(upfrontMin)))
			}
		}
		if merchant.MinInstallmentPercent > 0 && invoice.AmountPaidKobo > 0 {
			installMin := invoice.TotalKobo * int64(merchant.MinInstallmentPercent) / 100
			if amount < installMin {
				return s.sendText(ctx, channel, recipient, fmt.Sprintf("Each installment must be at least %d%% of the invoice total (%s). Enter a higher amount or send FULL.", merchant.MinInstallmentPercent, domain.FormatNGN(installMin)))
			}
		}
		if merchant.MaxInstallments > 0 {
			paymentCount, _ := s.store.CountInvoicePayments(ctx, invoice.ID)
			if paymentCount >= int64(merchant.MaxInstallments) {
				return s.sendText(ctx, channel, recipient, fmt.Sprintf("This invoice has reached the maximum of %d installments. Please send FULL to pay the remaining balance.", merchant.MaxInstallments))
			}
		}
	}
	session.State = "invoice_pay_method"
	session.Data["invoice_pay_amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay invoice %s\n\nMerchant: %s\nAmount now: %s\nRemaining after this payment: %s\n\nChoose a payment method.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount), domain.FormatNGN(remaining-amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "method_wallet", Title: "Pay from wallet"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleInvoicePayMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, amount, err := s.invoicePaymentSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice payment session expired. Please send PAY followed by the invoice reference again.")
	}
	merchant, err := s.store.MerchantBySlug(ctx, invoice.MerchantSlug)
	if err != nil {
		return err
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.createPaymentDraft(ctx, user, merchant, amount, ProviderInterswitch, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amount); err != nil {
			return err
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nInvoice: %s\nMerchant: %s\nAmount: %s\n\nXego will update the invoice only after payment is verified.",
				invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount)),
			s.payments.HostedCheckoutURL(payment))
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, err := s.createPaymentDraft(ctx, user, merchant, amount, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amount); err != nil {
			return err
		}
		session.Data["payment_id"] = payment.ID.String()
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your secure checkout is ready.\n\nInvoice: %s\nMerchant: %s\nAmount: %s\n\nXego will update the invoice only after payment is verified.",
				invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount)),
			s.payments.HostedCheckoutURL(payment))
	case "method_wallet", "wallet", "pay from wallet":
		payment, err := s.createPaymentDraft(ctx, user, merchant, amount, ProviderWallet, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.CreateInvoicePayment(ctx, invoice.ID, payment.ID, user.ID, amount); err != nil {
			return err
		}
		if err := s.beginWalletConfirm(ctx, channel, recipient, user, session, payment); err != nil {
			return err
		}
		return s.sendInvoiceWalletReview(ctx, channel, recipient, user, invoice, amount)
	default:
		session.State = "invoice_pay_amount"
		_ = s.saveSession(ctx, session)
		return s.handleInvoicePayAmount(ctx, channel, recipient, user, session, "full")
	}
}

func (s *ConversationService) handleInvoicePayBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	invoice, _, err := s.invoicePaymentSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice payment session expired. Please send PAY followed by the invoice reference again.")
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
	session.State = "await_invoice_bank_transfer"
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInvoiceBankTransferInstructions(ctx, channel, recipient, payment, invoice, instruction)
}

func (s *ConversationService) sendInvoicePayMethods(ctx context.Context, channel, recipient string, invoice store.InvoiceView, amount int64) error {
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay invoice %s\n\nMerchant: %s\nAmount now: %s\nRemaining after this payment: %s\n\nChoose a payment method.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount), domain.FormatNGN(remaining-amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "method_wallet", Title: "Pay from wallet"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

// sendInvoiceWalletReview confirms an instant wallet-funded invoice payment
// of the given partial/full amount (invoices add no collection surcharge).
func (s *ConversationService) sendInvoiceWalletReview(ctx context.Context, channel, recipient string, user store.User, invoice store.InvoiceView, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay invoice %s from your Xego wallet:\n\nMerchant: %s\nAmount: %s%s\n\nPay instantly from your Xego wallet?",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(amount),
			s.walletBalanceLine(ctx, user, amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Pay from wallet"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleInvoiceBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		invoice, _, err := s.invoicePaymentSession(ctx, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendInvoiceBankTransferInstructions(ctx, channel, recipient, payment, invoice, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	updated, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	invoice, err := s.store.InvoiceByPaymentID(ctx, updated.ID)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Thanks. Xego has recorded your transfer confirmation.")
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thanks. Xego has recorded your transfer confirmation.\n\nInvoice: %s\nPaid now: %s\nInvoice status: %s\nTotal collected: %s of %s",
		invoice.Reference, domain.FormatNGN(updated.AmountKobo), strings.ToUpper(invoice.Status), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(invoice.TotalKobo)))
}

func (s *ConversationService) sendInvoiceBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, invoice store.InvoiceView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details for invoice %s\n\nMerchant: %s\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nPayment reference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above.\n3. Put the payment reference exactly in the narration, remark, or payment reference field.\n4. After sending, tap I have transferred.\n\nXego adds this receipt to the invoice after confirmation. The invoice is fully paid only when total collected reaches %s.",
			invoice.Reference, invoice.MerchantName, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference, domain.FormatNGN(invoice.TotalKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendInvoiceReview(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	merchant, items, fee, dueAt, err := s.invoiceDraft(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That invoice session expired. Please start again.")
	}
	subtotal := invoiceSubtotal(items)
	total := subtotal + fee
	body := invoiceItemsSummary("Review invoice", items)
	body += fmt.Sprintf("\n\nMerchant: %s\nCustomer WhatsApp: %s\nCustomer email: %s\nSubtotal: %s\nDelivery fee: %s\nTotal: %s\nDue: %s\n\nGenerate this invoice?",
		merchant.Name, session.Data["invoice_customer_phone"], session.Data["invoice_customer_email"], domain.FormatNGN(subtotal), domain.FormatNGN(fee), domain.FormatNGN(total), dueAt.Format("02 Jan 2006"))
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: body,
		Buttons: []ports.InteractiveButton{
			{ID: "invoice_confirm", Title: "Generate"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) invoiceDraft(ctx context.Context, user store.User, session store.Session) (store.Merchant, []store.InvoiceItem, int64, time.Time, error) {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return store.Merchant{}, nil, 0, time.Time{}, err
	}
	slug := session.Data["invoice_merchant_slug"]
	var merchant store.Merchant
	found := false
	for _, candidate := range merchants {
		if candidate.Slug == slug {
			merchant = candidate
			found = true
			break
		}
	}
	if !found {
		return store.Merchant{}, nil, 0, time.Time{}, fmt.Errorf("merchant not owned by user")
	}
	items, err := invoiceItemsFromSession(session)
	if err != nil || len(items) == 0 {
		return store.Merchant{}, nil, 0, time.Time{}, fmt.Errorf("missing invoice items")
	}
	fee, _ := strconv.ParseInt(session.Data["invoice_delivery_fee_kobo"], 10, 64)
	dueAt := time.Now()
	if raw := session.Data["invoice_due_date"]; raw != "" {
		if parsed, pErr := time.Parse(time.RFC3339, raw); pErr == nil {
			dueAt = parsed
		}
	}
	return merchant, items, fee, dueAt, nil
}

func (s *ConversationService) invoicePaymentSession(ctx context.Context, session store.Session) (store.InvoiceView, int64, error) {
	invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
	if err != nil {
		return store.InvoiceView{}, 0, err
	}
	amount, err := strconv.ParseInt(session.Data["invoice_pay_amount_kobo"], 10, 64)
	if err != nil || amount <= 0 {
		return store.InvoiceView{}, 0, fmt.Errorf("invalid invoice payment amount")
	}
	return invoice, amount, nil
}

func invoiceItemsFromSession(session store.Session) ([]store.InvoiceItem, error) {
	raw := strings.TrimSpace(session.Data["invoice_items"])
	if raw == "" {
		raw = "[]"
	}
	var items []store.InvoiceItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func putInvoiceItems(session *store.Session, items []store.InvoiceItem) error {
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.Data["invoice_items"] = string(raw)
	return nil
}

func invoiceSubtotal(items []store.InvoiceItem) int64 {
	total := int64(0)
	for _, item := range items {
		line := item.LineTotalKobo
		if line == 0 {
			line = int64(item.Quantity) * item.UnitPriceKobo
		}
		total += line
	}
	return total
}

func invoiceItemsSummary(prefix string, items []store.InvoiceItem) string {
	lines := []string{prefix}
	for i, item := range items {
		line := item.LineTotalKobo
		if line == 0 {
			line = int64(item.Quantity) * item.UnitPriceKobo
		}
		lines = append(lines, fmt.Sprintf("%d. %s — Qty %d × %s = %s", i+1, item.Description, item.Quantity, domain.FormatNGN(item.UnitPriceKobo), domain.FormatNGN(line)))
	}
	return strings.Join(lines, "\n")
}

func invoiceReferenceFromPAY(input string) (string, bool) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(input)))
	if len(fields) != 2 || fields[0] != "PAY" || !strings.HasPrefix(fields[1], "XG-INV-") {
		return "", false
	}
	return fields[1], true
}

func (s *ConversationService) notifyInvoiceCustomer(ctx context.Context, invoice store.InvoiceView) {
	link := s.cfg.BaseURL + "/invoices/" + invoice.Reference
	emailBody := fmt.Sprintf("Xego invoice from %s\n\nAmount: %s\nReference: %s\nLink: %s\n\nTo pay, visit the link above or open WhatsApp and send PAY %s to Xego.",
		invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link, invoice.Reference)
	if s.email != nil && invoice.CustomerEmail != "" {
		_ = s.email.Send(ctx, invoice.CustomerEmail, "Xego invoice "+invoice.Reference, emailBody)
	}
	if invoice.CustomerWhatsAppNumber != "" {
		buttonID := "pay_invoice:" + invoice.Reference
		_ = s.sendInteractive(ctx, ChannelWhatsApp, ports.InteractiveMessage{
			To: invoice.CustomerWhatsAppNumber,
			Body: fmt.Sprintf("Xego invoice from %s\n\nAmount: %s\nReference: %s\nLink: %s\n\nYou may pay the full balance or choose a split/partial amount during payment.",
				invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link),
			Buttons: []ports.InteractiveButton{
				{ID: buttonID, Title: "Pay now"},
			},
		})
	}
}

type invoiceBulkItem struct {
	Name     string
	Quantity string
	Price    string
	Errors   []string
}

func parseInvoiceSingleItemConcat(input string, maxPriceKobo int64) (name, qtyStr, priceStr string, ok bool) {
	fields := parseCommaSeparatedFields(input)
	if len(fields) < 3 {
		return "", "", "", false
	}

	priceStr = fields[len(fields)-1]
	qtyStr = fields[len(fields)-2]

	qty, err := strconv.Atoi(qtyStr)
	if err != nil || qty < 1 || qty > 1000 {
		return "", "", "", false
	}

	if _, err := domain.ParseNGNAmount(priceStr, 100, maxPriceKobo); err != nil {
		return "", "", "", false
	}

	name = strings.Join(fields[:len(fields)-2], ", ")
	if len([]rune(name)) < 2 || len([]rune(name)) > 120 {
		return "", "", "", false
	}

	return name, qtyStr, priceStr, true
}

func parseInvoiceBulkItems(input string, maxPriceKobo int64) []invoiceBulkItem {
	lines := strings.Split(input, "\n")
	var items []invoiceBulkItem
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := parseCommaSeparatedFields(line)
		if len(fields) < 1 {
			continue
		}
		item := invoiceBulkItem{}

		if len(fields) >= 3 {
			item.Price = fields[len(fields)-1]
			item.Quantity = fields[len(fields)-2]
			item.Name = strings.Join(fields[:len(fields)-2], ", ")
		} else if len(fields) == 2 {
			item.Name = fields[0]
			item.Quantity = fields[1]
		} else {
			item.Name = fields[0]
		}

		if len([]rune(item.Name)) < 2 || len([]rune(item.Name)) > 120 {
			item.Errors = append(item.Errors, "Item name should be between 2 and 120 characters.")
		}
		if item.Quantity != "" {
			qty, err := strconv.Atoi(item.Quantity)
			if err != nil || qty < 1 || qty > 1000 {
				item.Errors = append(item.Errors, "Quantity should be between 1 and 1000.")
			}
		}
		if item.Price != "" {
			if _, err := domain.ParseNGNAmount(item.Price, 100, maxPriceKobo); err != nil {
				item.Errors = append(item.Errors, "Price: "+err.Error())
			}
		}

		items = append(items, item)
	}
	return items
}
