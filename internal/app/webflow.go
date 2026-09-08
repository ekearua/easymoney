package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// webFlowAction is one button on a web-flow page. Kind "submit" posts the
// form back with the action value; kind "link" opens a URL (used on done
// pages for the receipt and return-to-chat).
type webFlowAction struct {
	Kind  string // "submit" (default) | "link"
	Name  string
	Label string
	URL   string
}

type webFlowOption struct {
	Value       string
	Label       string
	Description string
}

type webFlowField struct {
	Name     string
	Label    string
	Type     string // text | email | tel | number | date | amount | textarea | select | radio | hidden | upload | voice
	Value    string
	Hint     string
	Required bool
	Options  []webFlowOption
	// MediaPrompt is the OCR instruction sent with an upload field's image to
	// ports.ImageReader. Voice fields always transcribe speech to text.
	MediaPrompt string
}

type webFlowLine struct {
	Term string
	Desc string
}

// webFlowPage is the data for the shared webflow.html template.
type webFlowPage struct {
	AppName      string
	FlowType     string
	Token        string
	Title        string
	Intro        string
	Error        string
	Fields       []webFlowField
	Review       []webFlowLine
	Actions      []webFlowAction
	WhatsAppLink string
	BaseURL      string
	// Done renders the completion state instead of a form.
	Done       bool
	DoneTitle  string
	DoneBody   string
	DoneAction webFlowAction
	Expired    bool
}

func (a *App) wfPage(flow store.WebFlow) webFlowPage {
	return webFlowPage{
		AppName: a.cfg.AppName, FlowType: flow.FlowType, Token: flow.Token,
		BaseURL: a.cfg.BaseURL, WhatsAppLink: a.whatsappDeepLink(),
	}
}

func (a *App) whatsappDeepLink() string {
	if a.cfg.WhatsAppPhoneNumber == "" {
		return ""
	}
	return "https://wa.me/" + strings.TrimPrefix(a.cfg.WhatsAppPhoneNumber, "+")
}

// webFlowPage renders the current step of a web flow (GET).
func (a *App) webFlowPage(w http.ResponseWriter, r *http.Request) {
	flow, user, ok := a.webFlowContext(w, r)
	if !ok {
		return
	}
	if flow.Status == store.WebFlowExpired {
		page := a.wfPage(flow)
		page.Expired = true
		page.Title = "This link has expired"
		page.Intro = "Reopen the flow from your Xego chat to get a fresh link. Your details are safe and nothing was charged."
		a.renderStatus(w, "webflow.html", page, http.StatusGone)
		return
	}
	if flow.Status == store.WebFlowComplete {
		a.wfRenderDone(w, r, flow, a.wfPage(flow))
		return
	}
	page, err := a.wfRenderStep(r, flow, user)
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow render failed", "flow", flow.ID, "step", flow.Step, "error", err)
		http.Error(w, "Something went wrong loading this page. Go back to WhatsApp and tap the link again.", http.StatusInternalServerError)
		return
	}
	a.wfPrefillMediaValues(&page, flow)
	page.Token = flow.Token
	page.AppName = a.cfg.AppName
	page.WhatsAppLink = a.whatsappDeepLink()
	page.BaseURL = a.cfg.BaseURL
	a.renderStatus(w, "webflow.html", page, http.StatusOK)
}

