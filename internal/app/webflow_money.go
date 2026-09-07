package app

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// wfRenderStep builds the current step page for a web flow (GET).
func (a *App) wfRenderStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	switch flow.FlowType {
	case service.WebFlowPay:
		return a.wfPayStep(r, flow, user)
	case service.WebFlowPayInvoice:
		return a.wfPayInvoiceStep(r, flow, user)
	case service.WebFlowThriftContribute:
		return a.wfThriftContributeStep(r, flow, user)
	case service.WebFlowData:
		return a.wfDataStep(r, flow, user)
	case service.WebFlowTopup:
		return a.wfTopupStep(r, flow, user)
	case service.WebFlowIndividualPay:
		return a.wfIndividualPayStep(r, flow, user)
	case service.WebFlowInvoiceCreate:
		return a.wfInvoiceCreateStep(r, flow, user)
	case service.WebFlowThriftCreate:
		return a.wfThriftCreateStep(r, flow, user)
	case service.WebFlowThriftJoin:
		return a.wfThriftJoinStep(r, flow, user)
	case service.WebFlowOnboard:
		return a.wfOnboardStep(r, flow, user)
	case service.WebFlowIndividualUpgrade:
		return a.wfIndividualUpgradeStep(r, flow, user)
	case service.WebFlowMerchantRegister:
		return a.wfMerchantRegisterStep(r, flow, user)
	case service.WebFlowKYBRequest:
		return a.wfKYBRequestStep(r, flow, user)
	}
	return webFlowPage{}, fmt.Errorf("unknown web flow %q", flow.FlowType)
}

// wfHandleStep advances a web flow (POST). A nil page means the handler
// already wrote a redirect.
func (a *App) wfHandleStep(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	switch flow.FlowType {
	case service.WebFlowPay:
		return a.wfPaySubmit(w, r, flow, user, action)
	case service.WebFlowPayInvoice:
		return a.wfPayInvoiceSubmit(w, r, flow, user, action)
	case service.WebFlowThriftContribute:
		return a.wfThriftContributeSubmit(w, r, flow, user, action)
	case service.WebFlowData:
		return a.wfDataSubmit(w, r, flow, user, action)
	case service.WebFlowTopup:
		return a.wfTopupSubmit(w, r, flow, user, action)
	case service.WebFlowIndividualPay:
		return a.wfIndividualPaySubmit(w, r, flow, user, action)
	case service.WebFlowInvoiceCreate:
		return a.wfInvoiceCreateSubmit(w, r, flow, user, action)
	case service.WebFlowThriftCreate:
		return a.wfThriftCreateSubmit(w, r, flow, user, action)
	case service.WebFlowThriftJoin:
		return a.wfThriftJoinSubmit(w, r, flow, user, action)
	case service.WebFlowOnboard:
		return a.wfOnboardSubmit(w, r, flow, user, action)
	case service.WebFlowIndividualUpgrade:
		return a.wfIndividualUpgradeSubmit(w, r, flow, user, action)
	case service.WebFlowMerchantRegister:
		return a.wfMerchantRegisterSubmit(w, r, flow, user, action)
	case service.WebFlowKYBRequest:
		return a.wfKYBRequestSubmit(w, r, flow, user, action)
	}
	return nil, fmt.Errorf("unknown web flow %q", flow.FlowType)
}

// wfPageWithError renders a page with a form error attached.
func (a *App) wfPageWithError(flow store.WebFlow, page webFlowPage, msg string) *webFlowPage {
	page.Error = msg
	page.FlowType = flow.FlowType
	page.Token = flow.Token
	return &page
}

