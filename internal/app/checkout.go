package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// paymentReturn is the customer's return landing page after completing a
// card checkout. Interswitch Web Checkout returns here with the transaction
// reference either in the query (GET) or in the form body (POST); the outcome
// is always confirmed with an authoritative server-side requery.
func (a *App) paymentReturn(w http.ResponseWriter, r *http.Request) {
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		_ = r.ParseForm()
		if ref := strings.TrimSpace(r.FormValue("txnref")); ref != "" {
			reference = ref
		} else {
			reference = strings.TrimSpace(r.FormValue("txn_ref"))
		}
	}
	if reference == "" {
		http.Error(w, "missing reference", http.StatusBadRequest)
		return
	}
	payment, _, err := a.payments.VerifyAndApply(r.Context(), reference, "interswitch.callback")
	if err != nil {
		a.logger.WarnContext(r.Context(), "callback verification failed", "reference", reference, "error", err)
		http.Error(w, "Payment is still being verified. Return to WhatsApp or refresh your receipt shortly.", http.StatusAccepted)
		return
	}
	// A payment started from a browser web flow gets its WhatsApp confirmation
	// (message 2) here; the guarded flow claim prevents double sends.
	a.maybeCompleteWebFlowForPayment(r.Context(), payment)
	http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
}

// hostedCheckout renders the branded confirmation page for a payment capability.
func (a *App) hostedCheckout(w http.ResponseWriter, r *http.Request) {
	payment, ok := a.paymentByCheckoutToken(w, r)
	if !ok {
		return
	}
	if payment.Status == domain.StatusSucceeded {
		http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
		return
	}
	a.renderHostedCheckout(w, r, payment, http.StatusOK)
}

// hostedCheckoutPay initializes the secure gateway only after the customer
// confirms on the hosted page.
func (a *App) hostedCheckoutPay(w http.ResponseWriter, r *http.Request) {
	payment, ok := a.paymentByCheckoutToken(w, r)
	if !ok {
		return
	}
	switch payment.Status {
	case domain.StatusSucceeded:
		http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
		return
	case domain.StatusInitialized, domain.StatusPending:
		if payment.CheckoutURL != "" {
			http.Redirect(w, r, payment.CheckoutURL, http.StatusSeeOther)
			return
		}
	}
	if payment.Provider != service.ProviderInterswitch && payment.Provider != service.ProviderBankTransfer {
		a.renderHostedCheckout(w, r, payment, http.StatusOK)
		return
	}
	updated, err := a.payments.InitializeCheckout(r.Context(), payment)
	if err != nil {
		a.logger.WarnContext(r.Context(), "hosted checkout initialize failed", "payment_id", payment.ID, "error", err)
		a.renderHostedCheckout(w, r, payment, http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, updated.CheckoutURL, http.StatusSeeOther)
}

// interswitchCheckout renders the page that forwards the browser to the
// Interswitch Web Checkout hosted payment page. Web Checkout is initiated with
// a client-side form POST, so this page auto-submits the redirect form carrying
// the merchant, amount, and transaction reference fields.
func (a *App) interswitchCheckout(w http.ResponseWriter, r *http.Request) {
	reference := strings.TrimSpace(chi.URLParam(r, "reference"))
	payment, err := a.store.PaymentByReference(r.Context(), reference)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if payment.Provider != service.ProviderInterswitch && payment.Provider != service.ProviderBankTransfer {
		a.renderHostedCheckout(w, r, payment, http.StatusOK)
		return
	}
	page := a.interswitch.NewPayPage(
		payment.ProviderReference,
		payment.UserEmail,
		payment.AmountKobo,
		a.cfg.BaseURL+"/payments/return",
	)
	// The global CSP only allows form posts to 'self', which would block the
	// browser from posting this form to the Interswitch gateway. Scope this
	// page's policy to also allow the configured gateway origin.
	if gatewayURL, err := url.Parse(page.Action); err == nil && gatewayURL.Scheme != "" && gatewayURL.Host != "" {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data: blob:; form-action 'self' "+gatewayURL.Scheme+"://"+gatewayURL.Host)
	}
	a.renderStatus(w, "interswitch_checkout.html", map[string]any{
		"AppName": a.cfg.AppName, "Payment": payment, "Page": page, "BaseURL": a.cfg.BaseURL,
		"WhatsAppDeepLink": a.whatsappDeepLink(),
	}, http.StatusOK)
}

func (a *App) paymentByCheckoutToken(w http.ResponseWriter, r *http.Request) (store.PaymentView, bool) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return store.PaymentView{}, false
	}
	payment, err := a.store.PaymentByCheckoutToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return store.PaymentView{}, false
	}
	return payment, true
}

