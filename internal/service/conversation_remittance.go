package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// Cash-pickup remittance ("send cash for pickup"). The sender names a recipient
// and picks a destination region; Xego auto-assigns the least-loaded active
// agent in that region. The sender pays amount + transfer fee + agent
// commission up front; the recipient collects cash from the agent with a
// pickup code + OTP + government ID. The remittance and its code are opened by
// the payment's post-success hook (see PaymentService.applyRemittanceSettlement).
//
// The flow is reached from the free-text "send cash" commands rather than a
// menu row: the main menu is already at WhatsApp's 10-row ceiling, so a new
// top-level row would push it over.

const (
	remittanceStepName     = "send_agent_name"
	remittanceStepPhone    = "send_agent_phone"
	remittanceStepAmount   = "send_agent_amount"
	remittanceStepRegion   = "send_agent_region"
	remittanceStepConfirm  = "send_agent_confirm"
	remittanceRegionPrefix = "reg:"
	remittanceRegionPage   = "regions_page:"
)

// startSendToAgent initiates the cash-pickup flow.
func (s *ConversationService) startSendToAgent(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.cfg.AgentNetworkEnabled {
		return s.sendText(ctx, channel, recipient,
			"Cash pickup at an agent isn't available right now. Please try again later.")
	}
	if !s.userIsApprovedIndividual(ctx, user) {
		// Sending money on someone's behalf needs the same CBN Tier 2 identity
		// as an individual transfer.
		return s.startIndividualUpgrade(ctx, channel, recipient, user, session)
	}
	session.State = remittanceStepName
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		"Send cash for pickup\n\nEnter the recipient's full name (as it appears on their ID):")
}

func (s *ConversationService) handleSendAgentName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	name := strings.TrimSpace(input)
	if len([]rune(name)) < 2 {
		return s.sendText(ctx, channel, recipient, "Please enter the recipient's full name.")
	}
	session.Data["agent_recipient_name"] = name
	session.State = remittanceStepPhone
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptRemittanceStep(ctx, channel, recipient, session.Data)
}

func (s *ConversationService) handleSendAgentPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	normalized := domain.CanonicalE164Phone(strings.TrimSpace(input))
	if len(strings.TrimPrefix(normalized, "+")) < 10 {
		return s.sendText(ctx, channel, recipient, "That doesn't look like a valid phone number. Try again (e.g. 08012345678).")
	}
	session.Data["agent_recipient_phone"] = normalized
	session.State = remittanceStepAmount
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptRemittanceStep(ctx, channel, recipient, session.Data)
}

func (s *ConversationService) handleSendAgentAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amountKobo, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invalid amount: %s. Enter a valid amount in Naira.", err))
	}
	session.Data["amount_kobo"] = fmt.Sprintf("%d", amountKobo)
	session.State = remittanceStepRegion
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendRemittanceRegionPicker(ctx, channel, recipient, 0)
}

// handleSendAgentRegion handles the region picker: a row tap ("reg:<id>"), a
// page turn ("regions_page:<n>"), or a typed region name.
func (s *ConversationService) handleSendAgentRegion(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, remittanceRegionPage) {
		return s.sendRemittanceRegionPicker(ctx, channel, recipient, parsePickerPage(strings.TrimPrefix(input, remittanceRegionPage)))
	}
	if strings.HasPrefix(input, remittanceRegionPrefix) {
		return s.selectRemittanceRegion(ctx, channel, recipient, session, strings.TrimPrefix(input, remittanceRegionPrefix))
	}
	if input == "" {
		return s.sendRemittanceRegionPicker(ctx, channel, recipient, 0)
	}
	regions, err := s.store.AgentRegions(ctx, true)
	if err != nil {
		return err
	}
	var matches []store.AgentRegion
	needle := strings.ToLower(input)
	for _, r := range regions {
		if strings.EqualFold(r.Code, input) || strings.Contains(strings.ToLower(r.Name), needle) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 1 {
		return s.selectRemittanceRegion(ctx, channel, recipient, session, matches[0].ID.String())
	}
	return s.sendRemittanceRegionPicker(ctx, channel, recipient, 0)
}