// wfRoutePayment handles the terminal action of a money flow: it saves the
// payment id into the flow payload, then either completes instantly (wallet)
// or redirects to the branded hosted checkout whose callback finishes the
// flow and sends the WhatsApp confirmation.
func (a *App) wfRoutePayment(w http.ResponseWriter, r *http.Request, flow store.WebFlow, payment store.PaymentView, provider string) error {
	payload := clonePayload(flow.Payload)
	payload["payment_id"] = payment.ID.String()
	payload["provider"] = provider
	if provider == service.ProviderWallet {
		// Complete inline: confirm debits the wallet atomically; on success
		// send message 2 and render the done page in the caller.
		updated, changed, err := a.payments.ConfirmWalletPayment(r.Context(), payment)
		if err != nil {
			return err
		}
		_ = changed
		if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, "done", payload); err != nil {
			return err
		}
		link := a.cfg.BaseURL + "/receipts/" + updated.ReceiptToken
		msg := fmt.Sprintf("Paid from your Xego wallet.\n\nMerchant: %s\nAmount: %s\n\nReceipt: %s\n\nSend MENU anytime for more options.",
			updated.MerchantName, domain.FormatNGN(updated.AmountKobo), link)
		return a.wfFinish(r.Context(), flow, msg)
	}
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, "checkout", payload); err != nil {
		return err
	}
	http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
	return nil
}

func clonePayload(payload map[string]string) map[string]string {
	out := make(map[string]string, len(payload)+2)
	for k, v := range payload {
		out[k] = v
	}
	return out
}

// --- shared builders -----------------------------------------------------

func merchantSelectOptions(merchants []store.Merchant) []webFlowOption {
	opts := make([]webFlowOption, 0, len(merchants))
	for _, m := range merchants {
		opts = append(opts, webFlowOption{Value: m.Slug, Label: m.Name, Description: m.Category})
	}
	return opts
}

func (a *App) wfLoadMerchant(r *http.Request, slug string) (store.Merchant, error) {
	return a.store.MerchantBySlug(r.Context(), slug)
}

// wfProviderValid guards the provider from tampered forms.
func wfProviderValid(provider string, wallet bool) bool {
	switch provider {
	case service.ProviderInterswitch, service.ProviderBankTransfer:
		return true
	case service.ProviderWallet:
		return wallet
	}
	return false
}

// =========================================================================
// pay — merchant collection (services, events, custom amount)
// =========================================================================

