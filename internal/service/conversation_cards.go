package service

// Physical card requests in chat. A customer asks for a card to be handed over
// at a local agent in their area; the request is reviewed by compliance, and a
// card only becomes collectable once every issuer/PCI/funding/API/fulfilment
// gate is evidenced and an operator records externally issued stock. The
// customer then receives a one-time pickup code and collects in person with a
// government photo ID.
//
// The chat never asks for card numbers, PINs, CVVs or identity-document
// numbers: nothing in this flow is card data, and the handover records the ID
// *type* the agent inspected rather than the number. See
// docs/payments/CARD_REQUESTS.md.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

const (
	cardStepName    = "card_request_name"
	cardStepRegion  = "card_request_region"
	cardStepConfirm = "card_request_confirm"

	cardAreaPrefix     = "card_area:"
	cardAreaPagePrefix = "card_areas_page:"

	cardConfirmButton = "confirm_card_request"
	cardCancelButton  = "cancel_card_request"
)

// startCardRequest opens the physical-card request flow. It is reachable from
// the free-text card commands rather than a menu row because the main menu is
// already at WhatsApp's 10-row ceiling.
func (s *ConversationService) startCardRequest(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.cfg.CardRequestsEnabled {
		return s.sendText(ctx, channel, recipient,
			"Physical card requests aren't open yet. We'll announce it on this number when they are.")
	}
	if !s.userIsApprovedIndividual(ctx, user) {
		// A card is issued to a named, verified individual, so the CBN Tier 2
		// identity step comes first.
		return s.startIndividualUpgrade(ctx, channel, recipient, user, session)
	}
	if open, err := s.store.CardRequestsByUser(ctx, user.ID, 1); err == nil && len(open) > 0 {
		switch open[0].Status {
		case store.CardRequestRequested, store.CardRequestApproved, store.CardRequestReady:
			return s.sendCardRequestStatus(ctx, channel, recipient, user)
		}
	}
	session.State = cardStepName
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		"Request a Faya card\n\nEnter the full name exactly as it should appear on the card:")
}

func (s *ConversationService) handleCardRequestName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	name := strings.TrimSpace(input)
	if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
		return s.sendText(ctx, channel, recipient, "Please enter the full name as it should appear on the card.")
	}
	session.Data["cardholder_name"] = name
	session.State = cardStepRegion
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCardAreaPicker(ctx, channel, recipient, 0)
}

// handleCardRequestRegion handles the collection-area picker: a row tap
// ("card_area:<id>"), a page turn, or a typed area name.
func (s *ConversationService) handleCardRequestRegion(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, cardAreaPagePrefix) {
		return s.sendCardAreaPicker(ctx, channel, recipient, parsePickerPage(strings.TrimPrefix(input, cardAreaPagePrefix)))
	}
	if strings.HasPrefix(input, cardAreaPrefix) {
		return s.selectCardArea(ctx, channel, recipient, session, strings.TrimPrefix(input, cardAreaPrefix))
	}
	if input == "" {
		return s.sendCardAreaPicker(ctx, channel, recipient, 0)
	}
	regions, err := s.store.AgentRegionsWithActiveAgent(ctx)
	if err != nil {
		return err
	}
	needle := strings.ToLower(input)
	var matches []store.AgentRegion
	for _, region := range regions {
		if strings.EqualFold(region.Code, input) || strings.Contains(strings.ToLower(region.Name), needle) {
			matches = append(matches, region)
		}
	}
	if len(matches) == 1 {
		return s.selectCardArea(ctx, channel, recipient, session, matches[0].ID.String())
	}
	return s.sendCardAreaPicker(ctx, channel, recipient, 0)
}

// sendCardAreaPicker renders only areas that currently have an active agent: a
// card request for an area with nobody to hand it over would be a promise the
// network cannot keep.
func (s *ConversationService) sendCardAreaPicker(ctx context.Context, channel, recipient string, page int) error {
	page = normalizePickerPage(page)
	regions, err := s.store.AgentRegionsWithActiveAgent(ctx)
	if err != nil {
		return err
	}
	if len(regions) == 0 {
		return s.sendText(ctx, channel, recipient,
			"No collection areas have an agent yet. Please try again later.")
	}
	start := page * pickerPageSize
	if start >= len(regions) {
		start = 0
	}
	end := start + pickerPageSize
	if end > len(regions) {
		end = len(regions)
	}
	rows := make([]ports.InteractiveRow, 0, end-start+2)
	for _, region := range regions[start:end] {
		rows = append(rows, ports.InteractiveRow{
			ID:          cardAreaPrefix + region.ID.String(),
			Title:       region.Name,
			Description: regionDescription(region),
		})
	}
	rows = appendPickerNavigation(rows, cardAreaPagePrefix, page, end < len(regions))
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Where do you want to collect your card?",
		ButtonLabel: "Choose area",
		Sections:    []ports.InteractiveSection{{Title: "Collection areas", Rows: rows}},
	})
}

