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

// wfRoutePayment handles the terminal action of a money flow: it saves
// the payment id into the flow payload, then either completes instantly (wallet)
// or routes directly to the destination page — the secure gateway for card,
// transfer instructions for bank transfer. Initialization mirrors the
// checkout hub's pay handler so behavior is identical minus the hub hop.
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
	// Park the flow on its checkout step, then route straight to the
	// destination page by initializing the gateway exactly as the checkout
	// hub's Pay button does: interswitch card → the hosted payment page,
	// interswitch DVA → /transfer/:ref instructions, simulation → the
	// simulated gateway URL. The /checkout/:token hub is no longer part of
	// the fresh path — it remains the resume/status entry point for saved
	// and in-flight links.
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, "checkout", payload); err != nil {
		return err
	}
	updated, err := a.payments.InitializeCheckout(r.Context(), payment)
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow checkout initialize failed", "payment_id", payment.ID, "error", err)
		// Fall back to the checkout hub, which renders the retry affordance.
		http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
		return nil
	}
	http.Redirect(w, r, updated.CheckoutURL, http.StatusSeeOther)
	return nil
}

// wfSkipStep records a render-time auto-skip and returns the advanced flow.
// The pay flow hides the steps that have nothing to choose (a lone merchant, an
// empty catalog), so the render path advances the flow itself. That advance is
// a real transition and is persisted before the page is drawn: the stored step
// is what the next POST is dispatched against, and a skip kept in memory alone
// left the page on Amount while the row still said "item", so submitting the
// amount was handled as an item submission and dead-ended on an empty page.
// The returned flow carries the new step so the rendered stepper and the page
// being shown cannot disagree.
func (a *App) wfSkipStep(r *http.Request, flow store.WebFlow, next string, payload map[string]string) (store.WebFlow, error) {
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, next, payload); err != nil {
		return flow, err
	}
	flow.Step = next
	flow.Payload = payload
	return flow, nil
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

// --- checkout / change-method helpers ------------------------------------

// wfCheckoutPage renders the consistent "Payment in progress" page shown by
// every money flow that is parked on its gateway "checkout" or "done" step.
// It provides a "Back to payment" link (when the gateway page is reachable)
// and a "Change payment method" submit so the customer can rewind to review.
func (a *App) wfCheckoutPage(r *http.Request, flow store.WebFlow) webFlowPage {
	page := a.wfPage(flow)
	page.Title = "Payment in progress"
	page.Intro = "Complete the payment on the secure page, then return here (or just check WhatsApp) for the confirmation. If anything goes wrong you can change the payment method below."
	var checkoutURL string
	if id := flow.Payload["payment_id"]; id != "" {
		if pid, err := uuid.Parse(id); err == nil {
			if payment, err := a.store.PaymentByID(r.Context(), pid); err == nil {
				switch payment.Status {
				case domain.StatusSucceeded:
					page.Intro = "This payment was completed."
					page.Actions = []webFlowAction{
						{Kind: "link", Label: "View receipt", URL: a.cfg.BaseURL + "/receipts/" + payment.ReceiptToken},
						{Kind: "submit", Name: "change_method", Label: "Change payment method"},
					}
					return page
				case domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusPending:
					checkoutURL = payment.CheckoutURL
				}
			}
		}
	}
	switch {
	case checkoutURL != "":
		page.Actions = []webFlowAction{
			{Kind: "link", Label: "Back to payment", URL: checkoutURL},
			{Name: "change_method", Label: "Change payment method"},
		}
	default:
		page.Actions = []webFlowAction{{Name: "change_method", Label: "Change payment method"}}
	}
	return page
}

