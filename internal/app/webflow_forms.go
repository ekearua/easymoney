package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

type wfInvoiceItemDraft struct {
	Description   string
	Quantity      int
	UnitPriceKobo int64
}

func wfInvoiceItemsFromPayload(payload map[string]string) ([]wfInvoiceItemDraft, error) {
	var items []wfInvoiceItemDraft
	raw := payload["invoice_items"]
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func wfInvoiceItemsToPayload(items []wfInvoiceItemDraft) (string, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// =========================================================================
// invoice_create — generate an invoice for a customer
// =========================================================================

func (a *App) wfInvoiceCreateStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		merchants, err := a.store.ApprovedMerchantsForUser(r.Context(), user.ID)
		if err != nil {
			return page, err
		}
		if len(merchants) == 0 {
			page.Title = "No approved merchant"
			page.Intro = "Invoice generation is available after your merchant registration is approved. Register your business from the Xego menu."
			page.Done = true
			return page, nil
		}
		page.Title = "Create an invoice"
		page.Intro = "Which approved merchant should issue this invoice?"
		page.Fields = []webFlowField{{Name: "merchant_slug", Label: "Merchant", Type: "select", Required: true, Options: merchantSelectOptions(merchants)}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "customer":
		page.Title = "Invoice customer"
		page.Intro = "Who is the invoice for?"
		page.Fields = []webFlowField{
			{Name: "customer_phone", Label: "Customer WhatsApp number", Type: "tel", Required: true, Hint: "e.g. +2348012345678"},
			{Name: "customer_email", Label: "Customer email", Type: "email", Hint: "For the invoice copy (optional)"},
		}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "items":
		items, err := wfInvoiceItemsFromPayload(flow.Payload)
		if err != nil {
			return page, err
		}
		page.Title = "Invoice items"
		page.Intro = "Add line items. Each item: name, quantity and unit price in naira."
		for _, item := range items {
			page.Review = append(page.Review, webFlowLine{
				Term: item.Description + " × " + strconv.Itoa(item.Quantity),
				Desc: domain.FormatNGN(int64(item.Quantity) * item.UnitPriceKobo),
			})
		}
		page.Fields = []webFlowField{
			{Name: "item_name", Label: "Item description", Type: "text"},
			{Name: "item_quantity", Label: "Quantity", Type: "number", Value: "1"},
			{Name: "item_price", Label: "Unit price (naira)", Type: "amount"},
		}
		page.Actions = []webFlowAction{
			{Name: "add_item", Label: "Add item"},
			{Name: "items_done", Label: "Done — delivery and date"},
		}
		return page, nil
	case "options":
		page.Title = "Delivery and due date"
		page.Fields = []webFlowField{
			{Name: "delivery_fee", Label: "Delivery fee (naira, optional)", Type: "amount", Value: flow.Payload["delivery_fee_kobo"]},
			{Name: "due_date", Label: "Due date (YYYY-MM-DD, optional)", Type: "date", Value: flow.Payload["due_date"]},
		}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back to items"}}
		return page, nil
	case "review":
		merchant, err := a.store.MerchantBySlug(r.Context(), flow.Payload["merchant_slug"])
		if err != nil {
			return page, err
		}
		items, err := wfInvoiceItemsFromPayload(flow.Payload)
		if err != nil {
			return page, err
		}
		fee := wfInt(flow.Payload["delivery_fee_kobo"])
		var total int64
		page.Review = append(page.Review,
			webFlowLine{Term: "Merchant", Desc: merchant.Name},
			webFlowLine{Term: "Customer", Desc: flow.Payload["customer_phone"] + " · " + flow.Payload["customer_email"]})
		for _, item := range items {
			line := int64(item.Quantity) * item.UnitPriceKobo
			total += line
			page.Review = append(page.Review, webFlowLine{Term: item.Description + " × " + strconv.Itoa(item.Quantity), Desc: domain.FormatNGN(line)})
		}
		if fee > 0 {
			total += fee
			page.Review = append(page.Review, webFlowLine{Term: "Delivery fee", Desc: domain.FormatNGN(fee)})
		}
		page.Review = append(page.Review, webFlowLine{Term: "Total", Desc: domain.FormatNGN(total)})
		page.Title = "Review invoice"
		page.Actions = []webFlowAction{{Name: "create", Label: "Create invoice"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown invoice_create step %q", flow.Step)
}

func (a *App) wfInvoiceCreateSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		slug := strings.TrimSpace(r.FormValue("merchant_slug"))
		if slug == "" {
			return a.wfPageWithError(flow, page, "Choose a merchant."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["merchant_slug"] = slug
		a.wfAdvance(w, r, flow, "customer", payload)
		return nil, nil
	case "customer":
		phone, err := domain.NormalizeNigerianPhone(strings.TrimSpace(r.FormValue("customer_phone")))
		if err != nil {
			return a.wfPageWithError(flow, page, "Send a valid Nigerian WhatsApp number for the customer."), nil
		}
		if !a.conversation.InvoiceNumberAccepted(phone) {
			return a.wfPageWithError(flow, page, a.conversation.InvoiceRejectedMessage()), nil
		}
		email := strings.TrimSpace(r.FormValue("customer_email"))
		if email != "" {
			parsed, ok := validWebEmail(email)
			if !ok {
				return a.wfPageWithError(flow, page, "That email doesn't look valid."), nil
			}
			email = parsed
		}
		payload := clonePayload(flow.Payload)
		payload["customer_phone"] = phone
		payload["customer_email"] = email
		payload["invoice_items"] = "[]"
		a.wfAdvance(w, r, flow, "items", payload)
		return nil, nil
	case "items":
		items, err := wfInvoiceItemsFromPayload(flow.Payload)
		if err != nil {
			return a.wfPageWithError(flow, page, "Your invoice session is invalid. Please start again."), nil
		}
		payload := clonePayload(flow.Payload)
		switch action {
		case "add_item":
			name := strings.TrimSpace(r.FormValue("item_name"))
			qty, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("item_quantity")))
			if qty < 1 {
				qty = 1
			}
			price, err := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("item_price")), 100, a.cfg.PaymentMaxKobo)
			if err != nil || name == "" || len([]rune(name)) > 120 {
				return a.wfPageWithError(flow, page, "Each item needs a description and a unit price in naira (min ₦1)."), nil
			}
			items = append(items, wfInvoiceItemDraft{Description: name, Quantity: qty, UnitPriceKobo: price})
			raw, _ := wfInvoiceItemsToPayload(items)
			payload["invoice_items"] = raw
			a.wfAdvance(w, r, flow, "items", payload)
			return nil, nil
		case "items_done":
			if len(items) == 0 {
				return a.wfPageWithError(flow, page, "Add at least one item to the invoice."), nil
			}
			raw, _ := wfInvoiceItemsToPayload(items)
			payload["invoice_items"] = raw
			a.wfAdvance(w, r, flow, "options", payload)
			return nil, nil
		case "back":
			a.wfAdvance(w, r, flow, "customer", flow.Payload)
			return nil, nil
		}
		return a.wfPageWithError(flow, page, "Choose an action."), nil
	case "options":
		fee, _ := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("delivery_fee")), 0, 1<<40)
		payload := clonePayload(flow.Payload)
		payload["delivery_fee_kobo"] = strconv.FormatInt(fee, 10)
		payload["due_date"] = strings.TrimSpace(r.FormValue("due_date"))
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "create" {
			return a.wfPageWithError(flow, page, "Tap Create invoice to finish."), nil
		}
		merchant, err := a.store.MerchantBySlug(r.Context(), flow.Payload["merchant_slug"])
		if err != nil {
			return a.wfPageWithError(flow, page, "That merchant is no longer available."), nil
		}
		itemsDraft, err := wfInvoiceItemsFromPayload(flow.Payload)
		if err != nil || len(itemsDraft) == 0 {
			return a.wfPageWithError(flow, page, "Add at least one item."), nil
		}
		items := make([]store.InvoiceItem, 0, len(itemsDraft))
		for i, item := range itemsDraft {
			items = append(items, store.InvoiceItem{
				Description: item.Description, Quantity: item.Quantity,
				UnitPriceKobo: item.UnitPriceKobo, LineTotalKobo: int64(item.Quantity) * item.UnitPriceKobo,
				SortOrder: i + 1,
			})
		}
		fee := wfInt(flow.Payload["delivery_fee_kobo"])
		dueAt := time.Now().Add(30 * 24 * time.Hour)
		if raw := flow.Payload["due_date"]; raw != "" {
			if parsed, err := time.Parse("2006-01-02", raw); err == nil {
				dueAt = parsed
			}
		}
		invoice, err := a.store.CreateInvoice(r.Context(), store.InvoiceSpec{
			MerchantID: merchant.ID, CreatedByUserID: user.ID,
			CustomerWhatsAppNumber: flow.Payload["customer_phone"],
			CustomerEmail:          flow.Payload["customer_email"],
			DeliveryFeeKobo:        fee, DueAt: &dueAt, Items: items,
		})
		if err != nil {
			return a.wfPageWithError(flow, page, "The invoice could not be created. Please try again."), nil
		}
		a.conversation.NotifyInvoiceCustomer(r.Context(), invoice)
		link := a.cfg.BaseURL + "/invoices/" + invoice.Reference
		msg := fmt.Sprintf("Invoice created.\n\nMerchant: %s\nCustomer: %s\nTotal: %s\nReference: %s\n\nCustomer payment link: %s\nThey can also send PAY %s to Xego on WhatsApp.",
			invoice.MerchantName, invoice.CustomerWhatsAppNumber, domain.FormatNGN(invoice.TotalKobo), invoice.Reference, link, invoice.Reference)
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "The invoice was created but the confirmation could not be sent."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "Invoice created"
		done.DoneBody = "Reference " + invoice.Reference + " · Total " + domain.FormatNGN(invoice.TotalKobo) + ". The customer can pay from the link."
		done.DoneAction = webFlowAction{Kind: "link", Label: "View invoice", URL: link}
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// thrift_create — start a contribution group
// =========================================================================

