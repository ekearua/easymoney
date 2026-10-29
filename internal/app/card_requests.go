package app

// Physical card requests (migration 106): the compliance console that reviews
// requests and records issuer readiness, and the agent console that hands
// issued cards over against a one-time code and a visual ID check.
//
// Issuance is deliberately outside this codebase. An operator orders the card
// from the issuer, then records the issuer reference here; the request can only
// be marked ready when every readiness gate is evidenced *and* an active agent
// in the customer's area has been chosen. Nothing here captures a PAN, CVV, PIN
// or an identity-document number: the handover records the ID type the agent
// inspected. See docs/payments/CARD_REQUESTS.md.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminCardRequestView adds the display-only fields the queue needs to its
// stored request: the short reference and the agents that can take the card.
type adminCardRequestView struct {
	store.CardRequest
	Reference string
	Agents    []store.AgentOption
}

// cardGateView pairs a stored readiness gate with its operator-facing copy.
type cardGateView struct {
	store.CardIssuerGate
	Label string
	Help  string
}

// cardGateCopy is the single place a gate's meaning is written down, so the
// console cannot describe a gate differently from the store's enforcement.
func cardGateCopy(gate string) (label, help string) {
	switch gate {
	case store.CardGateIssuerContract:
		return "Issuer contract",
			"A signed card-issuing agreement names Xego as the programme owner and defines who issues, funds and supports the plastic."
	case store.CardGatePCIScope:
		return "PCI scope",
			"The data-flow review is signed off: the platform never handles a PAN, CVV or PIN, and the issuer/processor owns card-data scope."
	case store.CardGateFundingSettlement:
		return "Funding and settlement",
			"The prefunding or settlement account, cardholder liability and fee flows are agreed and reconciled to the ledger."
	case store.CardGateIssuerAPI:
		return "Issuer API",
			"The documented issuance and status APIs are contracted for this programme; Xego does not call undocumented endpoints."
	case store.CardGateFulfillment:
		return "Fulfilment",
			"Card personalisation, delivery to the agent, custody and lost-card handling have an owner and a written procedure."
	}
	return gate, ""
}

// cardGateViews decorates every gate in canonical order.
func cardGateViews(gates []store.CardIssuerGate) []cardGateView {
	views := make([]cardGateView, 0, len(gates))
	for _, gate := range gates {
		label, help := cardGateCopy(gate.GateKey)
		views = append(views, cardGateView{CardIssuerGate: gate, Label: label, Help: help})
	}
	return views
}

// adminCardRequests renders the compliance queue: readiness evidence first,
// then requests awaiting review or handover.
func (a *App) adminCardRequests(w http.ResponseWriter, r *http.Request) {
	a.renderAdminCardRequests(w, r, "", http.StatusOK)
}