// wfChangeMethod rewinds a money flow parked on checkout back to the review
// step where the payment-method options render. If the payment already
// succeeded the customer is sent straight to the receipt instead. The
// abandoned attempt is marked superseded so a late gateway success is
// auto-refunded rather than double-charging.
func (a *App) wfChangeMethod(w http.ResponseWriter, r *http.Request, flow store.WebFlow) {
	payload := clonePayload(flow.Payload)
	rawPID := payload["payment_id"]
	delete(payload, "payment_id")
	// The abandoned attempt's provider becomes the preset choice on the review
	// step so the customer can either retry the same method or pick another.
	if m := payload["provider"]; wfProviderValid(m, true) || wfProviderValid(m, false) {
		payload["method"] = m
	}
	delete(payload, "provider")
	// When a payment exists, guard against rewinding a successful payment
	// and mark the abandoned attempt superseded.
	if rawPID != "" {
		if pid, err := uuid.Parse(rawPID); err == nil {
			if payment, err := a.store.PaymentByID(r.Context(), pid); err == nil && payment.Status == domain.StatusSucceeded {
				http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
				return
			}
			if _, err := a.store.ReopenWebFlowForRetry(r.Context(), flow.Token, pid, "review", payload); err != nil {
				a.logger.WarnContext(r.Context(), "change_method reopen failed", "flow_id", flow.ID, "payment_id", rawPID, "error", err)
			}
		}
	} else {
		// No payment in flight — just advance to review.
		_ = a.store.SaveWebFlowProgress(r.Context(), flow.Token, "review", payload)
	}
	http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
}

// =========================================================================
// pay — merchant collection (services, events, custom amount)
// =========================================================================