func (a *App) wfPayStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		merchants, _, err := a.store.SearchMerchants(r.Context(), "", 0, 60)
		if err != nil {
			return page, err
		}
		page.Title = "Who are you paying?"
		page.Intro = "Choose the merchant, then pick a service, event ticket, or enter a custom amount. You can also snap the bill or say the amount below to prefill the form."
		merchantField := webFlowField{Name: "merchant_slug", Label: "Merchant", Type: "select", Required: true, Options: merchantSelectOptions(merchants)}
		// When a bill photo or voice note was already captured on this step
		// (the media upload posts back here), pre-select the merchant named in
		// it so the customer only confirms. The choice stays visible and is
		// required, so nothing is charged without an explicit Continue.
		if flow.Payload["merchant_slug"] == "" {
			if slug := wfBillMerchantSlug(wfBillCapturedText(flow.Payload), merchants); slug != "" {
				merchantField.Value = slug
			}
		}
		page.Fields = append([]webFlowField{merchantField}, wfBillCaptureFields()...)
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}}
		return page, nil
	case "item":
		merchant, err := a.wfLoadMerchant(r, flow.Payload["merchant_slug"])
		if err != nil {
			return page, err
		}
		services, err := a.store.ListActiveMerchantServices(r.Context(), merchant.ID)
		if err != nil {
			return page, err
		}
		events, err := a.store.ListActiveEventsByMerchantID(r.Context(), merchant.ID)
		if err != nil {
			return page, err
		}
		var opts []webFlowOption
		for _, svc := range services {
			opts = append(opts, webFlowOption{Value: "svc:" + svc.ID.String(), Label: svc.Name, Description: domain.FormatNGN(svc.UnitPriceKobo) + " each"})
		}
		for _, evt := range events {
			tiers, err := a.store.ActiveTiersByEventID(r.Context(), evt.ID)
			if err != nil || len(tiers) == 0 {
				continue
			}
			for _, tier := range tiers {
				opts = append(opts, webFlowOption{Value: "evt:" + tier.ID.String(), Label: evt.Name + " — " + tier.Name, Description: domain.FormatNGN(tier.PriceKobo) + " ticket"})
			}
		}
		opts = append(opts, webFlowOption{Value: "custom", Label: "Custom amount", Description: "Pay any amount within your limits"})
		page.Title = merchant.Name
		page.Intro = "What are you paying for?"
		page.Fields = []webFlowField{{Name: "item", Label: "Item", Type: "select", Required: true, Options: opts}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "qty":
		page.Title = "Quantity"
		page.Intro = "How many? (default 1)"
		page.Fields = []webFlowField{{Name: "qty", Label: "Quantity", Type: "number", Value: "1", Required: true}}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "custom_fields":
		page.Title = "Extra details"
		fields, err := a.wfCustomFieldSpecs(r.Context(), flow.Payload)
		if err != nil {
			return page, err
		}
		page.Intro = "This purchase needs a few more details."
		page.Fields = fields
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "amount":
		page.Title = "Amount"
		page.Intro = fmt.Sprintf("How much would you like to pay? Between %s and %s.",
			domain.FormatNGN(a.cfg.PaymentMinKobo), domain.FormatNGN(a.cfg.PaymentMaxKobo))
		amountField := webFlowField{Name: "amount_kobo", Label: "Amount (naira)", Type: "amount", Required: true}
		if flow.Payload["amount_kobo"] == "" {
			// A bill photo or voice note captured earlier supplies the amount;
			// the customer confirms it here before the charge is created.
			if read := wfBillAmountKobo(wfBillCapturedText(flow.Payload)); read >= a.cfg.PaymentMinKobo && read <= a.cfg.PaymentMaxKobo {
				amountField.Value = wfKoboToNairaInput(read)
				amountField.Hint = "Read from your bill or voice note — confirm or edit before continuing."
			}
		}
		page.Fields = []webFlowField{amountField}
		page.Actions = []webFlowAction{{Name: "next", Label: "Continue"}, {Name: "back", Label: "Back"}}
		return page, nil
	case "review":
		return a.wfPayReview(r, flow, page)
	case "checkout", "done":
		page.DoneTitle = "Checkout started"
		page.DoneBody = "Complete the payment on the secure page. Xego will confirm the result on WhatsApp."
		return page, nil
	}
	return page, fmt.Errorf("unknown pay step %q", flow.Step)
}

func (a *App) wfPayReview(r *http.Request, flow store.WebFlow, page webFlowPage) (webFlowPage, error) {
	if preview := a.wfWalletStatePreview(r, flow); preview != "" {
		page.Error = preview
	}
	merchant, err := a.wfLoadMerchant(r, flow.Payload["merchant_slug"])
	if err != nil {
		return page, err
	}
	amount := wfInt(flow.Payload["amount_kobo"])
	item := flow.Payload["item_name"]
	if item == "" {
		item = "Custom payment"
	}
	page.Title = "Review your payment"
	page.Review = []webFlowLine{
		{Term: "Merchant", Desc: merchant.Name},
		{Term: "Item", Desc: item},
		{Term: "Amount", Desc: domain.FormatNGN(amount)},
		{Term: "Method", Desc: "Choose below"},
	}
	// Show where the amount came from when a bill/voice note supplied it.
	if read := wfBillAmountKobo(wfBillCapturedText(flow.Payload)); read > 0 && read == amount {
		page.Review = append(page.Review, webFlowLine{Term: "Read from your bill/voice", Desc: domain.FormatNGN(read)})
	}
	methodField := webFlowField{Name: "method", Label: "Payment method", Type: "radio", Required: true, Options: wfMethodOptions(true)}
	if m := flow.Payload["method"]; wfProviderValid(m, true) {
		methodField.Value = m
	}
	page.Fields = []webFlowField{methodField}
	page.Actions = []webFlowAction{{Name: "pay", Label: "Pay " + domain.FormatNGN(amount)}, {Name: "back", Label: "Back"}}
	return page, nil
}