// renderAdminCardRequests assembles the console, optionally with a failure
// message, so a refused action re-renders the page the operator was using
// rather than dropping them on a bare error.
func (a *App) renderAdminCardRequests(w http.ResponseWriter, r *http.Request, failure string, status int) {
	ctx := r.Context()
	gates, err := a.store.CardIssuerReadiness(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "card readiness", "error", err)
		http.Error(w, "card console unavailable", http.StatusInternalServerError)
		return
	}
	allReady, err := a.store.CardIssuerReady(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "card readiness check", "error", err)
		http.Error(w, "card console unavailable", http.StatusInternalServerError)
		return
	}
	open, err := a.store.CardRequestsByStatus(ctx, "open", 100)
	if err != nil {
		a.logger.WarnContext(ctx, "card request queue", "error", err)
		http.Error(w, "card console unavailable", http.StatusInternalServerError)
		return
	}
	ready, err := a.store.CardRequestsByStatus(ctx, store.CardRequestReady, 50)
	if err != nil {
		a.logger.WarnContext(ctx, "card request ready queue", "error", err)
		http.Error(w, "card console unavailable", http.StatusInternalServerError)
		return
	}
	collected, err := a.store.CardRequestsByStatus(ctx, store.CardRequestCollected, 25)
	if err != nil {
		a.logger.WarnContext(ctx, "card request collected queue", "error", err)
		http.Error(w, "card console unavailable", http.StatusInternalServerError)
		return
	}

	views := make([]adminCardRequestView, 0, len(open))
	for _, request := range open {
		agents, err := a.store.ActiveAgentsInRegion(ctx, request.RegionID)
		if err != nil {
			a.logger.WarnContext(ctx, "card area agents", "error", err, "region_id", request.RegionID)
		}
		views = append(views, adminCardRequestView{
			CardRequest: request,
			Reference:   cardReference(request.ID),
			Agents:      agents,
		})
	}
	// The issued and collected lists use the same view so every row shows the
	// customer-facing reference rather than a raw id.
	readyViews := make([]adminCardRequestView, 0, len(ready))
	for _, request := range ready {
		readyViews = append(readyViews, adminCardRequestView{CardRequest: request, Reference: cardReference(request.ID)})
	}
	collectedViews := make([]adminCardRequestView, 0, len(collected))
	for _, request := range collected {
		collectedViews = append(collectedViews, adminCardRequestView{CardRequest: request, Reference: cardReference(request.ID)})
	}

	data := map[string]any{
		"AppName":   a.cfg.AppName,
		"Title":     "Card requests",
		"CSRF":      csrfFromContext(ctx),
		"AdminRole": adminRoleFromContext(ctx),
		"Gates":     cardGateViews(gates),
		"AllReady":  allReady,
		"Requests":  views,
		"Ready":     readyViews,
		"Collected": collectedViews,
		"Error":     failure,
	}
	a.renderStatus(w, "admin_card_requests.html", data, status)
}

// adminCardRequestReview records a compliance decision. Rejecting needs a
// reason the customer can be shown; approving only moves the request to the
// readiness step.
func (a *App) adminCardRequestReview(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid card request", http.StatusBadRequest)
		return
	}
	decision := strings.ToLower(strings.TrimSpace(r.FormValue("decision")))
	if decision != "approve" && decision != "reject" {
		http.Error(w, "decision must be approve or reject", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if decision == "reject" && note == "" {
		a.renderAdminCardRequests(w, r, "A rejection needs a reason the customer can be shown.", http.StatusBadRequest)
		return
	}
	approve := decision == "approve"
	request, err := a.store.ReviewCardRequest(r.Context(), id, approve, note, adminEmailFromContext(r.Context()))
	if err != nil {
		a.logger.WarnContext(r.Context(), "card request review", "error", err, "request_id", id)
		switch {
		case errors.Is(err, store.ErrCardRequestNotFound):
			http.Error(w, "card request not found", http.StatusNotFound)
		case errors.Is(err, store.ErrCardRequestInvalidState):
			a.renderAdminCardRequests(w, r, "That request has already been reviewed.", http.StatusConflict)
		default:
			http.Error(w, "could not record the decision", http.StatusInternalServerError)
		}
		return
	}
	a.audit(r, "admin.card_request_review", "card_request", request.ID.String(), map[string]any{
		"decision": request.Status,
		"note":     note,
	})
	http.Redirect(w, r, "/admin/cards", http.StatusSeeOther)
}

// adminCardGateAttest records or withdraws one readiness gate. Marking a gate
// ready requires an evidence reference; withdrawing one is allowed and
// immediately removes the ability to mark any further stock ready.
func (a *App) adminCardGateAttest(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	gate := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "gate")))
	if !store.ValidCardReadinessGate(gate) {
		http.Error(w, "unknown readiness gate", http.StatusBadRequest)
		return
	}
	ready := r.FormValue("ready") == "on" || r.FormValue("ready") == "true"
	evidence := strings.TrimSpace(r.FormValue("evidence_ref"))
	note := strings.TrimSpace(r.FormValue("note"))
	if ready && evidence == "" {
		a.renderAdminCardRequests(w, r,
			"An evidenced gate needs a reference (contract id, review sign-off, ticket or document location).", http.StatusBadRequest)
		return
	}
	if err := a.store.AttestCardIssuerGate(r.Context(), gate, ready, evidence, note, adminEmailFromContext(r.Context())); err != nil {
		a.logger.WarnContext(r.Context(), "card gate attest", "error", err, "gate", gate)
		a.renderAdminCardRequests(w, r, "Could not record that gate.", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.card_gate_attested", "card_issuer_readiness", gate, map[string]any{
		"ready":        ready,
		"evidence_ref": evidence,
	})
	http.Redirect(w, r, "/admin/cards", http.StatusSeeOther)
}

