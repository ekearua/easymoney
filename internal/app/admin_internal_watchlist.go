package app

// Spec G3: the internal watchlist admin console — reviewer-owned CRUD for
// the platform's own list of identities that must never be auto-cleared.
// Every mutation is audited with the acting admin's identity.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminInternalWatchlist renders the list with an add form and per-entry
// remove buttons. The `q` parameter filters by name prefix.
func (a *App) adminInternalWatchlist(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListInternalWatchlist(r.Context(), r.URL.Query().Get("q"), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "internal watchlist list", "error", err)
		http.Error(w, "watchlist unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_internal_watchlist.html", r, "Internal watchlist", map[string]any{
		"Entries": entries,
		"Query":   strings.TrimSpace(r.URL.Query().Get("q")),
	})
}

// adminInternalWatchlistAdd lists an identity. The audit trail is the point:
// a listing must be attributable to a named admin with a stated reason.
func (a *App) adminInternalWatchlistAdd(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())
	fullName := strings.TrimSpace(r.FormValue("full_name"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if fullName == "" || reason == "" {
		http.Error(w, "full name and reason are required", http.StatusBadRequest)
		return
	}
	entry := store.InternalWatchlistEntry{
		FullName:       fullName,
		PhoneNumber:    strings.TrimSpace(r.FormValue("phone_number")),
		BankAccountRef: strings.TrimSpace(r.FormValue("bank_account_ref")),
		Reason:         reason,
		ListedBy:       adminEmail,
	}
	if raw := strings.TrimSpace(r.FormValue("date_of_birth")); raw != "" {
		dob, err := time.Parse("2006-01-02", raw)
		if err != nil {
			http.Error(w, "date of birth must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		entry.DateOfBirth = &dob
	}
	if _, err := a.store.AddInternalWatchlistEntry(r.Context(), entry); err != nil {
		a.logger.WarnContext(r.Context(), "internal watchlist add", "error", err)
		http.Error(w, "could not list identity", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.watchlist_listed", "internal_watchlist", fullName,
		map[string]any{"reason": reason, "listed_by": adminEmail})
	http.Redirect(w, r, "/admin/internal-watchlist", http.StatusSeeOther)
}

// adminInternalWatchlistRemove delists an identity. The audit entry carries
// the entry's fields captured before deletion, since the row is gone after.
func (a *App) adminInternalWatchlistRemove(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid entry id", http.StatusBadRequest)
		return
	}
	entries, err := a.store.ListInternalWatchlist(r.Context(), "", 500)
	if err != nil {
		http.Error(w, "watchlist unavailable", http.StatusInternalServerError)
		return
	}
	var removed store.InternalWatchlistEntry
	for _, e := range entries {
		if e.ID == id {
			removed = e
			break
		}
	}
	adminEmail := adminEmailFromContext(r.Context())
	if err := a.store.RemoveInternalWatchlistEntry(r.Context(), id); err != nil {
		a.logger.WarnContext(r.Context(), "internal watchlist remove", "error", err)
		http.Error(w, "could not remove entry", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.watchlist_delisted", "internal_watchlist", removed.FullName,
		map[string]any{"removed_by": adminEmail, "reason_was": removed.Reason})
	http.Redirect(w, r, "/admin/internal-watchlist", http.StatusSeeOther)
}