func (a *App) wfPaySubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		slug := strings.TrimSpace(r.FormValue("merchant_slug"))
		if slug == "" {
			return a.wfPageWithError(flow, page, "Choose a merchant."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["merchant_slug"] = slug
		a.wfAdvance(w, r, flow, "item", payload)
		return nil, nil
	case "item":
		item := r.FormValue("item")
		payload := clonePayload(flow.Payload)
		if item == "custom" {
			payload["item_kind"] = "custom"
			delete(payload, "service_id")
			delete(payload, "tier_id")
			a.wfAdvance(w, r, flow, "amount", payload)
			return nil, nil
		}
		kind, idStr, ok := strings.Cut(item, ":")
		if !ok {
			return a.wfPageWithError(flow, page, "Choose an item."), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return a.wfPageWithError(flow, page, "Choose an item."), nil
		}
		switch kind {
		case "svc":
			svc, err := a.store.MerchantServiceByID(r.Context(), id)
			if err != nil {
				return a.wfPageWithError(flow, page, "That service is no longer available."), nil
			}
			payload["item_kind"] = "service"
			payload["service_id"] = id.String()
			payload["item_name"] = svc.Name
			payload["unit_price_kobo"] = strconv.FormatInt(svc.UnitPriceKobo, 10)
		case "evt":
			tier, err := a.store.TierByID(r.Context(), id)
			if err != nil {
				return a.wfPageWithError(flow, page, "That ticket tier is no longer available."), nil
			}
			payload["item_kind"] = "event"
			payload["tier_id"] = id.String()
			payload["item_name"] = tier.Name
			payload["unit_price_kobo"] = strconv.FormatInt(tier.PriceKobo, 10)
		default:
			return a.wfPageWithError(flow, page, "Choose an item."), nil
		}
		a.wfAdvance(w, r, flow, "qty", payload)
		return nil, nil
	case "qty":
		qty, err := strconv.Atoi(strings.TrimSpace(r.FormValue("qty")))
		if err != nil || qty < 1 {
			qty = 1
		}
		payload := clonePayload(flow.Payload)
		unit := wfInt(payload["unit_price_kobo"])
		total := int64(qty) * unit
		if total < a.cfg.PaymentMinKobo || total > a.cfg.PaymentMaxKobo {
			return a.wfPageWithError(flow, page, fmt.Sprintf("The total (%s) is outside the allowed range. Enter a smaller quantity.", domain.FormatNGN(total))), nil
		}
		if payload["item_kind"] == "service" {
			if svc, err := a.store.MerchantServiceByID(r.Context(), uuid.MustParse(payload["service_id"])); err == nil && svc.QuantityAvailable >= 0 && qty > svc.QuantityAvailable {
				return a.wfPageWithError(flow, page, fmt.Sprintf("Sorry, only %d available.", svc.QuantityAvailable)), nil
			}
		} else if payload["item_kind"] == "event" {
			if tier, err := a.store.TierByID(r.Context(), uuid.MustParse(payload["tier_id"])); err == nil && tier.Capacity >= 0 && int64(tier.Capacity-tier.Sold) < int64(qty) {
				return a.wfPageWithError(flow, page, fmt.Sprintf("Sorry, only %d tickets left in this tier.", tier.Capacity-tier.Sold)), nil
			}
		}
		payload["qty"] = strconv.Itoa(qty)
		payload["amount_kobo"] = strconv.FormatInt(total, 10)
		next := "review"
		if a.wfItemHasCustomFields(r.Context(), payload) {
			next = "custom_fields"
		}
		a.wfAdvance(w, r, flow, next, payload)
		return nil, nil
	case "custom_fields":
		payload := clonePayload(flow.Payload)
		data, err := a.wfCollectCustomFields(r, payload)
		if err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		payload["custom_data_json"] = data
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "amount":
		amount, err := domain.ParseNGNAmount(strings.TrimSpace(r.FormValue("amount_kobo")), a.cfg.PaymentMinKobo, a.cfg.PaymentMaxKobo)
		if err != nil {
			return a.wfPageWithError(flow, page, "Enter a valid amount between "+domain.FormatNGN(a.cfg.PaymentMinKobo)+" and "+domain.FormatNGN(a.cfg.PaymentMaxKobo)+"."), nil
		}
		payload := clonePayload(flow.Payload)
		payload["amount_kobo"] = strconv.FormatInt(amount, 10)
		a.wfAdvance(w, r, flow, "review", payload)
		return nil, nil
	case "review":
		if action == "back" {
			a.wfAdvance(w, r, flow, "item", flow.Payload)
			return nil, nil
		}
		method := r.FormValue("method")
		if !wfProviderValid(method, true) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		// Persist the chosen method so a reopened review step (after a cancelled/
		// declined hosted checkout) can pre-select it instead of forcing the customer
		// to pick again.
		payPayload := clonePayload(flow.Payload)
		payPayload["method"] = method
		if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, flow.Step, payPayload); err != nil {
			return a.wfPageWithError(flow, page, err.Error()), nil
		}
		flow.Payload = payPayload
		merchant, err := a.wfLoadMerchant(r, flow.Payload["merchant_slug"])
		if err != nil {
			return a.wfPageWithError(flow, page, "That merchant is no longer available."), nil
		}
		amount := wfInt(flow.Payload["amount_kobo"])
		payment, err := a.payments.CreateCollectionDraft(r.Context(), user, merchant, amount, method, flow.Channel, user.WhatsAppNumber)
		if err != nil {
			return a.wfAllowancePageError(flow, page, err), nil
		}
		// Record the service/event purchase link so the payment purpose is
		// applied like the chat flow does.
		if qtyStr := flow.Payload["qty"]; qtyStr != "" {
			qty, _ := strconv.Atoi(qtyStr)
			if qty <= 0 {
				qty = 1
			}
			customJSON := flow.Payload["custom_data_json"]
			if flow.Payload["item_kind"] == "service" {
				if svc, err := a.store.MerchantServiceByID(r.Context(), uuid.MustParse(flow.Payload["service_id"])); err == nil {
					if purchaseID, err := a.store.CreateServicePurchase(r.Context(), svc.ID, payment.ID, qty, svc.UnitPriceKobo, int64(qty)*svc.UnitPriceKobo); err == nil && customJSON != "" {
						if m, err := jsonUnmarshalStringMap(customJSON); err == nil && len(m) > 0 {
							_ = a.store.SavePurchaseCustomData(r.Context(), purchaseID, m)
						}
					}
				}
			} else if flow.Payload["item_kind"] == "event" {
				if tier, err := a.store.TierByID(r.Context(), uuid.MustParse(flow.Payload["tier_id"])); err == nil {
					if purchaseID, err := a.store.CreateEventTicketPurchase(r.Context(), tier.ID, payment.ID, qty, tier.PriceKobo, int64(qty)*tier.PriceKobo); err == nil && customJSON != "" {
						if m, err := jsonUnmarshalStringMap(customJSON); err == nil && len(m) > 0 {
							_ = a.store.SaveEventPurchaseCustomData(r.Context(), purchaseID, m)
						}
					}
				}
			}
		}
		if err := a.wfRoutePayment(w, r, flow, payment, method); err != nil {
			return a.wfRoutePaymentFailed(r, flow, user, err)
		}
		if method == service.ProviderWallet {
			done := a.wfPage(flow)
			done.Done = true
			done.DoneTitle = "Payment sent"
			done.DoneBody = "Your wallet payment was completed. Check WhatsApp for your receipt."
			done.DoneAction = webFlowAction{Kind: "link", Label: "Open WhatsApp", URL: a.whatsappDeepLink()}
			return &done, nil
		}
		return nil, nil
	}
	return a.wfPageWithError(flow, page, "This flow has finished. Reopen it from WhatsApp."), nil
}

