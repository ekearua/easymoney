package service

import (
	"context"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) handleMerchant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	if strings.HasPrefix(input, "merchant_page:") {
		page := parsePickerPage(strings.TrimPrefix(input, "merchant_page:"))
		return s.sendMerchantPicker(ctx, channel, recipient, user, session.Data["merchant_query"], page)
	}
	if !strings.HasPrefix(input, "merchant:") {
		query := strings.TrimSpace(input)
		session.Data["merchant_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendMerchantPicker(ctx, channel, recipient, user, query, 0)
	}
	slug := strings.TrimPrefix(input, "merchant:")
	merchant, err := s.store.MerchantBySlug(ctx, slug)
	if err != nil {
		return s.sendMerchantPicker(ctx, channel, recipient, user, session.Data["merchant_query"], 0)
	}
	if err := s.store.TouchRecentMerchant(ctx, user.ID, merchant.ID); err != nil {
		return err
	}
	session.Data["merchant_slug"] = merchant.Slug
	delete(session.Data, "merchant_query")
	services, err := s.store.ListActiveMerchantServices(ctx, merchant.ID)
	if err != nil {
		return err
	}
	events, err := s.store.ListActiveEventsByMerchantID(ctx, merchant.ID)
	if err != nil {
		return err
	}
	if len(services) > 0 || len(events) > 0 {
		session.State = "select_service_or_amount"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendServicePicker(ctx, channel, recipient, merchant, services, events)
	}
	session.State = "enter_amount"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("How much would you like to pay %s?\n\nEnter an amount between %s and %s. Example: 2500",
			merchant.Name,
			domain.FormatNGN(s.cfg.PaymentMinKobo), domain.FormatNGN(s.cfg.PaymentMaxKobo)))
}

func (s *ConversationService) sendMerchants(ctx context.Context, channel, recipient string) error {
	merchants, err := s.store.ListActiveMerchants(ctx)
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants))
	for _, merchant := range merchants {
		rows = append(rows, ports.InteractiveRow{
			ID:          "merchant:" + merchant.Slug,
			Title:       merchant.Name,
			Description: merchant.Category + " · " + merchant.Description,
		})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient, Body: "Choose who you'd like to pay.", ButtonLabel: "View merchants",
		Sections: []ports.InteractiveSection{{Title: "Merchants", Rows: rows}},
	})
}

func (s *ConversationService) sendServicePicker(ctx context.Context, channel, recipient string, merchant store.Merchant, services []store.MerchantService, events []store.MerchantEvent) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("What would you like to pay %s for?\n", merchant.Name))
	n := 0
	for _, svc := range services {
		n++
		expiryText := ""
		if svc.ExpiresAt != nil {
			expiryText = fmt.Sprintf(" (expires %s)", svc.ExpiresAt.Format("02 Jan 2006"))
		}
		availText := ""
		if svc.QuantityAvailable >= 0 {
			availText = fmt.Sprintf(" [%d left]", svc.QuantityAvailable)
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s — %s%s%s", n, svc.Name, domain.FormatNGN(svc.UnitPriceKobo), availText, expiryText))
	}
	for _, evt := range events {
		n++
		dateText := ""
		if evt.EventStartAt != nil {
			dateText = fmt.Sprintf(" | %s", evt.EventStartAt.Format("02 Jan 2006 15:04"))
		}
		venueText := ""
		if evt.Venue != "" {
			venueText = fmt.Sprintf(" @ %s", evt.Venue)
		}
		sb.WriteString(fmt.Sprintf("\n%d. 📅 %s%s%s", n, evt.Name, venueText, dateText))
	}
	sb.WriteString("\n\nSend the number of your choice, or type CUSTOM to enter an amount.")
	return s.sendText(ctx, channel, recipient, sb.String())
}

func (s *ConversationService) sendMerchantPicker(ctx context.Context, channel, recipient string, user store.User, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	var sections []ports.InteractiveSection
	mainLimit := pickerPageSize
	hasRecents := false
	if query == "" {
		recents, err := s.store.RecentMerchantsForUser(ctx, user.ID, recentLimit)
		if err != nil {
			return err
		}
		if len(recents) > 0 {
			hasRecents = true
			mainLimit = pickerPageSize - len(recents)
			if mainLimit < 3 {
				mainLimit = 3
			}
			if page == 0 {
				rows := make([]ports.InteractiveRow, 0, len(recents))
				for _, merchant := range recents {
					rows = append(rows, merchantRow(merchant))
				}
				sections = append(sections, ports.InteractiveSection{Title: "Recent merchants", Rows: rows})
			}
		}
	}
	offset := page * mainLimit
	var merchants []store.Merchant
	var hasMore bool
	var err error
	if hasRecents && query == "" {
		merchants, hasMore, err = s.store.SearchMerchantsExcludingUserRecents(ctx, user.ID, query, offset, mainLimit)
	} else {
		merchants, hasMore, err = s.store.SearchMerchants(ctx, query, offset, mainLimit)
	}
	if err != nil {
		return err
	}
	rows := make([]ports.InteractiveRow, 0, len(merchants)+2)
	for _, merchant := range merchants {
		rows = append(rows, merchantRow(merchant))
	}
	rows = appendPickerNavigation(rows, "merchant_page:", page, hasMore)
	if len(rows) > 0 {
		sections = append(sections, ports.InteractiveSection{Title: "Merchants", Rows: rows})
	}
	if len(sections) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "No merchants are available right now. Please try again shortly.")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find that merchant. Type another merchant name, or type MENU to return to the main menu.")
	}
	body := "Choose who you'd like to pay.\n\nYou can also type a merchant name or category to search."
	if query != "" {
		body = fmt.Sprintf("Merchant search results for %q.\n\nChoose a merchant, or type another merchant name to search again.", query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient, Body: body, ButtonLabel: "View merchants",
		Sections: sections,
	})
}

func merchantRow(merchant store.Merchant) ports.InteractiveRow {
	return ports.InteractiveRow{
		ID:          "merchant:" + merchant.Slug,
		Title:       truncateInteractiveTitle(merchant.Name),
		Description: truncateInteractiveDescription(merchant.Category + " - " + merchant.Description),
	}
}