// adminCardRequestReady records externally issued stock and assigns it to an
// agent, minting the one-time pickup code and sending it to the customer on the
// channel they requested from. The store re-checks the approval, every
// readiness gate, the issuer reference and the agent's region inside the
// transaction that makes the card collectable.
func (a *App) adminCardRequestReady(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid card request", http.StatusBadRequest)
		return
	}
	issuerReference := strings.TrimSpace(r.FormValue("issuer_reference"))
	if issuerReference == "" {
		a.renderAdminCardRequests(w, r, "Record the issuer reference for the card that was manufactured.", http.StatusBadRequest)
		return
	}
	agentMerchantID, err := uuid.Parse(strings.TrimSpace(r.FormValue("agent_merchant_id")))
	if err != nil {
		a.renderAdminCardRequests(w, r, "Choose the agent that will hand the card over.", http.StatusBadRequest)
		return
	}
	code, expiresAt, err := a.newCardPickupCode()
	if err != nil {
		a.logger.ErrorContext(r.Context(), "card pickup code", "error", err)
		http.Error(w, "could not issue a pickup code", http.StatusInternalServerError)
		return
	}
	salt, err := store.NewRemittanceSalt()
	if err != nil {
		a.logger.ErrorContext(r.Context(), "card pickup salt", "error", err)
		http.Error(w, "could not issue a pickup code", http.StatusInternalServerError)
		return
	}
	request, err := a.store.MarkCardRequestReady(r.Context(), store.MarkCardReadyParams{
		CardRequestID:   id,
		IssuerReference: issuerReference,
		AgentMerchantID: agentMerchantID,
		CodeHash:        store.RemittanceCodeHash(salt, code),
		CodeSalt:        salt,
		ExpiresAt:       expiresAt,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "card request ready", "error", err, "request_id", id)
		switch {
		case errors.Is(err, store.ErrCardIssuerNotReady):
			a.renderAdminCardRequests(w, r,
				"Evidence every readiness gate before marking stock ready.", http.StatusConflict)
		case errors.Is(err, store.ErrCardAgentUnavailable), errors.Is(err, store.ErrCardRequestRegionMismatch):
			a.renderAdminCardRequests(w, r,
				"That agent cannot hand this card over: it must be active and in the customer's collection area.", http.StatusConflict)
		case errors.Is(err, store.ErrCardRequestInvalidState):
			a.renderAdminCardRequests(w, r, "That request is not approved and awaiting stock.", http.StatusConflict)
		case errors.Is(err, store.ErrCardRequestNotFound):
			http.Error(w, "card request not found", http.StatusNotFound)
		default:
			a.renderAdminCardRequests(w, r, "Could not mark the card ready.", http.StatusInternalServerError)
		}
		return
	}
	if err := a.deliverCardPickupCode(r.Context(), request, code, expiresAt); err != nil {
		a.logger.ErrorContext(r.Context(), "card pickup delivery", "error", err, "request_id", request.ID)
		a.audit(r, "admin.card_request_ready", "card_request", request.ID.String(), map[string]any{
			"issuer_reference":  issuerReference,
			"agent_merchant_id": agentMerchantID.String(),
			"code_delivered":    false,
		})
		a.renderAdminCardRequests(w, r,
			"The card is ready but the pickup code could not be delivered. Use “Resend code” to try again.", http.StatusOK)
		return
	}
	a.audit(r, "admin.card_request_ready", "card_request", request.ID.String(), map[string]any{
		"issuer_reference":  issuerReference,
		"agent_merchant_id": agentMerchantID.String(),
		"code_delivered":    true,
	})
	http.Redirect(w, r, "/admin/cards", http.StatusSeeOther)
}