func (a *App) wfAllowancePageError(flow store.WebFlow, page webFlowPage, err error) *webFlowPage {
	return a.wfPageWithError(flow, page, friendlyWebAllowanceError(err))
}

func friendlyWebAllowanceError(err error) string {
	if msg, ok := service.AllowanceMessage(err); ok && msg != "" {
		return msg
	}
	return "Payment could not be created: " + err.Error()
}

// wfWalletStatePreview returns a short inline note shown above the pay-review
// radio when the customer chose wallet and the wallet is not active or cannot
// cover the amount yet. It only attaches to the pay flow (the one with a
// method-selector radio); other payment flows keep their own review copy.
func (a *App) wfWalletStatePreview(r *http.Request, flow store.WebFlow) string {
	if flow.FlowType != service.WebFlowPay {
		return ""
	}
	method := r.FormValue("method")
	if !strings.EqualFold(method, service.ProviderWallet) {
		return ""
	}
	if flow.UserID.String() == "" {
		return ""
	}
	wallet, err := a.store.WalletByOwner(r.Context(), store.WalletOwnerUser, flow.UserID)
	if err != nil {
		return ""
	}
	amount := wfInt(flow.Payload["amount_kobo"])
	if wallet.Status != store.WalletStatusActive {
		return fmt.Sprintf("Your wallet isn't active yet. Verify your account to reach Level 1 and activate your wallet to pay from it here, or choose another payment method.")
	}
	balance, err := a.store.WalletBalance(r.Context(), wallet.ID)
	if err != nil {
		return ""
	}
	if balance < amount {
		return fmt.Sprintf("Your wallet has %s, which is less than %s. Top it up first, or choose another payment method.",
			domain.FormatNGN(balance), domain.FormatNGN(amount))
	}
	return ""
}

