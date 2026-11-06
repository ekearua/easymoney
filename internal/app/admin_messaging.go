package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// smtpImplicitTLSPort is the submission port that speaks TLS from the first
// byte; every other port must advertise STARTTLS. The email adapter narrows on
// the same value, so the console reports the transport the send will actually
// negotiate.
const smtpImplicitTLSPort = 465

// testEmailTimeout bounds one operator-triggered probe of the relay. The HTTP
// write timeout is 45s, so this leaves room to report a stall instead of
// dropping the console response.
const testEmailTimeout = 20 * time.Second

// emailDeliveryView is the console's read-only picture of the live SMTP
// configuration. It reports whether authentication is configured but never the
// credential itself.
type emailDeliveryView struct {
	Provider            string
	Configured          bool
	Host                string
	Port                int
	From                string
	Auth                bool
	ImplicitTLS         bool
	ConfirmationEnabled bool
	Recipient           string
	Problem             string
}

// emailDelivery maps the running configuration onto that view, naming the
// first thing that would stop a send rather than leaving the operator to infer
// it from an empty field.
func (a *App) emailDelivery() emailDeliveryView {
	view := emailDeliveryView{
		Provider:            a.cfg.SMTPProvider,
		Configured:          a.email != nil,
		Host:                a.cfg.SMTPHost,
		Port:                a.cfg.SMTPPort,
		From:                a.cfg.SMTPFrom,
		Auth:                a.cfg.SMTPUsername != "",
		ImplicitTLS:         a.cfg.SMTPPort == smtpImplicitTLSPort,
		ConfirmationEnabled: a.cfg.EmailConfirmationEnabled,
	}
	if view.Provider == "" {
		view.Provider = "smtp"
	}
	switch {
	case a.email == nil:
		view.Problem = "No relay is configured (SMTP_HOST is empty), so verification codes and receipts are not emailed"
	case view.From == "":
		view.Problem = "No envelope sender is configured (SMTP_FROM and ADMIN_EMAIL are both empty), so every send will be refused before the relay is reached"
	}
	return view
}

// smtpRelay names the configured relay for logs, audit details and the console,
// so a reader can tell which endpoint a send used.
func (a *App) smtpRelay() string {
	if a.cfg.SMTPHost == "" {
		return "unconfigured"
	}
	return fmt.Sprintf("%s:%d", a.cfg.SMTPHost, a.cfg.SMTPPort)
}

// adminMessaging renders the messaging cost meter: outbound message counts
// per flow with the average messages per completed web transaction and the
// estimated Meta cost at the configurable naira-per-message rates. It also
// carries the SMTP probe, because a relay misconfiguration otherwise stays
// invisible until a customer cannot receive a verification code.
func (a *App) adminMessaging(w http.ResponseWriter, r *http.Request) {
	a.renderAdminMessaging(w, r, "", "")
}

// renderAdminMessaging assembles the console, optionally with the outcome of a
// test send, so the operator sees the result next to the configuration that
// produced it.
func (a *App) renderAdminMessaging(w http.ResponseWriter, r *http.Request, sent, failure string) {
	summary, err := a.store.MessageStats(r.Context(), 30, a.cfg.MessageCostServiceNGN, a.cfg.MessageCostMarketingNGN)
	if err != nil {
		a.logger.WarnContext(r.Context(), "message stats failed", "error", err)
		http.Error(w, "could not load messaging stats", http.StatusInternalServerError)
		return
	}
	delivery := a.emailDelivery()
	delivery.Recipient = strings.TrimSpace(adminEmailFromContext(r.Context()))
	a.renderAdmin(w, "admin_messaging.html", r, "Messaging cost", map[string]any{
		"Summary":    summary,
		"Email":      delivery,
		"EmailSent":  sent,
		"EmailError": failure,
	})
}

// adminSendTestEmail delivers one message through the running relay so a bad
// host, credential or unverified sender domain surfaces here instead of at a
// customer's first sign-in. The recipient is the signed-in operator's own
// address: the endpoint never takes one from the form, so it cannot be turned
// into a relay to arbitrary inboxes.
func (a *App) adminSendTestEmail(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	recipient := strings.TrimSpace(adminEmailFromContext(r.Context()))
	relay := a.smtpRelay()
	if err := a.sendTestEmail(r.Context(), recipient); err != nil {
		a.logger.WarnContext(r.Context(), "test email failed", "to", recipient, "relay", relay, "error", err)
		a.audit(r, "admin.email.test_failed", "", "", map[string]any{"to": recipient, "relay": relay, "error": err.Error()})
		a.renderAdminMessaging(w, r, "", "Test email failed: "+err.Error())
		return
	}
	a.logger.InfoContext(r.Context(), "test email sent", "to", recipient, "relay", relay)
	a.audit(r, "admin.email.test_sent", "", "", map[string]any{"to": recipient, "relay": relay})
	a.renderAdminMessaging(w, r, fmt.Sprintf("Test email accepted by %s for %s. Check that inbox, spam included; a bounce there points at an unverified sender domain.", relay, recipient), "")
}

// sendTestEmail performs the send, so the probe and any future caller share one
// timeout and the same explicit failure when delivery is not configured.
func (a *App) sendTestEmail(ctx context.Context, recipient string) error {
	if a.email == nil {
		return errors.New("email delivery is not configured: SMTP_HOST is empty")
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return errors.New("the signed-in admin account has no email address to send to")
	}
	appName := strings.TrimSpace(a.cfg.AppName)
	if appName == "" {
		appName = "the platform"
	}
	transport := "STARTTLS"
	if a.cfg.SMTPPort == smtpImplicitTLSPort {
		transport = "implicit TLS"
	}
	auth := "none"
	if a.cfg.SMTPUsername != "" {
		auth = "PLAIN as " + a.cfg.SMTPUsername
	}
	body := fmt.Sprintf(
		"This is a test message from %s.\n\nRelay: %s (%s)\nEnvelope sender: %s\nAuthentication: %s\n\nIf you are reading this, transactional email is delivering. Verification codes, password links and receipts leave through the same path.\n",
		appName, a.smtpRelay(), transport, a.cfg.SMTPFrom, auth,
	)
	ctx, cancel := context.WithTimeout(ctx, testEmailTimeout)
	defer cancel()
	return a.email.Send(ctx, recipient, appName+" SMTP test", body)
}