// selectRemittanceRegion assigns an agent in the chosen region (least-loaded,
// active) and computes the agent commission, then moves to review.
func (s *ConversationService) selectRemittanceRegion(ctx context.Context, channel, recipient string, session store.Session, regionIDStr string) error {
	regionID, err := uuid.Parse(regionIDStr)
	if err != nil {
		return s.sendRemittanceRegionPicker(ctx, channel, recipient, 0)
	}
	region, err := s.store.AgentRegionByID(ctx, regionID)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "That area is no longer available. Choose another.")
	}
	agentID, err := s.store.AssignAgentInRegion(ctx, regionID)
	if err == store.ErrAgentNotFound {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("No agent is available in %s yet. Choose a different area.", region.Name))
	}
	if err != nil {
		return err
	}
	profile, err := s.store.AgentProfileByMerchant(ctx, agentID)
	if err != nil {
		return err
	}
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	if profile.PerPayoutLimitKobo > 0 && amountKobo > profile.PerPayoutLimitKobo {
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Agents in %s can pay out up to %s per collection. Enter a smaller amount or choose another area.",
				region.Name, domain.FormatNGN(profile.PerPayoutLimitKobo)))
	}
	commission := amountKobo * int64(profile.CommissionBps) / 10000
	session.Data["region_id"] = region.ID.String()
	session.Data["region_name"] = region.Name
	session.Data["agent_merchant_id"] = agentID.String()
	session.Data["commission_kobo"] = fmt.Sprintf("%d", commission)
	session.State = remittanceStepConfirm
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, s.remittanceReviewMessage(session.Data))
}

func (s *ConversationService) handleSendAgentConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1":
		return s.initSendAgentBankTransfer(ctx, channel, recipient, user, session)
	case "2", "wallet", "pay from wallet":
		return s.initSendAgentWallet(ctx, channel, recipient, user, session)
	case "cancel":
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Cash pickup cancelled. Send *menu* to start over.")
	}
	return s.sendText(ctx, channel, recipient, "Send *1* to pay by bank transfer, *2* to pay from your wallet, or *cancel* to abort.")
}

// sendRemittanceRegionPicker renders the active regions as a paged interactive
// list. Row ids carry the region id ("reg:<uuid>").
func (s *ConversationService) sendRemittanceRegionPicker(ctx context.Context, channel, recipient string, page int) error {
	page = normalizePickerPage(page)
	regions, err := s.store.AgentRegions(ctx, true)
	if err != nil {
		return err
	}
	if len(regions) == 0 {
		return s.sendText(ctx, channel, recipient,
			"No cash pickup areas are open right now. Please try again later.")
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
			ID:          remittanceRegionPrefix + region.ID.String(),
			Title:       region.Name,
			Description: regionDescription(region),
		})
	}
	rows = appendPickerNavigation(rows, remittanceRegionPage, page, end < len(regions))
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Where should the recipient collect the cash?",
		ButtonLabel: "Choose area",
		Sections:    []ports.InteractiveSection{{Title: "Pickup areas", Rows: rows}},
	})
}

// promptRemittanceStep sends the prompt for the next missing field.
func (s *ConversationService) promptRemittanceStep(ctx context.Context, channel, recipient string, data map[string]string) error {
	switch firstMissingRemittanceStep(data) {
	case remittanceStepName:
		return s.sendText(ctx, channel, recipient,
			"Send cash for pickup\n\nEnter the recipient's full name (as it appears on their ID):")
	case remittanceStepPhone:
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Recipient: %s\n\nEnter the recipient's phone number (e.g. 08012345678):", data["agent_recipient_name"]))
	case remittanceStepAmount:
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Recipient: %s (%s)\n\nEnter the amount in Naira (e.g. 5000):",
				data["agent_recipient_name"], data["agent_recipient_phone"]))
	case remittanceStepRegion:
		return s.sendRemittanceRegionPicker(ctx, channel, recipient, 0)
	case remittanceStepConfirm:
		return s.sendText(ctx, channel, recipient, s.remittanceReviewMessage(data))
	}
	return nil
}

func firstMissingRemittanceStep(data map[string]string) string {
	if strings.TrimSpace(data["agent_recipient_name"]) == "" {
		return remittanceStepName
	}
	if strings.TrimSpace(data["agent_recipient_phone"]) == "" {
		return remittanceStepPhone
	}
	if strings.TrimSpace(data["amount_kobo"]) == "" {
		return remittanceStepAmount
	}
	if strings.TrimSpace(data["region_id"]) == "" {
		return remittanceStepRegion
	}
	return remittanceStepConfirm
}

// remittanceReviewMessage renders the confirm-step review. The sender pays the
// amount plus the transfer fee plus the agent's commission on top.
func (s *ConversationService) remittanceReviewMessage(data map[string]string) string {
	amountKobo := parseAmountKobo(data["amount_kobo"])
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), amountKobo).FeeKobo
	commission := parseAmountKobo(data["commission_kobo"])
	totalPay := amountKobo + fee + commission
	return fmt.Sprintf("*Review cash pickup*\n\nRecipient: %s (%s)\nCollect at: %s\n\nAmount: %s\nTransfer fee: %s\nAgent fee: %s\nTotal you pay: %s\n\nThe recipient collects the full amount in cash from the agent with their pickup code and a valid ID.\n\nSend *1* to pay by bank transfer, *2* to pay from your wallet, or *cancel* to abort.",
		data["agent_recipient_name"], data["agent_recipient_phone"], data["region_name"],
		domain.FormatNGN(amountKobo), domain.FormatNGN(fee), domain.FormatNGN(commission), domain.FormatNGN(totalPay))
}