// friendlyWebPaymentError maps a payment routing/confirmation failure (the
// wallet inline confirm, primarily) to customer-facing copy, mirroring the
// copy used on the chat channel so a browser flow explains the problem
// instead of a bare error.
func friendlyWebPaymentError(err error) string {
	switch {
	case errors.Is(err, store.ErrInsufficientWalletBalance):
		return "Your wallet balance is too low for this payment. Top up your wallet and try again, or choose another payment method."
	case errors.Is(err, store.ErrWalletNotActive):
		return "Wallet payments need an active wallet. Verify your account to reach Level 1 and activate your wallet, then try again, or choose another payment method."
	}
	if msg, ok := service.AllowanceMessage(err); ok && msg != "" {
		return msg
	}
	return "Payment could not be completed. Please go back and try again."
}

// wfRoutePaymentFailed renders the flow's current step with a friendly error
// when a payment could not be routed or confirmed (e.g. a wallet payment the
// customer cannot afford yet). Re-rendering the step keeps the payment-method
// options visible so the customer can switch rail and retry, and returning a
// nil error stops webFlowSubmit from falling through to a bare 500.
func (a *App) wfRoutePaymentFailed(r *http.Request, flow store.WebFlow, user store.User, err error) (*webFlowPage, error) {
	// Always surface the underlying reason in the logs: the friendly page copy
	// deliberately hides the raw error, so without this line wallet confirm
	// failures would be impossible to diagnose after the fact.
	a.logger.WarnContext(r.Context(), "web flow payment failed",
		"flow_id", flow.ID, "payee", flow.UserID, "step", flow.Step, "error", err)
	msg := friendlyWebPaymentError(err)
	page, rerr := a.wfRenderStep(r, flow, user)
	if rerr != nil {
		a.logger.WarnContext(r.Context(), "web flow payment failure re-render failed", "flow", flow.ID, "step", flow.Step, "error", rerr)
		return a.wfPageWithError(flow, a.wfPage(flow), msg), nil
	}
	page.Error = msg
	page.FlowType = flow.FlowType
	page.Token = flow.Token
	page.AppName = a.cfg.AppName
	page.WhatsAppLink = a.whatsappDeepLink()
	page.BaseURL = a.cfg.BaseURL
	return &page, nil
}