func (a *App) renderHostedCheckout(w http.ResponseWriter, r *http.Request, payment store.PaymentView, status int) {
	var invoice *store.InvoiceView
	if view, err := a.store.InvoiceByPaymentID(r.Context(), payment.ID); err == nil {
		invoice = &view
	}
	var dataOrder *store.DataOrderView
	if order, err := a.store.DataOrderByPaymentID(r.Context(), payment.ID); err == nil {
		dataOrder = &order
	}
	var thrift *store.ThriftContributionView
	if view, err := a.store.ThriftContributionByPaymentID(r.Context(), payment.ID); err == nil {
		thrift = &view
	}
	a.renderStatus(w, "checkout.html", map[string]any{
		"AppName": a.cfg.AppName, "Payment": payment, "Invoice": invoice,
		"DataOrder": dataOrder, "Thrift": thrift, "BaseURL": a.cfg.BaseURL,
		"WhatsAppDeepLink": a.whatsappDeepLink(),
	}, status)
}

// checkoutLink renders a general request-money link and resolves the payer
// before any payment is created.
func (a *App) checkoutLink(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return
	}
	checkout, err := a.store.CheckoutByToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if checkout.Status == "paid" {
		if checkout.PaymentID.Valid {
			if payment, err := a.store.PaymentByID(r.Context(), checkout.PaymentID.UUID); err == nil {
				http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
				return
			}
		}
	}
	// An open link that was already resolved continues the active payment
	// attempt instead of asking for the payer phone again.
	if checkout.Status == "open" && checkout.PaymentID.Valid {
		if payment, err := a.store.PaymentByID(r.Context(), checkout.PaymentID.UUID); err == nil {
			switch payment.Status {
			case domain.StatusFailed, domain.StatusAbandoned, domain.StatusExpired, domain.StatusSucceeded:
			default:
				http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
				return
			}
		}
	}
	a.render(w, "link.html", map[string]any{
		"AppName": a.cfg.AppName, "Checkout": checkout, "BaseURL": a.cfg.BaseURL,
		"CollectionFee": service.XegoCollectionFee(a.cfg, "card", checkout.AmountKobo).FeeKobo,
	})
}

// checkoutLinkResolve collects the payer's WhatsApp number on the hosted link
// page, creates the payment against the payee, and hands off to the branded
// checkout page to confirm and pay.
func (a *App) checkoutLinkResolve(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return
	}
	checkout, err := a.store.CheckoutByToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if checkout.Status == "paid" {
		if checkout.PaymentID.Valid {
			if payment, err := a.store.PaymentByID(r.Context(), checkout.PaymentID.UUID); err == nil {
				http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
				return
			}
		}
	}
	if checkout.Status != "open" {
		a.render(w, "link.html", map[string]any{
			"AppName": a.cfg.AppName, "Checkout": checkout, "BaseURL": a.cfg.BaseURL,
			"Error": "This payment request is no longer open.",
		})
		return
	}
	phone := strings.TrimSpace(r.FormValue("phone"))
	payment, err := a.payments.ResolveCheckout(r.Context(), checkout, phone)
	if err != nil {
		a.logger.WarnContext(r.Context(), "checkout resolve failed", "checkout_id", checkout.ID, "error", err)
		a.render(w, "link.html", map[string]any{
			"AppName": a.cfg.AppName, "Checkout": checkout, "BaseURL": a.cfg.BaseURL,
			"Error": "Enter the WhatsApp number where you want to receive the receipt.",
		})
		return
	}
	http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
}

func (a *App) receipt(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return
	}
	payment, err := a.store.PaymentByReceiptToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var dataOrder *store.DataOrderView
	if order, err := a.store.DataOrderByPaymentID(r.Context(), payment.ID); err == nil {
		dataOrder = &order
	}
	var invoice *store.InvoiceView
	if view, err := a.store.InvoiceByPaymentID(r.Context(), payment.ID); err == nil {
		invoice = &view
	}
	var thrift *store.ThriftContributionView
	if view, err := a.store.ThriftContributionByPaymentID(r.Context(), payment.ID); err == nil {
		thrift = &view
	}
	var scanToken *store.ReceiptScanTokenView
	if view, err := a.store.ReceiptScanTokenByPaymentID(r.Context(), payment.ID); err == nil {
		scanToken = &view
	}
	a.render(w, "receipt.html", map[string]any{
		"AppName": a.cfg.AppName, "Payment": payment, "DataOrder": dataOrder,
		"Invoice": invoice, "Thrift": thrift, "ScanToken": scanToken, "BaseURL": a.cfg.BaseURL,
		"WhatsAppDeepLink": a.whatsappDeepLink(),
	})
}

