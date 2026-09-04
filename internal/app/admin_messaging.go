package app

import "net/http"

// adminMessaging renders the messaging cost meter: outbound message counts
// per flow with the average messages per completed web transaction and the
// estimated Meta cost at the configurable naira-per-message rates.
func (a *App) adminMessaging(w http.ResponseWriter, r *http.Request) {
	summary, err := a.store.MessageStats(r.Context(), 30, a.cfg.MessageCostServiceNGN, a.cfg.MessageCostMarketingNGN)
	if err != nil {
		a.logger.WarnContext(r.Context(), "message stats failed", "error", err)
		http.Error(w, "could not load messaging stats", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_messaging.html", r, "Messaging cost", map[string]any{
		"Summary": summary,
	})
}