func (a *App) wfPayStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		// One call at the page-bounds ceiling: the picker and the ask-bar
		// matcher must see the whole active catalog. (The old page-bounds
		// downgrade truncated both lists to 10 rows silently.)
		merchants, _, err := a.store.SearchMerchants(r.Context(), "", 0, 100)
		if err != nil {
			return page, err
		}
		// Auto-skip: a single active merchant has nothing to choose, so the
		// flow starts at the item step with the merchant pre-selected. The
		// merchant stays editable via the stepper-adjacent summary on later
		// steps, and the legacy "merchant" step keeps working for in-flight
		// flows created before this cut.
		if flow.Step == "" && len(merchants) == 1 {
			payload := clonePayload(flow.Payload)
			payload["merchant_slug"] = merchants[0].Slug
			skipped, err := a.wfSkipStep(r, flow, "item", payload)
			if err != nil {
				return page, err
			}
			return a.wfPayStep(r, skipped, user)
		}
		page.Title = "Who are you paying?"
		page.Intro = "Tell Xego who to pay and how much — type it below, snap the bill with the icons, or say it out loud. Xego reads it and prefills the form; you confirm before anything is charged."
		// The select is not browser-required: the AI bar is the primary input
		// and a typed ask must be free to submit with the select untouched —
		// the submit-side parser resolves it, and an unresolvable ask re-renders
		// with a choose-from-list notice. Server validation covers the empty case.
		merchantField := webFlowField{Name: "merchant_slug", Label: "Merchant", Type: "select", Options: merchantSelectOptions(merchants), OptionsClass: "wf-ai-merchant"}
		// When a bill photo or voice note was already captured on this step
		// (the media upload posts back here), pre-select the merchant named in
		// it so the customer only confirms. The choice stays visible and is
		// required, so nothing is charged without an explicit Continue.
		// When a bill photo, voice note, or typed ask already named a merchant,
		// pre-select it so the customer only confirms. Ambiguous matches are
		// left unselected on purpose: the customer picks from the list rather
		// than Xego guessing between two businesses.
		if flow.Payload["merchant_slug"] == "" {
			if slug, ok := wfBillMerchantSlug(wfBillCapturedText(flow.Payload), merchants); ok && slug != "" {
				merchantField.Value = slug
			}
		}
		// The ask field first: the AI bar renders at the top of the page.
		askField := webFlowField{
			Name: wfBillAskField, Label: "Ask Xego to pay", Type: "aisearch",
			Value: flow.Payload[wfBillAskField],
			Hint:  "e.g. “pay Ade's Kitchen ₦2,500” — Xego reads it and prefills the form, or use the icons to snap the bill or say it out loud.",
		}
		page.Fields = append([]webFlowField{askField, merchantField}, wfBillCaptureFields()...)
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
		// Auto-skip: with only one purchasable option there is nothing to
		// choose — jump straight past item (and, for a fixed-price option,
		// past amount too).
		if flow.Step == "item" && len(opts) == 1 {
			payload := clonePayload(flow.Payload)
			payload["item"] = opts[0].Value
			if opts[0].Value == "custom" {
				payload["item_kind"] = "custom"
				skipped, err := a.wfSkipStep(r, flow, "amount", payload)
				if err != nil {
					return page, err
				}
				return a.wfPayStep(r, skipped, user)
			}
			kind, idStr, _ := strings.Cut(opts[0].Value, ":")
			id, err := uuid.Parse(idStr)
			if err == nil {
				switch kind {
				case "svc":
					if svc, err := a.store.MerchantServiceByID(r.Context(), id); err == nil {
						payload["item_kind"] = "service"
						payload["service_id"] = id.String()
						payload["item_name"] = svc.Name
						payload["unit_price_kobo"] = strconv.FormatInt(svc.UnitPriceKobo, 10)
						payload["qty"] = "1"
						payload["amount_kobo"] = strconv.FormatInt(svc.UnitPriceKobo, 10)
						skipped, serr := a.wfSkipStep(r, flow, "review", payload)
						if serr != nil {
							return page, serr
						}
						return a.wfPayReview(r, skipped, page)
					}
				case "evt":
					if tier, err := a.store.TierByID(r.Context(), id); err == nil {
						payload["item_kind"] = "event"
						payload["tier_id"] = id.String()
						payload["item_name"] = tier.Name
						payload["unit_price_kobo"] = strconv.FormatInt(tier.PriceKobo, 10)
						payload["qty"] = "1"
						payload["amount_kobo"] = strconv.FormatInt(tier.PriceKobo, 10)
						skipped, serr := a.wfSkipStep(r, flow, "review", payload)
						if serr != nil {
							return page, serr
						}
						return a.wfPayReview(r, skipped, page)
					}
				}
			}
		}
		page.Title = merchant.Name
		page.Intro = "What are you paying for?"
		// Quantity folds onto the item step for fixed-price items: one page
		// instead of two. The custom-amount option skips qty entirely.
		itemField := webFlowField{Name: "item", Label: "Item", Type: "select", Required: true, Options: opts}
		if v := flow.Payload["item"]; v != "" {
			itemField.Value = v
		}
		page.Fields = []webFlowField{itemField, {Name: "qty", Label: "Quantity", Type: "number", Value: flow.Payload["qty"], Hint: "For fixed-price items — defaults to 1"}}
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
		return a.wfCheckoutPage(r, flow), nil
	}
	return page, fmt.Errorf("unknown pay step %q", flow.Step)
}

func (a *App) wfPayReview(r *http.Request, flow store.WebFlow, page webFlowPage) (webFlowPage, error) {
	// A wallet attempt that failed leaves the chosen rail in the payload, so a
	// fresh GET of the review — a reload, a browser back, or a checkout
	// reopened for retry — still explains the wallet's state inline instead of
	// silently offering a rail that cannot work yet. A specific failure message
	// set by the caller (the immediate error re-render) wins over it.
	if page.Error == "" {
		page.Error = a.wfWalletStatePreview(r, flow)
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
	}
	// Show where the amount came from when a bill/voice note supplied it.
	if read := wfBillAmountKobo(wfBillCapturedText(flow.Payload)); read > 0 && read == amount {
		page.Review = append(page.Review, webFlowLine{Term: "Read from your bill/voice", Desc: domain.FormatNGN(read)})
	}
	// Review is the final step: each payment option is its own link button,
	// so tapping one routes straight to that rail (card/DVA leave for the
	// Interswitch page, wallet debits inline). The button labels carry the
	// charge including the collection fee, mirroring the chat reviews.
	charge := amount + service.XegoCollectionFee(a.cfg, "card", amount).FeeKobo
	page.Fields = nil
	page.Actions = []webFlowAction{
		{Name: service.ProviderInterswitch, Label: "Pay " + domain.FormatNGN(charge) + " with card"},
		{Name: service.ProviderBankTransfer, Label: "Bank transfer"},
		{Name: service.ProviderWallet, Label: "Pay from wallet"},
		{Name: "back", Label: "Back"},
	}
	return page, nil
}