// initSendAgentBankTransfer creates the sender's collection payment on the
// bank-transfer rail; the pickup is opened by the post-success hook.
func (s *ConversationService) initSendAgentBankTransfer(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), amountKobo).FeeKobo
	commission := parseAmountKobo(session.Data["commission_kobo"])
	totalPay := amountKobo + fee + commission

	payment, err := s.payments.CreateRemittanceDraft(ctx, user, channel, recipient, totalPay, ProviderBankTransfer, s.remittanceMeta(session.Data))
	if err != nil {
		return friendlyAllowanceErr(err)
	}
	recipientName := session.Data["agent_recipient_name"]
	regionName := session.Data["region_name"]
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure checkout is ready.\n\nRecipient: %s\nCollect at: %s\n\nAmount: %s\nTransfer fee: %s\nAgent fee: %s\nTotal you pay: %s\n\nXego verifies the payment before the pickup code is issued.",
			recipientName, regionName,
			domain.FormatNGN(amountKobo), domain.FormatNGN(fee), domain.FormatNGN(commission), domain.FormatNGN(totalPay)),
		s.payments.HostedCheckoutURL(payment))
}

// initSendAgentWallet is initSendAgentBankTransfer on the wallet rail.
func (s *ConversationService) initSendAgentWallet(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), amountKobo).FeeKobo
	commission := parseAmountKobo(session.Data["commission_kobo"])
	totalPay := amountKobo + fee + commission

	payment, err := s.payments.CreateRemittanceDraft(ctx, user, channel, recipient, totalPay, ProviderWallet, s.remittanceMeta(session.Data))
	if err != nil {
		return friendlyAllowanceErr(err)
	}
	if err := s.beginWalletConfirm(ctx, channel, recipient, user, session, payment); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("*Pay from wallet*\n\nRecipient: %s\nCollect at: %s\n\nAmount: %s\nTransfer fee: %s\nAgent fee: %s\nTotal you pay: %s%s\n\nPay instantly from your Xego wallet?",
			session.Data["agent_recipient_name"], session.Data["region_name"],
			domain.FormatNGN(amountKobo), domain.FormatNGN(fee), domain.FormatNGN(commission),
			domain.FormatNGN(totalPay), s.walletBalanceLine(ctx, user, totalPay)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Pay from wallet"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

// remittanceMeta builds the payment metadata the post-success hook reads to
// open the remittance.
func (s *ConversationService) remittanceMeta(data map[string]string) map[string]any {
	return map[string]any{
		"remittance": map[string]any{
			"agent_merchant_id": data["agent_merchant_id"],
			"region_id":         data["region_id"],
			"recipient_name":    data["agent_recipient_name"],
			"recipient_phone":   data["agent_recipient_phone"],
			"amount_kobo":       parseAmountKobo(data["amount_kobo"]),
			"fee_kobo":          XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), parseAmountKobo(data["amount_kobo"])).FeeKobo,
			"commission_kobo":   parseAmountKobo(data["commission_kobo"]),
		},
	}
}

// requestRemittanceOTP mints an OTP for the sender's most recent cash pickup
// still awaiting collection and sends it to the sender to forward (D8). The
// OTP is short-lived, and only one is active at a time.
func (s *ConversationService) requestRemittanceOTP(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.cfg.AgentNetworkEnabled {
		return s.sendText(ctx, channel, recipient, "Cash pickup isn't available right now.")
	}
	remittances, err := s.store.RemittancesBySender(ctx, user.ID, 10)
	if err != nil {
		return err
	}
	var active store.Remittance
	found := false
	for _, r := range remittances {
		if r.Status == store.RemittanceAwaitingPickup {
			active, found = r, true
			break
		}
	}
	if !found {
		return s.sendText(ctx, channel, recipient,
			"You have no cash pickup waiting for collection. Send *send cash* to start one.")
	}
	salt, err := store.NewRemittanceSalt()
	if err != nil {
		return err
	}
	code, err := store.NewRemittanceCode()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(s.cfg.RemittanceOTPTTL)
	if err := s.store.RequestRemittanceOTP(ctx, active.ID, store.RemittanceCodeHash(salt, code), salt, expiresAt); err != nil {
		if errors.Is(err, store.ErrRemittanceNotClaimable) {
			return s.sendText(ctx, channel, recipient, "That cash pickup has expired. Send *send cash* to start a new one.")
		}
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Your OTP for the %s cash pickup to %s is %s.\n\nIt expires in %d minutes. Share it with the recipient so they can collect at the agent.",
			domain.FormatNGN(active.AmountKobo), active.RecipientName, code, int(s.cfg.RemittanceOTPTTL.Minutes())))
}

func regionDescription(r store.AgentRegion) string {
	switch {
	case r.State != "" && r.LGA != "":
		return r.State + " · " + r.LGA
	case r.State != "":
		return r.State
	case r.LGA != "":
		return r.LGA
	}
	return ""
}
