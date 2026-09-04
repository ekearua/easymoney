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
	Type     string // text | email | tel | number | date | amount | textarea | select | radio | hidden
	Value    string
	Hint     string
	Required bool
	Options  []webFlowOption
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
	if err := r.ParseForm(); err != nil {
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

// maybeCompleteWebFlowForPayment completes the open web flow that created the
// payment (identified by payload payment_id) with a generic confirmation.
// It runs after gateway verification so the customer gets message 2 without
// polling; the guarded claim prevents double sends with the webhook.
func (a *App) maybeCompleteWebFlowForPayment(ctx context.Context, payment store.PaymentView) {
	flow, err := a.store.OpenWebFlowByPayment(ctx, payment.ID)
	if err != nil || flow.Status != store.WebFlowOpen {
		return
	}
	link := a.cfg.BaseURL + "/receipts/" + payment.ReceiptToken
	message := fmt.Sprintf("Payment received.\n\nMerchant: %s\nAmount: %s\n\nReceipt: %s\n\nOpen WhatsApp anytime and send MENU for more options.",
		payment.MerchantName, domain.FormatNGN(payment.AmountKobo), link)
	if err := a.wfFinish(ctx, flow, message); err != nil {
		a.logger.WarnContext(ctx, "web flow completion notify failed", "flow_id", flow.ID, "error", err)
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