// adminCardRequestResend mints a fresh pickup code for a ready request and
// re-delivers it, for a code the customer never received or lost. The previous
// code is retired in the same transaction.
func (a *App) adminCardRequestResend(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid card request", http.StatusBadRequest)
		return
	}
	code, expiresAt, err := a.newCardPickupCode()
	if err != nil {
		http.Error(w, "could not issue a pickup code", http.StatusInternalServerError)
		return
	}
	salt, err := store.NewRemittanceSalt()
	if err != nil {
		http.Error(w, "could not issue a pickup code", http.StatusInternalServerError)
		return
	}
	request, err := a.store.ReissueCardPickupCode(r.Context(), id, store.RemittanceCodeHash(salt, code), salt, expiresAt)
	if err != nil {
		a.logger.WarnContext(r.Context(), "card code reissue", "error", err, "request_id", id)
		if errors.Is(err, store.ErrCardRequestInvalidState) {
			a.renderAdminCardRequests(w, r, "Only a card that is ready and uncollected can be reissued a code.", http.StatusConflict)
			return
		}
		http.Error(w, "could not reissue the code", http.StatusInternalServerError)
		return
	}
	if err := a.deliverCardPickupCode(r.Context(), request, code, expiresAt); err != nil {
		a.logger.ErrorContext(r.Context(), "card code delivery", "error", err, "request_id", request.ID)
		a.renderAdminCardRequests(w, r, "The new code was created but not delivered. Check the customer's channel and retry.", http.StatusOK)
		return
	}
	a.audit(r, "admin.card_code_reissued", "card_request", request.ID.String(), nil)
	http.Redirect(w, r, "/admin/cards", http.StatusSeeOther)
}

// newCardPickupCode mints a six-digit code and its expiry. The code is stored
// only as a salted digest; this is the only moment the plaintext exists in the
// process, and it leaves through the encrypted outbox.
func (a *App) newCardPickupCode() (string, time.Time, error) {
	code, err := store.NewRemittanceCode()
	if err != nil {
		return "", time.Time{}, err
	}
	return code, time.Now().Add(a.cfg.CardRequestPickupTTL), nil
}

