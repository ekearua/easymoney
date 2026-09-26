package app

// Trust Fabric admin surfaces: the network ring view (entity links across
// shared devices / settlement accounts / channel identifiers) and the
// per-profile trust timeline (alerts + device sightings + audit entries in
// one chronological feed).

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminNetwork renders the ring-detection view: identity-link edges grouped
// into connected components, largest first. Edges come from the store;
// component grouping is DetectRings' pure logic.
func (a *App) adminNetwork(w http.ResponseWriter, r *http.Request) {
	links, err := a.store.IdentityLinks(r.Context(), 300)
	if err != nil {
		a.logger.WarnContext(r.Context(), "identity links", "error", err)
		http.Error(w, "network view unavailable", http.StatusInternalServerError)
		return
	}
	minMembers := 2
	if raw := strings.TrimSpace(r.URL.Query().Get("min")); raw != "" {
		if n, err := parseIntSafe(raw); err == nil && n >= 2 && n <= 50 {
			minMembers = n
		}
	}
	rings := store.DetectRings(links, minMembers)
	a.renderAdmin(w, "admin_network.html", r, "Trust network", map[string]any{
		"Links":      links,
		"Rings":      rings,
		"MinMembers": minMembers,
		"HasLinks":   len(links) > 0,
	})
}

// adminKYCProfile renders one subject's trust timeline: monitoring alerts,
// device sightings and user-scoped audit entries in one chronological feed.
func (a *App) adminKYCProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "userID")
	userID, err := uuid.Parse(id)
	if err != nil {
		http.Error(w, "unknown profile", http.StatusNotFound)
		return
	}
	profile, err := a.store.KYCProfileByUser(r.Context(), userID)
	if err != nil {
		http.Error(w, "unknown profile", http.StatusNotFound)
		return
	}
	timeline, err := a.store.TrustTimeline(r.Context(), userID, 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "trust timeline", "error", err)
		timeline = nil
	}
	// The same edges the network view computes, restricted to this subject.
	links, err := a.store.IdentityLinks(r.Context(), 300)
	if err != nil {
		a.logger.WarnContext(r.Context(), "identity links", "error", err)
		links = nil
	}
	var subjectLinks []store.IdentityLinkRow
	for _, l := range links {
		if l.SubjectA == id || l.SubjectB == id {
			subjectLinks = append(subjectLinks, l)
		}
	}
	user, err := a.store.UserByID(r.Context(), userID)
	displayName := ""
	if err == nil {
		displayName = user.DisplayName
	}
	a.renderAdmin(w, "admin_kyc_profile.html", r, "KYC profile", map[string]any{
		"Profile":       profile,
		"UserID":        id,
		"DisplayName":   displayName,
		"Timeline":      timeline,
		"SubjectLinks":  subjectLinks,
		"TimelineCount": len(timeline),
	})
}

// parseIntSafe parses a small non-negative integer query value.
func parseIntSafe(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, err
	}
	return n, nil
}
