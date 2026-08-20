package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

func (a *App) merchantEventsList(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	events, err := a.store.ListEventsByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_events_list.html", r, "Events", map[string]any{
		"Events": events,
	})
}

func (a *App) merchantEventNewForm(w http.ResponseWriter, r *http.Request) {
	a.renderMerchant(w, "merchant_event_form.html", r, "New event", map[string]any{
		"Edit":         false,
		"Tiers":        []store.EventTicketTier{},
		"CustomFields": []store.EventCustomField{},
	})
}

func (a *App) merchantEventCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	venue := strings.TrimSpace(r.FormValue("venue"))
	imageURL := strings.TrimSpace(r.FormValue("image_url"))
	if name == "" {
		http.Error(w, "event name required", http.StatusBadRequest)
		return
	}
	var eventStart, eventEnd *time.Time
	if v := strings.TrimSpace(r.FormValue("event_start_at")); v != "" {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			eventStart = &t
		}
	}
	if v := strings.TrimSpace(r.FormValue("event_end_at")); v != "" {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			eventEnd = &t
		}
	}
	evt, err := a.store.CreateEvent(r.Context(), merchantID, name, description, venue, eventStart, eventEnd, imageURL)
	if err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.saveEventTiers(r, evt.ID)
	http.Redirect(w, r, "/merchant/events?created=1", http.StatusSeeOther)
}

func (a *App) merchantEventEditForm(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	evt, err := a.store.EventByID(r.Context(), eventID)
	if err != nil || evt.MerchantID != merchantID {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}
	tiers, _ := a.store.TiersByEventID(r.Context(), eventID)
	customFields, _ := a.store.ListEventCustomFields(r.Context(), a.firstTierID(tiers))
	a.renderMerchant(w, "merchant_event_form.html", r, "Edit event", map[string]any{
		"Event":        evt, "Edit": true,
		"Tiers":        tiers,
		"CustomFields": customFields,
	})
}

func (a *App) merchantEventUpdate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	evt, err := a.store.EventByID(r.Context(), eventID)
	if err != nil || evt.MerchantID != merchantID {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	venue := strings.TrimSpace(r.FormValue("venue"))
	imageURL := strings.TrimSpace(r.FormValue("image_url"))
	if name == "" {
		http.Error(w, "event name required", http.StatusBadRequest)
		return
	}
	var eventStart, eventEnd *time.Time
	if v := strings.TrimSpace(r.FormValue("event_start_at")); v != "" {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			eventStart = &t
		}
	}
	if v := strings.TrimSpace(r.FormValue("event_end_at")); v != "" {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			eventEnd = &t
		}
	}
	if err := a.store.UpdateEvent(r.Context(), eventID, name, description, venue, eventStart, eventEnd, imageURL); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.saveEventTiers(r, eventID)
	http.Redirect(w, r, "/merchant/events?saved=1", http.StatusSeeOther)
}

func (a *App) merchantEventToggle(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if err := a.store.ToggleEvent(r.Context(), eventID, merchantID); err != nil {
		http.Error(w, "toggle failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/events?toggled=1", http.StatusSeeOther)
}

func (a *App) merchantEventTickets(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	evt, err := a.store.EventByID(r.Context(), eventID)
	if err != nil || evt.MerchantID != merchantID {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}
	tiers, _ := a.store.TiersByEventID(r.Context(), eventID)
	purchases, _ := a.store.EventTicketPurchasesByEventID(r.Context(), eventID)
	purchaseIDs := make([]uuid.UUID, len(purchases))
	for i, p := range purchases {
		purchaseIDs[i] = p.ID
	}
	customData, _ := a.store.EventPurchaseCustomDataByPurchaseIDs(r.Context(), purchaseIDs)
	a.renderMerchant(w, "merchant_event_tickets.html", r, "Event tickets", map[string]any{
		"Event":      evt,
		"Tiers":      tiers,
		"Purchases":  purchases,
		"CustomData": customData,
	})
}

// saveEventTiers parses dynamic tier rows from the form and applies custom fields
// to all tiers.
func (a *App) saveEventTiers(r *http.Request, eventID uuid.UUID) {
	names := r.Form["tier_name[]"]
	priceStrs := r.Form["tier_price_kobo[]"]
	capStrs := r.Form["tier_capacity[]"]
	activeChecks := r.Form["tier_active[]"]
	var tiers []store.EventTierSpec
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		price, _ := strconv.ParseInt(priceStrs[i], 10, 64)
		if price <= 0 {
			continue
		}
		cap := -1
		if i < len(capStrs) {
			if c, err := strconv.Atoi(strings.TrimSpace(capStrs[i])); err == nil {
				cap = c
			}
		}
		isActive := true
		if i < len(activeChecks) && activeChecks[i] == "0" {
			isActive = false
		}
		tiers = append(tiers, store.EventTierSpec{
			Name:      n,
			PriceKobo: price,
			Capacity:  cap,
			IsActive:  isActive,
		})
	}
	_ = a.store.SetEventTiersFull(r.Context(), eventID, tiers)

	// Save custom fields to all active tiers.
	cfNames := r.Form["cf_name[]"]
	cfTypes := r.Form["cf_type[]"]
	cfRequired := r.Form["cf_required[]"]
	cfOptions := r.Form["cf_options[]"]
	var cfSpecs []store.EventCustomFieldSpec
	for i, n := range cfNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		ft := "text"
		if i < len(cfTypes) {
			ft = cfTypes[i]
		}
		isReq := false
		if i < len(cfRequired) && cfRequired[i] == "1" {
			isReq = true
		}
		opts := ""
		if i < len(cfOptions) {
			opts = cfOptions[i]
		}
		cfSpecs = append(cfSpecs, store.EventCustomFieldSpec{
			FieldName:    n,
			FieldType:    ft,
			FieldOptions: opts,
			IsRequired:   isReq,
		})
	}
	if len(cfSpecs) > 0 {
		tiersNow, _ := a.store.TiersByEventID(r.Context(), eventID)
		for _, t := range tiersNow {
			_ = a.store.SetEventCustomFields(r.Context(), t.ID, cfSpecs)
		}
	}
}

func (a *App) firstTierID(tiers []store.EventTicketTier) uuid.UUID {
	if len(tiers) > 0 {
		return tiers[0].ID
	}
	return uuid.Nil
}