func (a *App) wfThriftCreateStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		page.Title = "Create a thrift group"
		page.Intro = "Give your contribution group a name members will recognise."
		page.Fields = []webFlowField{{Name: "name", Label: "Group name", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "amount":
		page.Title = "Contribution amount"
		page.Intro = fmt.Sprintf("How much does each member contribute per cycle? Between %s and %s.", domain.FormatNGN(a.cfg.PaymentMinKobo), domain.FormatNGN(a.cfg.PaymentMaxKobo))
		page.Fields = []webFlowField{{Name: "amount_kobo", Label: "Amount (naira)", Type: "amount", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "frequency":
		page.Title = "Contribution frequency"
		page.Fields = []webFlowField{{Name: "frequency", Label: "Frequency", Type: "radio", Required: true, Options: []webFlowOption{
			{Value: "weekly", Label: "Weekly"},
			{Value: "monthly", Label: "Monthly"},
		}}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "target":
		page.Title = "Group size"
		page.Intro = "How many members (including you)? Between 2 and 12."
		page.Fields = []webFlowField{{Name: "target", Label: "Members", Type: "number", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		page.Title = "Review your thrift group"
		page.Review = []webFlowLine{
			{Term: "Name", Desc: flow.Payload["thrift_name"]},
			{Term: "Contribution", Desc: domain.FormatNGN(wfInt(flow.Payload["thrift_amount_kobo"])) + " " + titleCase(flow.Payload["thrift_frequency"])},
			{Term: "Members", Desc: "1 of " + flow.Payload["thrift_target"]},
		}
		page.Actions = []webFlowAction{{Name: "create", Label: "Create group"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown thrift_create step %q", flow.Step)
}

func (a *App) wfThriftCreateSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		name := strings.TrimSpace(r.FormValue("name"))
		if len([]rune(name)) < 2 || len([]rune(name)) > 60 {
			return a.wfPageWithError(flow, page, "Send a group name between 2 and 60 characters."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_name"] = name
		a.wfAdvance(w, r, flow, "amount", payload)
		return nil, nil
	case "amount":
		amount, err := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("amount_kobo")), a.cfg.PaymentMinKobo, a.cfg.PaymentMaxKobo)
		if err != nil {
			return a.wfPageWithError(flow, page, "Enter a valid contribution amount in naira."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_amount_kobo"] = strconv.FormatInt(amount, 10)
		a.wfAdvance(w, r, flow, "frequency", payload)
		return nil, nil
	case "frequency":
		frequency := strings.TrimSpace(r.FormValue("frequency"))
		if frequency != "weekly" && frequency != "monthly" {
			return a.wfPageWithError(flow, page, "Choose weekly or monthly."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_frequency"] = frequency
		a.wfAdvance(w, r, flow, "target", payload)
		return nil, nil
	case "target":
		target, err := strconv.Atoi(strings.TrimSpace(r.FormValue("target")))
		if err != nil || target < 2 || target > 12 {
			return a.wfPageWithError(flow, page, "Group size must be between 2 and 12 members."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_target"] = strconv.Itoa(target)
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "create" {
			return a.wfPageWithError(flow, page, "Tap Create group to finish."), nil
		}
		amount := wfInt(flow.Payload["thrift_amount_kobo"])
		target, _ := strconv.Atoi(flow.Payload["thrift_target"])
		group, err := a.store.CreateThriftGroup(r.Context(), user.ID, flow.Payload["thrift_name"], amount, flow.Payload["thrift_frequency"], target)
		if err != nil {
			return a.wfPageWithError(flow, page, "The group could not be created: "+err.Error()), nil
		}
		link := a.cfg.BaseURL + "/thrift/" + group.Name
		msg := fmt.Sprintf("Thrift group created.\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\n\nShare this link for members to join: %s\nOr tell them to send JOIN %s to Xego. When everyone has joined, send START %s to choose the payout rotation.",
			group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount, link, group.Name, group.Name)
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "The group was created but the confirmation could not be sent."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "Thrift group created"
		done.DoneBody = group.Name + " · " + domain.FormatNGN(group.ContributionAmountKobo) + " " + group.Frequency + " · " + strconv.Itoa(group.TargetMemberCount) + " members"
		done.DoneAction = webFlowAction{Kind: "link", Label: "View group page", URL: link}
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

func titleCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// =========================================================================
// thrift_join — join a group by name
// =========================================================================

func (a *App) wfThriftJoinGroup(r *http.Request, flow store.WebFlow) (store.ThriftGroupView, error) {
	name := strings.TrimSpace(flow.Payload["thrift_name"])
	if name == "" {
		name = strings.TrimSpace(flow.Payload["group_name"])
	}
	if name == "" {
		return store.ThriftGroupView{}, fmt.Errorf("no group name")
	}
	return a.store.ThriftGroupByName(r.Context(), name)
}

func (a *App) wfThriftJoinStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		page.Title = "Join a thrift group"
		page.Intro = "Send the group name you were invited to (e.g. Office Pool)."
		page.Fields = []webFlowField{{Name: "group_name", Label: "Group name", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "review":
		group, err := a.wfThriftJoinGroup(r, flow)
		if err != nil {
			page.Title = "Group not found"
			page.Intro = "No thrift group with that name was found. Check the name and reopen from WhatsApp."
			page.Done = true
			return page, nil
		}
		if group.CreatorUserID == user.ID {
			page.Title = "You created this group"
			page.Intro = "You are already the creator of " + group.Name + ". You can invite members from the group page."
			page.Done = true
			return page, nil
		}
		page.Title = "Join " + group.Name
		page.Review = []webFlowLine{
			{Term: "Group", Desc: group.Name},
			{Term: "Contribution", Desc: domain.FormatNGN(group.ContributionAmountKobo) + " " + titleCase(group.Frequency)},
			{Term: "Members", Desc: strconv.Itoa(group.MemberCount) + " of " + strconv.Itoa(group.TargetMemberCount)},
			{Term: "Status", Desc: group.Status},
		}
		page.Actions = []webFlowAction{{Name: "join", Label: "Join group"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown thrift_join step %q", flow.Step)
}

func (a *App) wfThriftJoinSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		name := strings.TrimSpace(r.FormValue("group_name"))
		if name == "" {
			return a.wfPageWithError(flow, page, "Send the group name."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_name"] = name
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "join" {
			return a.wfPageWithError(flow, page, "Tap Join group to confirm."), nil
		}
		group, member, err := a.store.JoinThriftGroup(r.Context(), flow.Payload["thrift_name"], user.ID)
		if err != nil {
			return a.wfPageWithError(flow, page, "Xego could not join that thrift group: "+err.Error()), nil
		}
		msg := fmt.Sprintf("You're in.\n\nThrift: %s\nMember: %s\nMembers: %d of %d\n\nWhen the creator activates the group, Xego will show your contribution prompt.",
			group.Name, member.UserName, group.MemberCount, group.TargetMemberCount)
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "You joined, but the confirmation could not be sent."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "You're in " + group.Name
		done.DoneBody = "Members: " + strconv.Itoa(group.MemberCount) + " of " + strconv.Itoa(group.TargetMemberCount) + ". Wait for the creator to activate the group."
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// onboard — first-time profile setup
// =========================================================================

func (a *App) wfOnboardStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		page.Title = "Welcome to " + a.cfg.AppName
		page.Intro = "What name should we use on your receipts?"
		page.Fields = []webFlowField{{Name: "name", Label: "Your name", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "email":
		page.Title = "Your email"
		page.Intro = "We use your email for checkout and receipts."
		page.Fields = []webFlowField{{Name: "email", Label: "Email address", Type: "email", Required: true, Value: user.Email}}
		page.Actions = []webFlowAction{{Name: "send_code", Label: "Send code"}}
		return page, nil
	case "code":
		page.Title = "Confirm your email"
		page.Intro = "Enter the 6-digit code we emailed to " + flow.Payload["email"] + "."
		page.Fields = []webFlowField{{Name: "code", Label: "6-digit code", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "resend", Label: "Resend code"}, {Name: "next", Label: "Verify"}}
		return page, nil
	case "confirm":
		page.Title = "Almost done"
		page.Review = []webFlowLine{
			{Term: "Name", Desc: flow.Payload["name"]},
			{Term: "Email", Desc: flow.Payload["email"]},
			{Term: "Account", Desc: user.WhatsAppNumber},
		}
		page.Intro = "Xego uses your WhatsApp number as your account identity. Confirm to finish setup."
		page.Actions = []webFlowAction{{Name: "confirm", Label: "Confirm my account"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown onboard step %q", flow.Step)
}

func (a *App) wfOnboardSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "name":
		name := strings.TrimSpace(r.FormValue("name"))
		if len(name) < 2 || len(name) > 80 {
			return a.wfPageWithError(flow, page, "Send the name you would like on receipts (2-80 characters)."), nil
		}
		if err := a.store.UpdateUserName(r.Context(), user.ID, name); err != nil {
			return a.wfPageWithError(flow, page, "Could not save your name."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["name"] = name
		a.wfAdvance(w, r, flow, "email", payload)
		return nil, nil
	case "email":
		address, ok := validWebEmail(r.FormValue("email"))
		if !ok {
			return a.wfPageWithError(flow, page, "That email doesn't look quite right."), nil
		}
		if err := a.store.UpdateUserEmail(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, "Could not save your email."), nil
		}
		if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["email"] = address
		a.wfAdvance(w, r, flow, "code", payload)
		return nil, nil
	case "code":
		if action == "resend" {
			if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, flow.Payload["email"]); err != nil {
				return a.wfPageWithError(flow, page, err.Error()), nil
			}
			page := a.wfPage(flow)
			page.Title = "Confirm your email"
			page.Intro = "A fresh code is on its way to " + flow.Payload["email"] + ". Enter it below."
			page.Fields = []webFlowField{{Name: "code", Label: "6-digit code", Type: "text", Required: true}}
			page.Actions = []webFlowAction{{Name: "resend", Label: "Resend code"}, {Name: "next", Label: "Verify"}}
			return &page, nil
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if err := a.conversation.VerifyEmailCode(r.Context(), user.ID, flow.Payload["email"], code); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["email_verified"] = "1"
		a.wfAdvance(w, r, flow, "confirm", payload)
		return nil, nil
	case "confirm":
		if action != "confirm" {
			return a.wfPageWithError(flow, page, "Tap Confirm to finish."), nil
		}
		if err := a.store.ConfirmUserNumber(r.Context(), user.ID); err != nil {
			return a.wfPageWithError(flow, page, "Could not confirm your account. Please try again."), nil
		}
		msg := "You're all set. Your account is confirmed for " + a.cfg.AppName + " payments. Send MENU anytime to pay merchants, buy data, run thrift groups and more."
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "Your account is confirmed."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "You're all set"
		done.DoneBody = "Your " + a.cfg.AppName + " profile is complete. Open WhatsApp and send MENU to get started."
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

func validWebEmail(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.Contains(trimmed, "@") || len(trimmed) > 254 || len(trimmed) < 5 {
		return "", false
	}
	return strings.ToLower(trimmed), true
}

// =========================================================================
// individual_upgrade — KYC profile verification
// =========================================================================

func (a *App) wfKYCProfileTier(r *http.Request, user store.User) (string, error) {
	profile, err := a.store.KYCProfileByUser(r.Context(), user.ID)
	if err != nil {
		return kyc.TierL0, nil
	}
	return profile.Tier, nil
}

func (a *App) wfIndividualUpgradeStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	tier, _ := a.wfKYCProfileTier(r, user)
	if kyc.Order(tier) >= kyc.Order(kyc.TierL2) && flow.Step == "" {
		page.Title = "Already verified"
		page.Done = true
		page.DoneTitle = "Already verified"
		if user.AccountLevel == "individual" {
			page.DoneBody = "Your Xego individual profile is already approved at level " + tier + ". Return to WhatsApp and send MENU to continue."
		} else {
			// Tier vouches L2+ but the account label gates P2P/thrift: offer
			// a one-tap repair instead of a dead end.
			page.Done = false
			page.Intro = "Your profile is already approved at level " + tier + ", but your account label was left behind and still blocks sending money. Tap below to fix it."
			page.Actions = []webFlowAction{{Name: "heal_account", Label: "Fix my account"}}
		}
		return page, nil
	}
	switch flow.Step {
	case "", "email":
		if user.Email != "" {
			page.Title = "Verify your email"
			page.Intro = "We'll send a 6-digit code to " + user.Email + " to confirm it for your profile."
			page.Fields = []webFlowField{{Name: "email", Label: "Email address", Type: "email", Required: true, Value: user.Email}}
		} else {
			page.Title = "Verify your email"
			page.Intro = "Send the email address we should verify for your individual profile."
			page.Fields = []webFlowField{{Name: "email", Label: "Email address", Type: "email", Required: true}}
		}
		page.Actions = []webFlowAction{{Name: "send_code", Label: "Send code"}}
		return page, nil
	case "code":
		page.Title = "Enter the code"
		page.Intro = "Enter the 6-digit code emailed to " + flow.Payload["email"] + "."
		page.Fields = []webFlowField{{Name: "code", Label: "6-digit code", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "resend", Label: "Resend code"}, {Name: "next", Label: "Verify"}}
		return page, nil
	case "profile":
		page.Title = "Your details"
		page.Intro = "Xego verifies this identity before approving your individual profile."
		page.Fields = []webFlowField{
			{Name: "legal_name", Label: "Legal name", Type: "text", Required: true},
			{Name: "dob", Label: "Date of birth (YYYY-MM-DD)", Type: "date", Required: true},
			{Name: "address", Label: "Residential address", Type: "textarea", Required: true},
			{Name: "occupation", Label: "Occupation", Type: "text", Required: true},
		}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		page.Title = "Review your profile"
		page.Review = []webFlowLine{
			{Term: "Legal name", Desc: flow.Payload["legal_name"]},
			{Term: "Date of birth", Desc: flow.Payload["dob"]},
			{Term: "Address", Desc: flow.Payload["address"]},
			{Term: "Occupation", Desc: flow.Payload["occupation"]},
			{Term: "Email", Desc: flow.Payload["email"]},
		}
		page.Intro = "Submitting this profile applies a compliance screening. Level 2 approval is immediate when the screen is clean."
		page.Actions = []webFlowAction{{Name: "apply", Label: "Submit profile"}}
		return page, nil
	case "result", "done":
		if flow.Step == "done" {
			page.Done = true
			page.DoneTitle = "Individual profile updated"
			page.DoneBody = "Check WhatsApp for the confirmation and your new limits."
			return page, nil
		}
		page.Title = "Profile submitted"
		page.Review = nil
		if flow.Payload["upgrade_result"] == "review" {
			page.Intro = "Your profile is under review. Our compliance team is checking your screening result and will follow up."
		} else {
			page.Intro = "Your individual profile is approved at Level 2 (identity on file). Optionally add your NIN or BVN to reach Level 3 and raise your limits."
		}
		page.Fields = []webFlowField{
			{Name: "id_type", Label: "ID type", Type: "radio", Required: false, Options: []webFlowOption{
				{Value: "nin", Label: "NIN"},
				{Value: "bvn", Label: "BVN"},
			}},
			{Name: "id_number", Label: "11-digit number", Type: "text"},
		}
		page.Actions = []webFlowAction{{Name: "skip", Label: "Skip for now"}, {Name: "verify_id", Label: "Verify ID"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown individual_upgrade step %q", flow.Step)
}

func (a *App) wfIndividualUpgradeSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "email":
		if action == "heal_account" {
			// One-tap repair for L2+ users whose account label was left
			// behind: re-check the ladder, then align the label. The tier
			// already vouches identity, so this only heals the gate.
			tierNow, _ := a.wfKYCProfileTier(r, user)
			if kyc.Order(tierNow) < kyc.Order(kyc.TierL2) {
				return a.wfPageWithError(flow, page, "Your profile is no longer at Level 2. Complete verification below."), nil
			}
			if err := a.store.HealIndividualAccountLevel(r.Context(), user.ID); err != nil {
				return a.wfPageWithError(flow, page, "Could not fix your account. Please try again or contact support."), nil
			}
			done := a.wfPage(flow)
			done.Done = true
			done.DoneTitle = "Account fixed"
			done.DoneBody = "Your account is now marked as an approved individual. Return to WhatsApp, send MENU, and retry Send money."
			return &done, nil
		}
		address, ok := validWebEmail(r.FormValue("email"))
		if !ok {
			return a.wfPageWithError(flow, page, "That email doesn't look valid."), nil
		}
		if err := a.store.UpdateUserEmail(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, "Could not save your email."), nil
		}
		if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["email"] = address
		a.wfAdvance(w, r, flow, "code", payload)
		return nil, nil
	case "code":
		if action == "resend" {
			if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, flow.Payload["email"]); err != nil {
				return a.wfPageWithError(flow, page, err.Error()), nil
			}
			return a.wfPageWithError(flow, page, "A fresh code is on its way."), nil
		}
		if err := a.conversation.VerifyEmailCode(r.Context(), user.ID, flow.Payload["email"], strings.TrimSpace(r.FormValue("code"))); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		a.wfAdvance(w, r, flow, "profile", flow.Payload)
		return nil, nil
	case "profile":
		legalName := strings.TrimSpace(r.FormValue("legal_name"))
		dob := strings.TrimSpace(r.FormValue("dob"))
		address := strings.TrimSpace(r.FormValue("address"))
		occupation := strings.TrimSpace(r.FormValue("occupation"))
		var errs []string
		if len([]rune(legalName)) < 3 || len([]rune(legalName)) > 100 {
			errs = append(errs, "Send your legal name (3-100 characters).")
		}
		if _, err := time.Parse("2006-01-02", dob); err != nil {
			errs = append(errs, "Enter your date of birth as YYYY-MM-DD.")
		}
		if len([]rune(address)) < 10 || len([]rune(address)) > 240 {
			errs = append(errs, "Send an address between 10 and 240 characters.")
		}
		if len([]rune(occupation)) < 2 || len([]rune(occupation)) > 80 {
			errs = append(errs, "Send an occupation between 2 and 80 characters.")
		}
		if msg := wfFormError(errs); msg != "" {
			return a.wfPageWithError(flow, page, msg), nil
		}
		payload := clonePayload(flow.Payload)
		payload["legal_name"] = legalName
		payload["dob"] = dob
		payload["address"] = address
		payload["occupation"] = occupation
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "apply" {
			return a.wfPageWithError(flow, page, "Tap Submit profile to apply."), nil
		}
		user, err := a.store.UserByID(r.Context(), user.ID)
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not load your account."), nil
		}
		outcome, err := a.conversation.ApplyIndividualProfile(r.Context(), user, flow.Payload["legal_name"], flow.Payload["dob"], flow.Payload["address"], flow.Payload["occupation"])
		if err != nil {
			return a.wfPageWithError(flow, page, "Profile submission failed: "+err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["upgrade_result"] = outcome
		if outcome == "review" {
			if err := a.wfFinish(r.Context(), flow, "Your Xego individual profile is under review. Our compliance team will follow up on the screening result."); err != nil {
				return a.wfPageWithError(flow, page, "Your profile was submitted."), nil
			}
			done := a.wfPage(flow)
			done.Done = true
			done.DoneTitle = "Profile under review"
			done.DoneBody = "Our compliance team is checking your screening result and will follow up on WhatsApp."
			return &done, nil
		}
		a.wfAdvance(w, r, flow, "result", payload)
		return nil, nil
	case "result":
		if action == "skip" {
			msg := "No problem. Your Xego individual profile is Level 2 (identity on file) and approved. You can create thrift groups and send money to individuals."
			if err := a.wfFinish(r.Context(), flow, msg); err != nil {
				return a.wfPageWithError(flow, page, "Profile saved."), nil
			}
			done := a.wfPage(flow)
			done.Done = true
			done.DoneTitle = "Individual profile updated"
			done.DoneBody = "Your profile is Level 2 and approved. Check WhatsApp for your new limits."
			return &done, nil
		}
		if action != "verify_id" {
			return a.wfPageWithError(flow, page, "Verify your ID or skip to stay at Level 2."), nil
		}
		idType := strings.ToLower(strings.TrimSpace(r.FormValue("id_type")))
		idNumber := strings.ReplaceAll(strings.TrimSpace(r.FormValue("id_number")), " ", "")
		user, err := a.store.UserByID(r.Context(), user.ID)
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not load your account."), nil
		}
		message, err := a.conversation.VerifyIndividualIdentity(r.Context(), user, flow.Payload["legal_name"], flow.Payload["dob"], idType, idNumber)
		if err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		if err := a.wfFinish(r.Context(), flow, message); err != nil {
			return a.wfPageWithError(flow, page, "Verification recorded."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "Individual profile updated"
		done.DoneBody = "Check WhatsApp for the confirmation and your new limits."
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// merchant_register — submit a business for approval
// =========================================================================

func (a *App) wfMerchantRegisterStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "email":
		page.Title = "Register your business"
		page.Intro = "First, the email address we should verify and use for merchant updates."
		page.Fields = []webFlowField{{Name: "email", Label: "Email address", Type: "email", Required: true, Value: user.Email}}
		page.Actions = []webFlowAction{{Name: "send_code", Label: "Send code"}}
		return page, nil
	case "code":
		page.Title = "Verify your email"
		page.Intro = "Enter the 6-digit code emailed to " + flow.Payload["email"] + "."
		page.Fields = []webFlowField{{Name: "code", Label: "6-digit code", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "resend", Label: "Resend code"}, {Name: "next", Label: "Verify"}}
		return page, nil
	case "name":
		page.Title = "Business name"
		page.Fields = []webFlowField{{Name: "business_name", Label: "Business or merchant name", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "category":
		page.Title = "Business category"
		page.Intro = "What does the business do? Examples: Food, Retail, Services, Health, Education, Logistics."
		page.Fields = []webFlowField{{Name: "category", Label: "Category", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "description":
		page.Title = "Business description"
		page.Intro = "Briefly describe what the business sells or does. One sentence is enough."
		page.Fields = []webFlowField{{Name: "description", Label: "Description", Type: "textarea", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		page.Title = "Review your registration"
		page.Review = []webFlowLine{
			{Term: "Business", Desc: flow.Payload["business_name"]},
			{Term: "Category", Desc: flow.Payload["category"]},
			{Term: "Description", Desc: flow.Payload["description"]},
			{Term: "Email", Desc: flow.Payload["email"]},
		}
		page.Intro = "Xego reviews every merchant before it can start collecting payments."
		page.Actions = []webFlowAction{{Name: "submit", Label: "Submit for approval"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown merchant_register step %q", flow.Step)
}

func (a *App) wfMerchantRegisterSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "email":
		address, ok := validWebEmail(r.FormValue("email"))
		if !ok {
			return a.wfPageWithError(flow, page, "That email doesn't look valid."), nil
		}
		if err := a.store.UpdateUserEmail(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, "Could not save your email."), nil
		}
		if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, address); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["email"] = address
		a.wfAdvance(w, r, flow, "code", payload)
		return nil, nil
	case "code":
		if action == "resend" {
			if err := a.conversation.SendEmailVerificationCode(r.Context(), user.ID, flow.Payload["email"]); err != nil {
				return a.wfPageWithError(flow, page, err.Error()), nil
			}
			return a.wfPageWithError(flow, page, "A fresh code is on its way."), nil
		}
		if err := a.conversation.VerifyEmailCode(r.Context(), user.ID, flow.Payload["email"], strings.TrimSpace(r.FormValue("code"))); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["email_verified"] = "1"
		a.wfAdvance(w, r, flow, "name", payload)
		return nil, nil
	case "name":
		name := strings.TrimSpace(r.FormValue("business_name"))
		if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
			return a.wfPageWithError(flow, page, "Send the merchant or business name (2-80 characters)."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["business_name"] = name
		a.wfAdvance(w, r, flow, "category", payload)
		return nil, nil
	case "category":
		category := strings.TrimSpace(r.FormValue("category"))
		if len([]rune(category)) < 2 || len([]rune(category)) > 50 {
			return a.wfPageWithError(flow, page, "Send a short business category, e.g. Food, Retail, Services."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["category"] = category
		a.wfAdvance(w, r, flow, "description", payload)
		return nil, nil
	case "description":
		description := strings.TrimSpace(r.FormValue("description"))
		if len([]rune(description)) < 10 || len([]rune(description)) > 240 {
			return a.wfPageWithError(flow, page, "Send a description between 10 and 240 characters."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["description"] = description
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "submit" {
			return a.wfPageWithError(flow, page, "Tap Submit for approval to finish."), nil
		}
		request, err := a.store.CreateMerchantRegistration(r.Context(), user.ID, flow.Payload["business_name"], flow.Payload["category"], flow.Payload["description"], flow.Payload["email"])
		if err != nil {
			return a.wfPageWithError(flow, page, "Registration failed: "+err.Error()), nil
		}
		msg := fmt.Sprintf("Merchant registration submitted.\n\nBusiness: %s\nCategory: %s\nReference: %s\nStatus: Awaiting approval\n\nXego will use your verified email for follow-up.",
			request.BusinessName, request.Category, request.Reference)
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "Registration submitted."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "Registration submitted"
		done.DoneBody = request.BusinessName + " · Reference " + request.Reference + " · Awaiting approval"
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// kyb_request — self-service business tier upgrade
// =========================================================================

func (a *App) wfKYBRequestStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		merchants, err := a.store.ApprovedMerchantsForUser(r.Context(), user.ID)
		if err != nil {
			return page, err
		}
		if len(merchants) == 0 {
			page.Title = "No approved merchant"
			page.Intro = "You need an approved merchant before requesting a business tier upgrade. Register a business from the Xego menu."
			page.Done = true
			return page, nil
		}
		page.Title = "Request a business upgrade"
		page.Intro = "Which merchant do you want to request an upgrade for?"
		page.Fields = []webFlowField{{Name: "merchant_slug", Label: "Merchant", Type: "select", Required: true, Options: merchantSelectOptions(merchants)}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "note":
		page.Title = "Upgrade note (optional)"
		page.Intro = "Anything the compliance team should know? You can leave this empty."
		page.Fields = []webFlowField{{Name: "note", Label: "Note", Type: "textarea"}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		merchant, err := a.store.MerchantBySlug(r.Context(), flow.Payload["merchant_slug"])
		if err != nil {
			return page, err
		}
		page.Title = "Review upgrade request"
		page.Review = []webFlowLine{
			{Term: "Merchant", Desc: merchant.Name},
			{Term: "Note", Desc: flow.Payload["note"]},
		}
		page.Intro = "The compliance team reviews upgrade requests before they take effect."
		page.Actions = []webFlowAction{{Name: "submit", Label: "Request upgrade"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown kyb_request step %q", flow.Step)
}

func (a *App) wfKYBRequestSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		slug := strings.TrimSpace(r.FormValue("merchant_slug"))
		if slug == "" {
			return a.wfPageWithError(flow, page, "Choose a merchant."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["merchant_slug"] = slug
		a.wfAdvance(w, r, flow, "note", payload)
		return nil, nil
	case "note":
		payload := clonePayload(flow.Payload)
		payload["note"] = strings.TrimSpace(r.FormValue("note"))
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "submit" {
			return a.wfPageWithError(flow, page, "Tap Request upgrade to submit."), nil
		}
		merchant, err := a.store.MerchantBySlug(r.Context(), flow.Payload["merchant_slug"])
		if err != nil {
			return a.wfPageWithError(flow, page, "That merchant is no longer available."), nil
		}
		if _, err := a.store.RequestKYBAdvancement(r.Context(), merchant.ID, nil, flow.Payload["note"]); err != nil {
			return a.wfPageWithError(flow, page, "The upgrade request failed: "+err.Error()), nil
		}
		msg := "Your upgrade request for " + merchant.Name + " has been submitted. The compliance team will review it and update you."
		if err := a.wfFinish(r.Context(), flow, msg); err != nil {
			return a.wfPageWithError(flow, page, "Upgrade request submitted."), nil
		}
		done := a.wfPage(flow)
		done.Done = true
		done.DoneTitle = "Upgrade requested"
		done.DoneBody = merchant.Name + " upgrade request submitted for review."
		return &done, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}
