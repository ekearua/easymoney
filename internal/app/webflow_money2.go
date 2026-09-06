package app

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// =========================================================================
// pay_invoice — a customer paying an invoice (full or partial)
// =========================================================================

func (a *App) wfPayInvoiceStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	invoice, err := a.store.InvoiceByReference(r.Context(), flow.Payload["invoice_reference"])
	if err != nil {
		page.Title = "Invoice not found"
		page.Intro = "This invoice is no longer available. Open Xego on WhatsApp and send PAY followed by the reference to retry."
		page.Done = true
		return page, nil
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	switch flow.Step {
	case "", "amount":
		page.Title = "Pay invoice " + invoice.Reference
		page.Review = []webFlowLine{
			{Term: "Merchant", Desc: invoice.MerchantName},
			{Term: "Total", Desc: domain.FormatNGN(invoice.TotalKobo)},
			{Term: "Paid so far", Desc: domain.FormatNGN(invoice.AmountPaidKobo)},
			{Term: "Remaining", Desc: domain.FormatNGN(remaining)},
		}
		page.Intro = "How much would you like to pay now? Enter the full remaining balance or a partial amount for split payments."
		page.Fields = []webFlowField{{Name: "amount_kobo", Label: "Amount (naira)", Type: "amount", Required: true, Hint: "Send the remaining balance to pay in full"}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "review":
		amount := wfInt(flow.Payload["invoice_pay_amount_kobo"])
		page.Title = "Review invoice payment"
		page.Review = []webFlowLine{
			{Term: "Invoice", Desc: invoice.Reference},
			{Term: "Merchant", Desc: invoice.MerchantName},
			{Term: "Paying now", Desc: domain.FormatNGN(amount)},
			{Term: "Remaining after", Desc: domain.FormatNGN(remaining - amount)},
		}
		page.Fields = []webFlowField{{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(false)}}
		page.Actions = []webFlowAction{{Name: "pay", Label: "Pay " + domain.FormatNGN(amount)}, {Name: "back", Label: "Back"}}
		return page, nil
	case "checkout", "done":
		page.Title = "Checkout started"
		page.Intro = "Complete the payment on the secure page. Xego updates the invoice only after the payment is verified."
		page.Done = true
		return page, nil
	}
	return page, fmt.Errorf("unknown pay_invoice step %q", flow.Step)
}

func (a *App) wfPayInvoiceSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	invoice, err := a.store.InvoiceByReference(r.Context(), flow.Payload["invoice_reference"])
	if err != nil {
		return a.wfPageWithError(flow, page, "This invoice is no longer available."), nil
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	if remaining <= 0 {
		return a.wfPageWithError(flow, page, "This invoice is already fully paid."), nil
	}
	switch flow.Step {
	case "", "amount":
		merchant, err := a.store.MerchantBySlug(r.Context(), invoice.MerchantSlug)
		if err != nil {
			return a.wfPageWithError(flow, page, "The merchant for this invoice is no longer available."), nil
		}
		raw := strings.TrimSpace(r.FormValue("amount_kobo"))
		var amount int64
		if strings.EqualFold(raw, "full") {
			amount = remaining
		} else {
			amount, err = domain.ParseNGNAmount(raw, a.cfg.PaymentMinKobo, remaining)
			if err != nil {
				return a.wfPageWithError(flow, page, "Enter an amount between "+domain.FormatNGN(a.cfg.PaymentMinKobo)+" and "+domain.FormatNGN(remaining)+", or FULL to pay the balance."), nil
			}
		}
		if msg := a.wfInvoicePayRuleCheck(r, merchant, invoice, amount); msg != "" {
			return a.wfPageWithError(flow, page, msg), nil
		}
		payload := clonePayload(flow.Payload)
		payload["invoice_pay_amount_kobo"] = strconv.FormatInt(amount, 10)
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action == "back" {
			a.wfAdvance(w, r, flow, "amount", flow.Payload)
			return nil, nil
		}
		method := r.FormValue("method")
		if !wfProviderValid(method, false) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		amount := wfInt(flow.Payload["invoice_pay_amount_kobo"])
		merchant, err := a.store.MerchantBySlug(r.Context(), invoice.MerchantSlug)
		if err != nil {
			return a.wfPageWithError(flow, page, "The merchant for this invoice is no longer available."), nil
		}
		payment, err := a.payments.CreateCollectionDraft(r.Context(), user, merchant, amount, method, flow.Channel, user.WhatsAppNumber)
		if err != nil {
			return a.wfAllowancePageError(flow, page, err), nil
		}
		if err := a.store.CreateInvoicePayment(r.Context(), invoice.ID, payment.ID, user.ID, amount); err != nil {
			return a.wfPageWithError(flow, page, "Could not attach this payment to the invoice."), nil
		}
		if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
			return a.wfPageWithError(flow, page, "Payment could not be completed. Please go back and try again."), nil
		}
		return nil, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

func (a *App) wfInvoicePayRuleCheck(r *http.Request, merchant store.Merchant, invoice store.InvoiceView, amount int64) string {
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	full := amount >= remaining
	if !merchant.AllowPartialPayments && !full {
		return "This merchant does not accept partial payments. Pay the remaining balance in full."
	}
	if full || !merchant.AllowPartialPayments {
		return ""
	}
	if merchant.MinInvoiceAmountKobo > 0 && invoice.TotalKobo < merchant.MinInvoiceAmountKobo {
		return fmt.Sprintf("This invoice is below the minimum of %s for installment payments. Pay in full.", domain.FormatNGN(merchant.MinInvoiceAmountKobo))
	}
	if merchant.UpfrontPercent > 0 && invoice.AmountPaidKobo == 0 {
		upfrontMin := invoice.TotalKobo * int64(merchant.UpfrontPercent) / 100
		if amount < upfrontMin {
			return fmt.Sprintf("The upfront payment must be at least %d%% of the invoice total (%s).", merchant.UpfrontPercent, domain.FormatNGN(upfrontMin))
		}
	}
	if merchant.MinInstallmentPercent > 0 && invoice.AmountPaidKobo > 0 {
		installMin := invoice.TotalKobo * int64(merchant.MinInstallmentPercent) / 100
		if amount < installMin {
			return fmt.Sprintf("Each installment must be at least %d%% of the invoice total (%s).", merchant.MinInstallmentPercent, domain.FormatNGN(installMin))
		}
	}
	if merchant.MaxInstallments > 0 {
		count, _ := a.store.CountInvoicePayments(r.Context(), invoice.ID)
		if count >= int64(merchant.MaxInstallments) {
			return fmt.Sprintf("This invoice has reached the maximum of %d installments. Pay the remaining balance in full.", merchant.MaxInstallments)
		}
	}
	return ""
}

// =========================================================================
// thrift_contribute — pay this cycle's contribution
// =========================================================================

func (a *App) wfThriftContribution(r *http.Request, flow store.WebFlow) (store.ThriftContributionView, error) {
	name := strings.TrimSpace(flow.Payload["thrift_name"])
	if name == "" {
		name = strings.TrimSpace(flow.Payload["group_name"])
	}
	if name == "" {
		return store.ThriftContributionView{}, fmt.Errorf("no thrift group selected")
	}
	contribution, err := a.store.CurrentThriftContributionForUser(r.Context(), name, flow.UserID)
	if err != nil {
		return store.ThriftContributionView{}, err
	}
	if contribution.Status == "paid" {
		return contribution, fmt.Errorf("your contribution for %s cycle %d is already paid", contribution.GroupName, contribution.CycleNumber)
	}
	return contribution, nil
}

func (a *App) wfThriftContributeStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	if flow.Step == "" || flow.Step == "group" {
		opts, err := a.wfActiveContributionOptions(r, user)
		if err != nil {
			return page, err
		}
		if len(opts) == 0 {
			page.Title = "No active contributions"
			page.Intro = "You have no unpaid thrift contributions right now. When a group activates and your turn is due, Xego will let you know."
			page.Done = true
			return page, nil
		}
		page.Title = "Pay a thrift contribution"
		page.Intro = "Which contribution would you like to pay?"
		page.Fields = []webFlowField{{Name: "thrift_name", Label: "Contribution", Type: "select", Required: true, Options: opts}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	}
	contribution, err := a.wfThriftContribution(r, flow)
	if err != nil {
		page.Title = "No active contribution"
		page.Intro = err.Error() + ". Reopen from WhatsApp to try again."
		page.Done = true
		return page, nil
	}
	page.Title = "Pay thrift contribution"
	page.Review = []webFlowLine{
		{Term: "Group", Desc: contribution.GroupName},
		{Term: "Cycle", Desc: strconv.FormatInt(int64(contribution.CycleNumber), 10)},
		{Term: "Amount", Desc: domain.FormatNGN(contribution.AmountKobo)},
	}
	page.Fields = []webFlowField{{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(false)}}
	page.Actions = []webFlowAction{{Name: "pay", Label: "Pay " + domain.FormatNGN(contribution.AmountKobo)}}
	return page, nil
}

func (a *App) wfThriftContributeSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	if flow.Step == "" || flow.Step == "group" {
		name := strings.TrimSpace(r.FormValue("thrift_name"))
		if name == "" {
			return a.wfPageWithError(flow, page, "Choose a contribution."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["thrift_name"] = name
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	}
	contribution, err := a.wfThriftContribution(r, flow)
	if err != nil {
		return a.wfPageWithError(flow, page, err.Error()), nil
	}
	if action != "pay" {
		return a.wfPageWithError(flow, page, "Choose a payment method and tap Pay."), nil
	}
	method := r.FormValue("method")
	if !wfProviderValid(method, false) {
		return a.wfPageWithError(flow, page, "Choose a payment method."), nil
	}
	merchant, err := a.store.ThriftSystemMerchant(r.Context())
	if err != nil {
		return a.wfPageWithError(flow, page, "Thrift payments are unavailable right now."), nil
	}
	payment, err := a.payments.CreateDraftForProvider(r.Context(), user, merchant, contribution.AmountKobo, method, flow.Channel, user.WhatsAppNumber)
	if err != nil {
		return a.wfAllowancePageError(flow, page, err), nil
	}
	if err := a.store.LinkThriftContributionPayment(r.Context(), contribution.ID, payment.ID); err != nil {
		return a.wfPageWithError(flow, page, "Could not link your contribution."), nil
	}
	if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
		return a.wfPageWithError(flow, page, "Payment could not be completed."), nil
	}
	return nil, nil
}

// =========================================================================
// data — buy mobile data
// =========================================================================

func (a *App) wfDataStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "network":
		networks, err := a.store.ListActiveDataNetworks(r.Context())
		if err != nil {
			return page, err
		}
		opts := make([]webFlowOption, 0, len(networks))
		for _, network := range networks {
			opts = append(opts, webFlowOption{Value: network.Code, Label: network.Name, Description: "Buy " + network.Name + " data"})
		}
		page.Title = "Buy mobile data"
		page.Intro = "Which network is the recipient on?"
		page.Fields = []webFlowField{{Name: "data_network", Label: "Network", Type: "select", Required: true, Options: opts}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "plan":
		plans, _, err := a.store.SearchDataPlans(r.Context(), flow.Payload["data_network"], "", 0, 200)
		if err != nil {
			return page, err
		}
		opts := make([]webFlowOption, 0, len(plans))
		for _, plan := range plans {
			opts = append(opts, webFlowOption{Value: plan.Code, Label: plan.DisplayName})
		}
		page.Title = "Choose a data plan"
		page.Fields = []webFlowField{{Name: "data_plan", Label: "Plan", Type: "select", Required: true, Options: opts}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "phone":
		plan, err := a.store.DataPlanByCode(r.Context(), flow.Payload["data_plan"])
		if err != nil {
			return page, err
		}
		page.Title = plan.DisplayName
		page.Intro = "Who should receive the data?"
		page.Fields = []webFlowField{{Name: "data_phone", Label: "Beneficiary phone", Type: "tel", Required: true, Hint: "Nigerian number, e.g. 08031234567"}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		plan, err := a.store.DataPlanByCode(r.Context(), flow.Payload["data_plan"])
		if err != nil {
			return page, err
		}
		page.Title = "Review data order"
		page.Review = []webFlowLine{
			{Term: "Data", Desc: plan.DisplayName},
			{Term: "Phone", Desc: flow.Payload["data_phone"]},
		}
		page.Fields = []webFlowField{{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(false)}}
		page.Actions = []webFlowAction{{Name: "pay", Label: "Pay and activate"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown data step %q", flow.Step)
}

func (a *App) wfDataSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "network":
		code := strings.TrimSpace(r.FormValue("data_network"))
		if code == "" {
			return a.wfPageWithError(flow, page, "Choose a network."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["data_network"] = code
		a.wfAdvance(w, r, flow, "plan", payload)
		return nil, nil
	case "plan":
		code := strings.TrimSpace(r.FormValue("data_plan"))
		if code == "" {
			return a.wfPageWithError(flow, page, "Choose a plan."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["data_plan"] = code
		a.wfAdvance(w, r, flow, "phone", payload)
		return nil, nil
	case "phone":
		phone, err := domain.NormalizeNigerianPhone(strings.TrimSpace(r.FormValue("data_phone")))
		if err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload := clonePayload(flow.Payload)
		payload["data_phone"] = phone
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "pay" {
			return a.wfPageWithError(flow, page, "Choose a payment method and tap Pay."), nil
		}
		method := r.FormValue("method")
		if !wfProviderValid(method, false) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		order, err := a.data.CreateOrder(r.Context(), user, flow.Channel, user.WhatsAppNumber, flow.Payload["data_plan"], flow.Payload["data_phone"])
		if err != nil {
			return a.wfPageWithError(flow, page, "The data order could not be created: "+err.Error()), nil
		}
		payment, _, err := a.data.CreatePaymentForOrder(r.Context(), user, order, method, flow.Channel, user.WhatsAppNumber)
		if err != nil {
			return a.wfAllowancePageError(flow, page, err), nil
		}
		if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
			return a.wfPageWithError(flow, page, "Payment could not be completed."), nil
		}
		return nil, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// topup — fund the Xego wallet
// =========================================================================

func (a *App) wfTopupStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "amount":
		page.Title = "Top up your wallet"
		page.Intro = fmt.Sprintf("How much would you like to add? Between %s and %s.", domain.FormatNGN(a.cfg.PaymentMinKobo), domain.FormatNGN(a.cfg.PaymentMaxKobo))
		page.Fields = []webFlowField{{Name: "amount_kobo", Label: "Amount (naira)", Type: "amount", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "review":
		page.Title = "Review wallet top-up"
		page.Review = []webFlowLine{{Term: "Amount to add", Desc: domain.FormatNGN(wfInt(flow.Payload["amount_kobo"]))}}
		page.Fields = []webFlowField{{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(false)}}
		page.Actions = []webFlowAction{{Name: "pay", Label: "Continue to payment"}}
		return page, nil
	}
	return page, fmt.Errorf("unknown topup step %q", flow.Step)
}

func (a *App) wfTopupSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "amount":
		amount, err := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("amount_kobo")), a.cfg.PaymentMinKobo, a.cfg.PaymentMaxKobo)
		if err != nil {
			return a.wfPageWithError(flow, page, "Enter a valid amount between "+domain.FormatNGN(a.cfg.PaymentMinKobo)+" and "+domain.FormatNGN(a.cfg.PaymentMaxKobo)+"."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["amount_kobo"] = strconv.FormatInt(amount, 10)
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "pay" {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		method := r.FormValue("method")
		if !wfProviderValid(method, false) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		amount := wfInt(flow.Payload["amount_kobo"])
		payment, err := a.payments.CreateWalletTopupDraft(r.Context(), user, flow.Channel, user.WhatsAppNumber, amount, method)
		if err != nil {
			return a.wfAllowancePageError(flow, page, err), nil
		}
		if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
			return a.wfPageWithError(flow, page, "Payment could not be completed."), nil
		}
		return nil, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// =========================================================================
// individual_pay — send money to a recipient's bank account
// =========================================================================

func (a *App) wfIndividualPayStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "phone":
		page.Title = "Send money to an individual"
		page.Intro = "Enter the recipient's phone number."
		page.Fields = []webFlowField{{Name: "recipient_phone", Label: "Recipient phone", Type: "tel", Required: true, Hint: "e.g. 08012345678"}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "amount":
		page.Title = "Amount to send"
		page.Intro = fmt.Sprintf("Between %s and %s. The recipient receives this less the NIP fee.", domain.FormatNGN(a.cfg.PaymentMinKobo), domain.FormatNGN(a.cfg.PaymentMaxKobo))
		page.Fields = []webFlowField{{Name: "amount_kobo", Label: "Amount (naira)", Type: "amount", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "bank":
		page.Title = "Recipient's bank"
		page.Intro = "Type the recipient's bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044)."
		page.Fields = []webFlowField{{Name: "bank_code", Label: "Bank name or code", Type: "text", Required: true, Hint: "e.g. GTBank or 058"}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "bank_pick":
		query := flow.Payload["bank_query"]
		opts, err := a.wfBankOptions(r, query)
		if err != nil {
			return page, err
		}
		page.Title = "Recipient's bank"
		if len(opts) == 0 {
			page.Intro = "No banks matched that search. Go back and type the bank name again."
			page.Actions = []webFlowAction{{Name: "back", Label: "Back"}}
			return page, nil
		}
		page.Intro = "That name matched several banks. Choose the one you mean."
		page.Fields = []webFlowField{{Name: "bank_pick", Label: "Bank", Type: "select", Required: true, Options: opts}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "account":
		page.Title = "Recipient's account"
		page.Intro = "Enter the recipient's 10-digit bank account number."
		page.Fields = []webFlowField{{Name: "account_number", Label: "Account number", Type: "text", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		amount := wfInt(flow.Payload["amount_kobo"])
		collectionFee := service.XegoCollectionFee(a.cfg, "transfer", amount)
		nipFee := service.XegoPayoutFee(a.cfg, amount)
		page.Title = "Review your transfer"
		page.Review = []webFlowLine{
			{Term: "Recipient", Desc: flow.Payload["recipient_phone"]},
			{Term: "Bank", Desc: wfBankLabel(flow.Payload) + " · " + flow.Payload["account_number"]},
			{Term: "Amount", Desc: domain.FormatNGN(amount)},
			{Term: "Collection fee", Desc: domain.FormatNGN(collectionFee.FeeKobo)},
			{Term: "Total you pay", Desc: domain.FormatNGN(amount + collectionFee.FeeKobo)},
			{Term: "Recipient receives", Desc: domain.FormatNGN(amount-nipFee) + " (after NIP fee)"},
		}
		page.Fields = []webFlowField{{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(false)}}
		page.Actions = []webFlowAction{{Name: "pay", Label: "Send " + domain.FormatNGN(amount+collectionFee.FeeKobo)}}
		return page, nil
	case "checkout", "done":
		page.Title = "Checkout started"
		page.Intro = "Complete the payment on the secure page. Xego disburses to the recipient only after the payment is verified."
		page.Done = true
		return page, nil
	}
	return page, fmt.Errorf("unknown individual_pay step %q", flow.Step)
}

func (a *App) wfIndividualPaySubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "phone":
		raw := strings.TrimSpace(r.FormValue("recipient_phone"))
		phone := domain.CanonicalE164Phone(raw)
		if len(strings.TrimPrefix(phone, "+")) < 10 {
			return a.wfPageWithError(flow, page, "That doesn't look like a valid phone number. Try again (e.g. 08012345678)."), nil
		}
		if phone == user.WhatsAppNumber {
			return a.wfPageWithError(flow, page, "You cannot send money to yourself."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["recipient_phone"] = phone
		a.wfAdvance(w, r, flow, "amount", payload)
		return nil, nil
	case "amount":
		amount, err := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("amount_kobo")), a.cfg.PaymentMinKobo, a.cfg.PaymentMaxKobo)
		if err != nil {
			return a.wfPageWithError(flow, page, "Enter a valid amount in naira."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["amount_kobo"] = strconv.FormatInt(amount, 10)
		a.wfAdvance(w, r, flow, "bank", payload)
		return nil, nil
	case "bank":
		raw := strings.TrimSpace(r.FormValue("bank_code"))
		resolution, err := service.ResolveBank(r.Context(), a.store, raw)
		if errors.Is(err, service.ErrBankAmbiguous) {
			payload := clonePayload(flow.Payload)
			payload["bank_query"] = raw
			a.wfAdvance(w, r, flow, "bank_pick", payload)
			return nil, nil
		}
		if errors.Is(err, service.ErrBankNotFound) {
			return a.wfPageWithError(flow, page, "We couldn't find that bank. Type the bank name (e.g. Access, GTBank, Zenith) or code (e.g. 044)."), nil
		}
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not resolve the bank. Try again."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["bank_code"] = resolution.Code
		payload["bank_name"] = resolution.Name
		delete(payload, "bank_query")
		a.wfAdvance(w, r, flow, "account", payload)
		return nil, nil
	case "bank_pick":
		code := strings.TrimSpace(r.FormValue("bank_pick"))
		bank, err := a.store.BankByCode(r.Context(), code)
		if errors.Is(err, store.ErrBankNotFound) {
			return a.wfPageWithError(flow, page, "That bank is no longer available. Go back and choose again."), nil
		}
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not resolve the bank. Try again."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["bank_code"] = bank.Code
		payload["bank_name"] = bank.Name
		delete(payload, "bank_query")
		a.wfAdvance(w, r, flow, "account", payload)
		return nil, nil
	case "account":
		account := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(r.FormValue("account_number")))
		if len(account) != 10 || !digitsOnly(account) {
			return a.wfPageWithError(flow, page, "Account number must be exactly 10 digits."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["account_number"] = account
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action != "pay" {
			return a.wfPageWithError(flow, page, "Choose a payment method and confirm."), nil
		}
		method := r.FormValue("method")
		if !wfProviderValid(method, false) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		amount := wfInt(flow.Payload["amount_kobo"])
		collectionFee := service.XegoCollectionFee(a.cfg, "transfer", amount)
		nipFee := service.XegoPayoutFee(a.cfg, amount)
		totalPay := amount + collectionFee.FeeKobo
		recipientGets := amount - nipFee
		recipientPhone := flow.Payload["recipient_phone"]
		bankCode := flow.Payload["bank_code"]
		bankName := flow.Payload["bank_name"]
		accountNumber := flow.Payload["account_number"]
		// Resolve the recipient user + destination so the post-success hook
		// can settle without any session state, exactly like the chat flow.
		recipientUser, err := a.store.GetOrCreateUser(r.Context(), recipientPhone)
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not resolve the recipient. Please go back and try again."), nil
		}
		if _, err := a.store.GetOrCreateUserPayoutDestination(r.Context(), recipientUser.ID, bankCode, bankName, accountNumber, ""); err != nil {
			return a.wfPageWithError(flow, page, "Could not save the recipient's bank details."), nil
		}
		payment, err := a.payments.CreateIndividualPayDraft(r.Context(), user, flow.Channel, user.WhatsAppNumber, totalPay, map[string]any{
			"individual_pay": map[string]any{
				"recipient_phone": recipientPhone, "bank_code": bankCode, "bank_name": bankName, "account_number": accountNumber,
				"amount_kobo": amount, "collection_fee_kobo": collectionFee.FeeKobo,
				"nip_fee_kobo": nipFee, "recipient_gets": recipientGets,
			},
		})
		if err != nil {
			return a.wfAllowancePageError(flow, page, err), nil
		}
		if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
			return a.wfPageWithError(flow, page, "Payment could not be completed."), nil
		}
		return nil, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

// wfBankOptions renders the bank directory as web-flow select options from a
// search query, for the ambiguous-name picker step.
func (a *App) wfBankOptions(r *http.Request, query string) ([]webFlowOption, error) {
	banks, err := a.store.SearchBanks(r.Context(), query, 15)
	if err != nil {
		return nil, err
	}
	opts := make([]webFlowOption, 0, len(banks))
	for _, bank := range banks {
		code := bank.Code
		opts = append(opts, webFlowOption{Value: code, Label: bank.Name + " (" + code + ")"})
	}
	return opts, nil
}

// wfBankLabel renders the bank part of a review line, preferring the resolved
// name with its code in parentheses.
func wfBankLabel(payload map[string]string) string {
	if name := payload["bank_name"]; name != "" {
		if code := payload["bank_code"]; code != "" {
			return name + " (" + code + ")"
		}
		return name
	}
	return payload["bank_code"]
}

// wfActiveContributionOptions lists the user's thrift groups that have an
// unpaid current contribution, as select options.
func (a *App) wfActiveContributionOptions(r *http.Request, user store.User) ([]webFlowOption, error) {
	groups, err := a.store.RecentThriftGroupsForUser(r.Context(), user.ID, 10)
	if err != nil {
		return nil, err
	}
	var opts []webFlowOption
	for _, group := range groups {
		if group.Status != "active" {
			continue
		}
		contribution, err := a.store.CurrentThriftContributionForUser(r.Context(), group.Name, user.ID)
		if err != nil || contribution.Status == "paid" {
			continue
		}
		opts = append(opts, webFlowOption{
			Value:       group.Name,
			Label:       group.Name,
			Description: "Cycle " + strconv.FormatInt(int64(contribution.CycleNumber), 10) + " · " + domain.FormatNGN(contribution.AmountKobo),
		})
	}
	return opts, nil
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