// selectCardArea records the chosen area and moves to the review step.
func (s *ConversationService) selectCardArea(ctx context.Context, channel, recipient string, session store.Session, regionIDStr string) error {
	regionID, err := uuid.Parse(regionIDStr)
	if err != nil {
		return s.sendCardAreaPicker(ctx, channel, recipient, 0)
	}
	region, err := s.store.AgentRegionByID(ctx, regionID)
	if err != nil || region.Status != store.AgentRegionActive {
		return s.sendText(ctx, channel, recipient, "That area is no longer available. Choose another.")
	}
	agents, err := s.store.ActiveAgentsInRegion(ctx, regionID)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("No agent can hand over cards in %s yet. Choose a different area.", region.Name))
	}
	session.Data["region_id"] = region.ID.String()
	session.Data["region_name"] = region.Name
	session.State = cardStepConfirm
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: cardRequestReviewMessage(session.Data),
		Buttons: []ports.InteractiveButton{
			{ID: cardConfirmButton, Title: "Request card"},
			{ID: cardCancelButton, Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleCardRequestConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1", "confirm", cardConfirmButton, "request card", "yes":
		return s.submitCardRequest(ctx, channel, recipient, user, session)
	case "2", "cancel", cardCancelButton, "no":
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			"Card request cancelled. Send *menu* to start over.")
	}
	return s.sendText(ctx, channel, recipient,
		"Tap *Request card* to confirm, or *Cancel* to stop.")
}

// submitCardRequest records the request. Approval and readiness are separate
// steps: this only queues it for compliance.
func (s *ConversationService) submitCardRequest(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	regionID, err := uuid.Parse(session.Data["region_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			"That card request expired. Send *request card* to start again.")
	}
	request, err := s.store.CreateCardRequest(ctx, store.CreateCardRequestParams{
		UserID:         user.ID,
		Channel:        channel,
		RegionID:       regionID,
		CardholderName: session.Data["cardholder_name"],
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCardRequestOpen):
			session.State, session.Data = "menu", map[string]string{}
			if saveErr := s.saveSession(ctx, session); saveErr != nil {
				return saveErr
			}
			return s.sendCardRequestStatus(ctx, channel, recipient, user)
		case errors.Is(err, store.ErrCardRequestRegionInvalid):
			session.State = cardStepRegion
			if saveErr := s.saveSession(ctx, session); saveErr != nil {
				return saveErr
			}
			return s.sendCardAreaPicker(ctx, channel, recipient, 0)
		}
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf(
		"Card request received (%s).\n\nCardholder: %s\nCollect at: %s\n\nCompliance reviews the request, and you'll get a pickup code here once your card has reached the agent. Bring a government photo ID to collect.",
		cardRequestReference(request.ID), request.CardholderName, request.RegionName))
}

// sendCardRequestStatus answers "where is my card?" with the customer's most
// recent request. It never shows anyone else's request and never includes a
// code: codes are delivered once, when the card is ready.
func (s *ConversationService) sendCardRequestStatus(ctx context.Context, channel, recipient string, user store.User) error {
	requests, err := s.store.CardRequestsByUser(ctx, user.ID, 3)
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		return s.sendText(ctx, channel, recipient,
			"You have no card request yet. Send *request card* to start one.")
	}
	var b strings.Builder
	b.WriteString("*Your card requests*\n")
	for _, request := range requests {
		b.WriteString("\n")
		b.WriteString(cardRequestReference(request.ID))
		b.WriteString(" · ")
		b.WriteString(cardStatusLabel(request))
		b.WriteString("\nCardholder: ")
		b.WriteString(request.CardholderName)
		if request.RegionName != "" {
			b.WriteString("\nCollect at: ")
			b.WriteString(request.RegionName)
		}
		if request.Status == store.CardRequestReady {
			b.WriteString("\n\nYour pickup code was sent separately. Bring that code and a government photo ID to the agent.")
		}
		if request.Status == store.CardRequestRejected && request.ReviewNote != "" {
			b.WriteString("\nReason: ")
			b.WriteString(request.ReviewNote)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSend *request card* to request another card once this one is collected.")
	return s.sendText(ctx, channel, recipient, b.String())
}

// cardRequestReviewMessage renders the confirm-step review.
func cardRequestReviewMessage(data map[string]string) string {
	return fmt.Sprintf("*Review card request*\n\nCardholder: %s\nCollect at: %s\n\nYour card is made to order and handed over by the agent in person. You'll need a government photo ID and the one-time pickup code we send you.\n\nRequest this card?",
		data["cardholder_name"], data["region_name"])
}

// cardRequestReference is the short, human-readable request reference shown to
// the customer and used in audit entries.
func cardRequestReference(id uuid.UUID) string {
	raw := strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))
	if len(raw) > 8 {
		raw = raw[:8]
	}
	return "XG-CARD-" + raw
}

// cardStatusLabel translates a stored status into customer-facing copy.
func cardStatusLabel(request store.CardRequest) string {
	switch request.Status {
	case store.CardRequestRequested:
		return "with compliance for review"
	case store.CardRequestApproved:
		return "approved · waiting for your card to reach the agent"
	case store.CardRequestReady:
		return "ready for collection"
	case store.CardRequestCollected:
		if request.CollectedAt != nil {
			return "collected on " + request.CollectedAt.Format("2 Jan 2006")
		}
		return "collected"
	case store.CardRequestRejected:
		return "not approved"
	}
	return request.Status
}