// webFlowSubmit advances a web flow (POST). Step handlers either save
// progress and redirect back to GET (post/redirect/get), redirect onward to
// the hosted checkout, or complete the flow and render the done page.
func (a *App) webFlowSubmit(w http.ResponseWriter, r *http.Request) {
	flow, user, ok := a.webFlowContext(w, r)
	if !ok {
		return
	}
	if flow.Status != store.WebFlowOpen {
		http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
		return
	}
	// urlencoded posts take ParseForm; multipart posts (upload/voice fields
	// render as their own forms with enctype="multipart/form-data") take
	// ParseMultipartForm. On Go 1.21+ ParseMultipartForm returns
	// ErrNotMultipart for urlencoded bodies, so the branch matters.
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
	} else if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	if action == "cancel" {
		a.wfCancel(w, r, flow)
		return
	}
	page, err := a.wfHandleStep(w, r, flow, user, action)
	if err != nil {
		a.logger.WarnContext(r.Context(), "web flow step failed", "flow", flow.ID, "step", flow.Step, "error", err)
		http.Error(w, "Something went wrong saving that step. Go back and try again.", http.StatusInternalServerError)
		return
	}
	if page == nil {
		return // handler already redirected
	}
	a.wfPrefillMediaValues(page, flow)
	page.Token = flow.Token
	page.AppName = a.cfg.AppName
	page.WhatsAppLink = a.whatsappDeepLink()
	page.BaseURL = a.cfg.BaseURL
	status := http.StatusOK
	if page.Error != "" {
		status = http.StatusUnprocessableEntity
	}
	a.renderStatus(w, "webflow.html", page, status)
}

// webFlowContext resolves the token into a flow and its owner.
func (a *App) webFlowContext(w http.ResponseWriter, r *http.Request) (store.WebFlow, store.User, bool) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return store.WebFlow{}, store.User{}, false
	}
	flow, err := a.store.WebFlowByToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return store.WebFlow{}, store.User{}, false
	}
	user, err := a.store.UserByID(r.Context(), flow.UserID)
	if err != nil {
		http.NotFound(w, r)
		return store.WebFlow{}, store.User{}, false
	}
	return flow, user, true
}

// wfPrefillMediaValues surfaces previously extracted upload/voice text as the
// field's value so a re-rendered step confirms what was read before asking the
// customer to continue. Any flow that adds an upload or voice field gets this
// automatically.
func (a *App) wfPrefillMediaValues(page *webFlowPage, flow store.WebFlow) {
	for i := range page.Fields {
		f := &page.Fields[i]
		if (f.Type == "upload" || f.Type == "voice") && f.Value == "" {
			f.Value = flow.Payload[f.Name]
		}
	}
}

