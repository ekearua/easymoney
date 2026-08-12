// C18: chat content guard admin surface. The /admin/chat-guard page lists
// blocked chat messages that tried to send card/PIN/CVV/OTP material, so
// compliance can review PCI / CBN consumer-protection attempts. The payload
// shown is always the redacted copy recorded by the conversation service.
package app

import (
	"net/http"
)

// adminChatGuard renders the blocked-attempt log.
func (a *App) adminChatGuard(w http.ResponseWriter, r *http.Request) {
	events, err := a.store.ListChatGuardEvents(r.Context(), 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list chat guard events", "error", err)
		http.Error(w, "chat guard log unavailable", http.StatusInternalServerError)
		return
	}
	total, err := a.store.ChatGuardAttemptCount(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "chat guard attempt count", "error", err)
		http.Error(w, "chat guard log unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_chat_guard.html", r, "Chat guard", map[string]any{
		"Events": events,
		"Total":  total,
	})
}