func (a *App) wfPaySubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page := a.wfPage(flow)
	switch flow.Step {
	case "", "merchant":
		slug := strings.TrimSpace(r.FormValue("merchant_slug"))
		payload := clonePayload(flow.Payload)
		// Server-side parse of the free text typed into the AI ask-bar: when
		// the customer types an instruction ("pay Ade's Kitchen ₦2,500") and
		// the typed text names a seeded merchant, pre-select it and remember
		// the ask so the amount step can prefill from the same sentence. The
		// customer still confirms on the next step, so a mis-parse is editable,
		// never charged.
		ask := strings.TrimSpace(r.FormValue(wfBillAskField))
		if ask != "" && len([]rune(ask)) <= wfMaxExtractLen {
			payload[wfBillAskField] = ask
		}
		// Full active list for the matcher (see the render path's note).
		merchants, _, err := a.store.SearchMerchants(r.Context(), "", 0, 100)
		if err != nil {
			return a.wfPageWithError(flow, page, "Could not load the merchant list. Please try again."), nil
		}
		if slug == "" {
			parsed, ok := wfBillMerchantSlug(wfBillCapturedText(payload), merchants)
			if !ok {
				// Confirmation for ambiguity: several merchants match the text,
				// so the customer — not the parser — picks one. The ask is kept
				// so the amount still prefills once they choose.
				flow.Payload = payload
				page, rerr := a.wfRenderStep(r, flow, user)
				if rerr != nil {
					return a.wfPageWithError(flow, a.wfPage(flow), "Several merchants match your text — choose one from the list."), nil
				}
				page.Error = "Several merchants match your text — choose the right one from the list."
				page.FlowType = flow.FlowType
				page.Token = flow.Token
				page.AppName = a.cfg.AppName
				page.WhatsAppLink = a.whatsappDeepLink()
				page.BaseURL = a.cfg.BaseURL
				return &page, nil
			}
			slug = parsed
			if slug != "" {
				// Resolved from the ask: keep the customer on this step with the
				// merchant pre-selected rather than advancing silently — the same
				// confirm-before-proceed contract the OCR/voice prefill uses.
				// The next Continue (with the select now filled) advances.
				flow.Payload = payload
				if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, flow.Step, payload); err != nil {
					return a.wfPageWithError(flow, page, err.Error()), nil
				}
				page, rerr := a.wfRenderStep(r, flow, user)
				if rerr != nil {
					return a.wfPageWithError(flow, a.wfPage(flow), ""), nil
				}
				page.FlowType = flow.FlowType
				page.Token = flow.Token
				page.AppName = a.cfg.AppName
				page.WhatsAppLink = a.whatsappDeepLink()
				page.BaseURL = a.cfg.BaseURL
				return &page, nil
			}
			// Unresolvable ask: re-render with everything kept so the typed
			// text and any prefill survive for correction.
			flow.Payload = payload
			page, rerr := a.wfRenderStep(r, flow, user)
			if rerr != nil {
				return a.wfPageWithError(flow, a.wfPage(flow), "We couldn't tell which merchant you mean. Choose one from the list."), nil
			}
			page.Error = "We couldn't tell which merchant you mean — choose one from the list (your text is kept)."
			page.FlowType = flow.FlowType
			page.Token = flow.Token
			page.AppName = a.cfg.AppName
			page.WhatsAppLink = a.whatsappDeepLink()
			page.BaseURL = a.cfg.BaseURL
			return &page, nil
		}
		payload["merchant_slug"] = slug
		// Persist the ask text with the advance so the amount step's prefill
		// (wfBillAmountKobo over wfBillCapturedText) sees the typed sentence.
		a.wfAdvance(w, r, flow, "item", payload)
		return nil, nil
	case "item":
		item := r.FormValue("item")
		// Quantity arrives on the same form as the item (folded step). A blank
		// qty means a custom amount was chosen — quantity doesn't apply.
		qty := 1
		if raw := strings.TrimSpace(r.FormValue("qty")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				return a.wfPageWithError(flow, page, "Quantity must be at least 1."), nil
			}
			qty = parsed
		}
		payload := clonePayload(flow.Payload)
		if item == "custom" {
			payload["item_kind"] = "custom"
			delete(payload, "service_id")
			delete(payload, "tier_id")
			delete(payload, "qty")
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
		var unit int64
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
			unit = svc.UnitPriceKobo
		case "evt":
			tier, err := a.store.TierByID(r.Context(), id)
			if err != nil {
				return a.wfPageWithError(flow, page, "That ticket tier is no longer available."), nil
			}
			payload["item_kind"] = "event"
			payload["tier_id"] = id.String()
			payload["item_name"] = tier.Name
			payload["unit_price_kobo"] = strconv.FormatInt(tier.PriceKobo, 10)
			unit = tier.PriceKobo
		default:
			return a.wfPageWithError(flow, page, "Choose an item."), nil
		}
		// Fixed price × qty is computed here, so the amount step disappears
		// for fixed-price items. Capacity and range checks move with it.
		total := unit * int64(qty)
		if total < a.cfg.PaymentMinKobo || total > a.cfg.PaymentMaxKobo {
			return a.wfPageWithError(flow, page, fmt.Sprintf("The total (%s) is outside the allowed range. Adjust the quantity.", domain.FormatNGN(total))), nil
		}
		if payload["item_kind"] == "service" {
			if svc, err := a.store.MerchantServiceByID(r.Context(), id); err == nil && svc.QuantityAvailable >= 0 && qty > svc.QuantityAvailable {
				return a.wfPageWithError(flow, page, fmt.Sprintf("Sorry, only %d available.", svc.QuantityAvailable)), nil
			}
		} else if payload["item_kind"] == "event" {
			if tier, err := a.store.TierByID(r.Context(), id); err == nil && tier.Capacity >= 0 && int64(tier.Capacity-tier.Sold) < int64(qty) {
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
		// Each payment option is its own link button (review = final step), so
		// the tapped button's name IS the method. Card and bank transfer leave
		// for the Interswitch page via wfRoutePayment; wallet debits inline.
		method := action
		if !wfProviderValid(method, true) {
			return a.wfPageWithError(flow, page, "Choose a payment method."), nil
		}
		// Record the chosen method so the wallet-state notice and a reopened
		// review after a cancelled hosted checkout reflect it.
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
	case "checkout", "done":
		if action == "change_method" {
			a.wfChangeMethod(w, r, flow)
			return nil, nil
		}
		// A stale browser-back POST on a parked checkout re-renders the page
		// instead of getting "This flow has finished".
		http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
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

// wfWalletStatePreview returns a short inline notice shown on the pay-review
// page when the recorded method is wallet and the wallet is not active or
// cannot cover the amount yet. With link-button options the chosen method
// lives in the flow payload (a failed wallet attempt persists it before the
// error re-render), so the notice is derived from there, not the request.
func (a *App) wfWalletStatePreview(r *http.Request, flow store.WebFlow) string {
	if flow.FlowType != service.WebFlowPay {
		return ""
	}
	method := flow.Payload["method"]
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