// wfAdvance persists the new step/payload and redirects to the GET page
// (PRG), so a refresh never re-submits.
func (a *App) wfAdvance(w http.ResponseWriter, r *http.Request, flow store.WebFlow, nextStep string, payload map[string]string) {
	if err := a.store.SaveWebFlowProgress(r.Context(), flow.Token, nextStep, payload); err != nil {
		a.logger.WarnContext(r.Context(), "web flow save failed", "flow", flow.ID, "error", err)
		http.Error(w, "Could not save your progress. Go back and try again.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
}

// wfCancel abandons the flow without side effects: the flow is marked
// complete so the link stops working, and the session resets to the menu.
func (a *App) wfCancel(w http.ResponseWriter, r *http.Request, flow store.WebFlow) {
	if _, claimed, err := a.store.CompleteWebFlow(r.Context(), flow.Token); err == nil && claimed {
		// Wake the chat back to the menu without sending a message.
		if session, err := a.store.LoadSession(r.Context(), flow.UserID); err == nil && session.State == "web_flow_active" {
			session.State = "menu"
			session.Data = map[string]string{}
			_ = a.store.SaveSession(r.Context(), session)
		}
	}
	http.Redirect(w, r, "/w/"+flow.Token, http.StatusSeeOther)
}

// wfFinish claims the flow and sends the WhatsApp confirmation (message 2).
// Only the claimer notifies, so this is safe to call from retried handlers
// and gateway callbacks alike. Done-page rendering follows in the caller.
func (a *App) wfFinish(ctx context.Context, flow store.WebFlow, message string) error {
	return a.conversation.FinishWebFlow(ctx, flow, message)
}

// wfRenderDone renders a completed flow's terminal page.
func (a *App) wfRenderDone(w http.ResponseWriter, r *http.Request, flow store.WebFlow, page webFlowPage) {
	page.Done = true
	page.FlowType = flow.FlowType
	page.Token = flow.Token
	page.AppName = a.cfg.AppName
	page.WhatsAppLink = a.whatsappDeepLink()
	if page.DoneTitle == "" {
		page.DoneTitle = "This request is closed"
		page.DoneBody = "Nothing was charged. Send MENU on WhatsApp to start again."
	}
	if page.DoneAction.Label == "" {
		page.DoneAction = webFlowAction{Kind: "link", Label: "Back to WhatsApp", URL: a.whatsappDeepLink()}
	}
	a.renderStatus(w, "webflow.html", page, http.StatusOK)
}

// maybeCompleteWebFlowForPayment settles the open web flow that created the
// payment (identified by its payment_id column) after a gateway verification.
// It runs after VerifyAndApply so the customer gets message 2 without polling.
//
// Only a genuinely succeeded payment claims the flow and sends the
// confirmation; a cancelled, failed, abandoned, or still-pending attempt must
// never be reported as "Payment received". A superseded attempt — one that was
// abandoned at the gateway, the flow reopened for retry, and that then
// unexpectedly verifies succeeded — is auto-refunded instead, because the
// retry already moved the customer's money. Any other non-succeeded attempt
// reopens the flow at its review step (clearing the payment link) so the
// customer can pick a different payment method and retry — the caller can
// send them straight back into the flow. The guarded claim and the guarded
// reopen both make the webhook racing the return page harmless.
func (a *App) maybeCompleteWebFlowForPayment(ctx context.Context, payment store.PaymentView) string {
	if payment.Status == domain.StatusSucceeded {
		superseded, err := a.store.WebFlowAttemptSuperseded(ctx, payment.ID)
		if err != nil {
			a.logger.WarnContext(ctx, "web flow superseded check failed", "payment_id", payment.ID, "error", err)
			return ""
		}
		if superseded {
			a.autoRefundSupersededWebFlowAttempt(ctx, payment)
			return ""
		}
		flow, err := a.store.OpenWebFlowByPayment(ctx, payment.ID)
		if err != nil || flow.Status != store.WebFlowOpen {
			return ""
		}
		link := a.cfg.BaseURL + "/receipts/" + payment.ReceiptToken
		message := fmt.Sprintf("Payment received.\n\nMerchant: %s\nAmount: %s\n\nReceipt: %s\n\nOpen WhatsApp anytime and send MENU for more options.",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), link)
		if err := a.wfFinish(ctx, flow, message); err != nil {
			a.logger.WarnContext(ctx, "web flow completion notify failed", "flow_id", flow.ID, "error", err)
		}
		return ""
	}
	return a.wfReopenForPaymentRetry(ctx, payment)
}

// wfReopenForPaymentRetry moves an open money flow that is parked on its
// gateway "checkout" step back to the "review" step (where the payment-method
// options render) when the gateway attempt did not succeed — the customer
// cancelled on the hosted Interswitch page, the card was declined, or the
// transaction was abandoned. Clearing the payment link means the abandoned
// attempt can no longer claim the flow, so retrying creates a fresh payment
// draft. Returns the reopened flow token, or "" when there was nothing to
// reopen (no open flow, a non-money flow, or a flow that already moved on).
//
// Design note — pending-then-success after reopen. Reopening clears the payment
// link *without* ever completing or aborting the abandoned payment and marks it
// superseded, so if the gateway later verifies the abandoned payment as
// succeeded (rare, but possible with slow providers or a customer who cancels
// then immediately succeeds on a different device), the success path in
// maybeCompleteWebFlowForPayment sees the superseded mark and auto-refunds it —
// the retry already moved the customer's money, and this late success must not
// charge twice. The refund is atomic and replay-safe, and exactly one message
// reports it; the abandoned attempt can never claim the reopened flow (its
// payment_id is cleared, and the success path only claims a flow still open on
// that exact payment id), so there is no double-notify and no double charge.
func (a *App) wfReopenForPaymentRetry(ctx context.Context, payment store.PaymentView) string {
	flow, err := a.store.OpenWebFlowByPayment(ctx, payment.ID)
	if err != nil || flow.Status != store.WebFlowOpen {
		return ""
	}
	switch flow.FlowType {
	case service.WebFlowPay, service.WebFlowPayInvoice, service.WebFlowThriftContribute,
		service.WebFlowData, service.WebFlowTopup, service.WebFlowIndividualPay:
	default:
		// Only money flows park on a gateway checkout step with a payment id.
		return ""
	}
	if flow.Step != "checkout" && flow.Step != "done" {
		return ""
	}
	payload := clonePayload(flow.Payload)
	delete(payload, "payment_id")
	delete(payload, "provider")
	if _, err := a.store.ReopenWebFlowForRetry(ctx, flow.Token, payment.ID, "review", payload); err != nil {
		a.logger.WarnContext(ctx, "web flow reopen for retry failed", "flow_id", flow.ID, "error", err)
		return ""
	}
	return flow.Token
}

// autoRefundSupersededWebFlowAttempt returns the customer's money for a
// web-flow attempt that was abandoned (the flow reopened for retry) and only
// later verified as succeeded — the retry already moved money, so this late
// success must never charge the customer twice. Store.RefundPayment is atomic
// and replay-safe (an already-refunded payment is skipped), so a webhook
// racing the return page can only produce one refund, and the customer gets
// exactly one message.
func (a *App) autoRefundSupersededWebFlowAttempt(ctx context.Context, payment store.PaymentView) {
	if _, err := a.store.RefundPayment(ctx, payment.ID, "auto-refund: superseded web-flow attempt", "auto", nil); err != nil {
		// A duplicate webhook/return-page hit after the first refund lands on a
		// payment that is already refunded — the refund is replay-safe, so this
		// is the expected quiet no-op, not a failure worth logging as error.
		if errors.Is(err, store.ErrPaymentNotSucceeded) {
			a.logger.InfoContext(ctx, "auto-refund superseded web-flow attempt already handled", "payment_id", payment.ID)
			return
		}
		a.logger.WarnContext(ctx, "auto-refund superseded web-flow attempt failed", "payment_id", payment.ID, "error", err)
		return
	}
	message := fmt.Sprintf("Payment received, then automatically refunded.\n\nMerchant: %s\nAmount: %s\n\nThe payment you retried was already completed, so this one was reversed automatically. No action needed.",
		payment.MerchantName, domain.FormatNGN(payment.AmountKobo))
	if err := a.conversation.NotifyWebFlowAutoRefund(ctx, payment.UserID, payment.Channel, message); err != nil {
		a.logger.WarnContext(ctx, "auto-refund notification failed", "payment_id", payment.ID, "error", err)
	}
}

// --- flow registry -------------------------------------------------------

// webFlowUser loads the user record without creating it.
func (a *App) webFlowUser(ctx context.Context, id uuid.UUID) (store.User, error) {
	return a.store.UserByID(ctx, id)
}

func wfMethodOptions(includeWallet bool) []webFlowOption {
	opts := []webFlowOption{
		{Value: service.ProviderInterswitch, Label: "Card checkout", Description: "Pay with a debit/credit card on the secure Interswitch page"},
		{Value: service.ProviderBankTransfer, Label: "Bank transfer", Description: "Complete a bank transfer on the Interswitch checkout"},
	}
	if includeWallet {
		opts = append(opts, webFlowOption{Value: service.ProviderWallet, Label: "Pay from wallet", Description: "Instant payment from your Xego wallet balance"})
	}
	return opts
}

// parseAmountKoboField converts a naira amount input to kobo.
func parseAmountKoboField(raw string) (int64, error) {
	amount, err := domain.ParseNGNAmount(strings.TrimSpace(raw), 1, 1<<62)
	if err != nil {
		return 0, err
	}
	return amount, nil
}

func wfInt(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

// wfFieldError joins form errors into one page error message.
func wfFormError(messages []string) string {
	clean := make([]string, 0, len(messages))
	for _, m := range messages {
		if m != "" {
			clean = append(clean, m)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return strings.Join(clean, "\n")
}

var errWFGone = errors.New("web flow item no longer available")

func wfLinkText(baseURL, path string) string {
	return baseURL + path
}