// deliverCardPickupCode sends the code to the customer on the channel the
// request came from, falling back to WhatsApp when that channel cannot be
// resolved.
func (a *App) deliverCardPickupCode(ctx context.Context, request store.CardRequest, code string, expiresAt time.Time) error {
	channel := request.Channel
	contact, err := a.store.UserContactForChannel(ctx, request.UserID, channel)
	if err != nil || strings.TrimSpace(contact) == "" {
		channel = "whatsapp"
		contact, err = a.store.UserContactForChannel(ctx, request.UserID, channel)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(contact) == "" {
		return errors.New("customer has no reachable channel")
	}
	body := fmt.Sprintf(
		"Your Faya card is ready for collection at %s.\n\nPickup code: %s\n\nBring this code and a government photo ID. The code expires on %s.",
		request.RegionName, code, expiresAt.Local().Format("2 Jan 2006"))
	return a.store.EnqueueTextForChannel(ctx, request.UserID, channel, contact, body)
}

// agentCardRequests renders the agent handover console: the cards waiting for
// this agent, and the form that records a collection.
func (a *App) agentCardRequests(w http.ResponseWriter, r *http.Request) {
	a.renderAgent(w, "agent_cards.html", r, "Card handover", a.agentCardData(r, "", r.URL.Query().Get("collected")))
}

// agentCardData assembles the handover page payload.
func (a *App) agentCardData(r *http.Request, failure, success string) map[string]any {
	ctx := r.Context()
	agentMerchantID := agentMerchantIDFromContext(ctx)
	data := map[string]any{
		"Error":   failure,
		"Success": success,
		"Cards":   []store.CardRequest{},
	}
	if cards, err := a.store.AgentCardRequests(ctx, agentMerchantID, 50); err != nil {
		a.logger.WarnContext(ctx, "agent card requests", "error", err)
	} else {
		data["Cards"] = cards
	}
	if merchant, err := a.store.MerchantByID(ctx, agentMerchantID); err == nil {
		data["AgentName"] = merchant.Name
	}
	return data
}

// agentCardCollect records a handover. The agent enters the code the customer
// presents and the type of government ID inspected; the ID number is never
// captured, and the store re-checks the agent's region, the code and its expiry
// in one transaction.
func (a *App) agentCardCollect(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != agentCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if retryAfter := a.limiter.retryAfter(ip); retryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retryAfter.Seconds())+1))
		a.renderAgentStatus(w, "agent_cards.html", r, "Card handover",
			a.agentCardData(r, "Too many incorrect codes from this terminal. Wait a moment and try again.", ""),
			http.StatusTooManyRequests)
		return
	}
	request, err := a.store.CollectCardRequest(r.Context(), store.CollectCardParams{
		AgentMerchantID: agentMerchantIDFromContext(r.Context()),
		OperatorUserID:  agentUserIDFromContext(r.Context()),
		Code:            strings.TrimSpace(r.FormValue("code")),
		IDType:          strings.TrimSpace(r.FormValue("id_type")),
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "card collection refused", "error", err)
		if errors.Is(err, store.ErrCardPickupInvalid) {
			a.limiter.fail(ip)
		}
		a.renderAgentStatus(w, "agent_cards.html", r, "Card handover",
			a.agentCardData(r, agentCardError(err), ""), http.StatusOK)
		return
	}
	a.audit(r, "agent.card_collected", "card_request", request.ID.String(), map[string]any{
		"agent_merchant_id": agentMerchantIDFromContext(r.Context()).String(),
		"id_type":           request.CollectionIDType,
	})
	http.Redirect(w, r, "/agent/cards?collected="+cardReference(request.ID), http.StatusSeeOther)
}

// cardReference is the short, non-secret request reference used in the consoles.
func cardReference(id uuid.UUID) string {
	raw := strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))
	if len(raw) > 8 {
		raw = raw[:8]
	}
	return "XG-CARD-" + raw
}

// agentCardError turns a collection refusal into operator-friendly copy.
func agentCardError(err error) string {
	switch {
	case errors.Is(err, store.ErrCardPickupInvalid):
		return "That pickup code was not recognised. Ask the customer to check it."
	case errors.Is(err, store.ErrCardPickupExpired):
		return "That pickup code has expired. Compliance must reissue it."
	case errors.Is(err, store.ErrCardPickupLocked):
		return "This pickup code is locked after too many attempts. Compliance must reissue it."
	case errors.Is(err, store.ErrCardPickupAlreadyUsed):
		return "This card has already been collected."
	case errors.Is(err, store.ErrCardRequestRegionMismatch):
		return "This card was not assigned to your area."
	case errors.Is(err, store.ErrCardIDTypeRequired):
		return "Record the type of government photo ID you inspected."
	case errors.Is(err, store.ErrAgentNotActive), errors.Is(err, store.ErrAgentNotFound):
		return "Your agent account is not active. Contact operations."
	}
	return "The handover could not be recorded. Check the details and try again."
}