func (a *App) receiptScanQR(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	payment, err := a.store.PaymentByReceiptToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	scanToken, err := a.store.ReceiptScanTokenByPaymentID(r.Context(), payment.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The QR payload contains only the opaque scan URL. Readers call the
	// authenticated API to get safe receipt details and consume the token.
	png, err := qrcode.Encode(a.cfg.BaseURL+"/scan/"+scanToken.Token, qrcode.Medium, 240)
	if err != nil {
		http.Error(w, "qr unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *App) scanLanding(w http.ResponseWriter, r *http.Request) {
	token := store.ExtractScanToken(chi.URLParam(r, "token"))
	if token == "" {
		http.NotFound(w, r)
		return
	}
	a.render(w, "scan.html", map[string]any{"AppName": a.cfg.AppName, "Token": token})
}

func (a *App) readerScan(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimSpace(r.Header.Get("X-Xego-Reader-Key"))
	if apiKey == "" {
		apiKey = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	}
	var body struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else if err := r.ParseForm(); err == nil {
		body.Token = r.FormValue("token")
		body.URL = r.FormValue("url")
	}
	tokenOrURL := body.Token
	if tokenOrURL == "" {
		tokenOrURL = body.URL
	}
	result, err := a.store.ValidateAndConsumeReceiptScan(r.Context(), apiKey, tokenOrURL, clientIP(r))
	if err != nil {
		http.Error(w, "scan unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if result.Status != "valid_consumed" {
		status = http.StatusConflict
		if result.Status == "reader_not_authorized" {
			status = http.StatusUnauthorized
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func (a *App) renderInvoicePage(w http.ResponseWriter, r *http.Request, invoice store.InvoiceView, errMsg, phone, amount, method string) {
	whatsappPayLink := ""
	if a.cfg.WhatsAppPhoneNumber != "" {
		whatsappPayLink = "https://wa.me/" + a.cfg.WhatsAppPhoneNumber + "?text=" + url.QueryEscape("PAY "+invoice.Reference)
	}
	a.render(w, "invoice.html", map[string]any{
		"AppName": a.cfg.AppName, "Invoice": invoice, "BaseURL": a.cfg.BaseURL,
		"WhatsAppPayLink": whatsappPayLink,
		"Error": errMsg, "Phone": phone, "Amount": amount, "Method": method,
	})
}

func (a *App) invoice(w http.ResponseWriter, r *http.Request) {
	reference := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "reference")))
	if !strings.HasPrefix(reference, "XG-INV-") {
		http.NotFound(w, r)
		return
	}
	invoice, err := a.store.InvoiceByReference(r.Context(), reference)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if invoice.TotalKobo-invoice.AmountPaidKobo <= 0 {
		if payment, err := a.store.LatestSuccessPaymentForInvoice(r.Context(), invoice.ID); err == nil {
			http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
			return
		}
	}
	a.renderInvoicePage(w, r, invoice, "", "", "", service.ProviderInterswitch)
}

// invoicePay is the public invoice fill-in page's POST: it collects the payer's
// WhatsApp number, amount, and rail, then creates the linked invoice payment
// and hands off to the branded hosted checkout — the same anonymous pattern as
// a request-money link (checkoutLinkResolve).
func (a *App) invoicePay(w http.ResponseWriter, r *http.Request) {
	reference := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "reference")))
	if !strings.HasPrefix(reference, "XG-INV-") {
		http.NotFound(w, r)
		return
	}
	invoice, err := a.store.InvoiceByReference(r.Context(), reference)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	remaining := invoice.TotalKobo - invoice.AmountPaidKobo
	if remaining <= 0 {
		http.Redirect(w, r, "/invoices/"+reference, http.StatusSeeOther)
		return
	}
	phone := strings.TrimSpace(r.FormValue("phone"))
	method := strings.TrimSpace(r.FormValue("method"))
	amountRaw := strings.TrimSpace(r.FormValue("amount"))
	prefill := func(errMsg, amount string) {
		a.renderInvoicePage(w, r, invoice, errMsg, phone, amount, method)
	}
	merchant, err := a.store.MerchantByID(r.Context(), invoice.MerchantID)
	if err != nil {
		prefill("The merchant for this invoice is no longer available.", amountRaw)
		return
	}
	if method != service.ProviderInterswitch && method != service.ProviderBankTransfer {
		prefill("Choose a payment method.", amountRaw)
		return
	}
	amount := remaining
	if merchant.AllowPartialPayments && amountRaw != "" && !strings.EqualFold(amountRaw, "full") {
		amount, err = domain.ParseNGNAmount(amountRaw, a.cfg.PaymentMinKobo, remaining)
		if err != nil {
			prefill(fmt.Sprintf("Enter an amount between %s and %s, or leave it blank to pay the full balance.", domain.FormatNGN(a.cfg.PaymentMinKobo), domain.FormatNGN(remaining)), amountRaw)
			return
		}
	}
	if msg := a.wfInvoicePayRuleCheck(r, merchant, invoice, amount); msg != "" {
		prefill(msg, amountRaw)
		return
	}
	payment, err := a.payments.ResolveInvoicePayment(r.Context(), invoice, merchant, phone, amount, method)
	if err != nil {
		a.logger.WarnContext(r.Context(), "invoice pay resolve failed", "invoice_id", invoice.ID, "error", err)
		prefill("Enter the WhatsApp number where you want to receive the receipt, then continue.", amountRaw)
		return
	}
	http.Redirect(w, r, "/checkout/"+payment.CheckoutToken, http.StatusSeeOther)
}

func (a *App) thriftGroup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	group, err := a.store.ThriftGroupByName(r.Context(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, _ := a.store.ThriftMembers(r.Context(), group.ID)
	progress, _ := a.store.ThriftCycleProgressForGroup(r.Context(), group.ID)
	a.render(w, "thrift_group.html", map[string]any{
		"AppName":  a.cfg.AppName,
		"Group":    group,
		"Members":  members,
		"Progress": progress,
	})
}
