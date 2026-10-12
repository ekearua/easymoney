package app

// Trust Fabric admin surfaces: the network ring view (entity links across
// shared devices / settlement accounts / channel identifiers) and the
// per-profile trust timeline (alerts + device sightings + audit entries in
// one chronological feed).

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// adminNetwork renders the ring-detection view as a triage queue: rings
// scored by weighted sightings/recency (account edges weigh more than a
// shared browser) and multiplied by the members' alert signal, members
// annotated with tier and risk band. Rings with confirmed alert outcomes
// among their members raise one linked network case per member set.
func (a *App) adminNetwork(w http.ResponseWriter, r *http.Request) {
	links, err := a.store.IdentityLinksWithRecipients(r.Context(), 300)
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
	rings := store.DetectRingsFromIdentityLinks(links, minMembers)
	scored, err := a.store.EnrichRings(r.Context(), rings, links, time.Now())
	if err != nil {
		a.logger.WarnContext(r.Context(), "ring enrichment", "error", err)
		http.Error(w, "network view unavailable", http.StatusInternalServerError)
		return
	}
	// Linked cases: one per ring with a confirmed alert among its members.
	if _, err := a.store.EnsureNetworkCases(r.Context(), scored, 1); err != nil {
		a.logger.WarnContext(r.Context(), "ensure network cases", "error", err)
	}
	cases, err := a.store.ListNetworkCases(r.Context(), 50)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list network cases", "error", err)
		cases = nil
	}
	caseByKey := map[string]store.NetworkCase{}
	for _, c := range cases {
		caseByKey[c.MembersKey] = c
	}
	for i := range scored {
		scored[i].HasCase = caseByKey[scored[i].MembersKeyOf()].Status != ""
	}
	a.renderAdmin(w, "admin_network.html", r, "Trust network", map[string]any{
		"Links":      links,
		"Rings":      scored,
		"Cases":      cases,
		"MinMembers": minMembers,
		"HasLinks":   len(links) > 0,
	})
}

// adminResolveNetworkCase disposes of one linked ring case once for the
// whole member set (resolved, or false_positive when the shared facts
// turned out to be benign — the family browser, the real family business).
func (a *App) adminResolveNetworkCase(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || caseID <= 0 {
		http.Error(w, "invalid case id", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if status != "resolved" && status != "false_positive" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	adminEmail := adminEmailFromContext(r.Context())
	nc, err := a.store.ResolveNetworkCase(r.Context(), caseID, status, adminEmail, note)
	if err != nil {
		a.logger.WarnContext(r.Context(), "resolve network case failed", "case_id", caseID, "error", err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	// Ring-aware enforcement: resolving as confirmed may freeze allowance
	// reservations for every member of the ring in one action (the reviewer
	// ticks the freeze box). Resolving as false_positive instead lifts any
	// freeze previously raised against these members — a mis-tick heals the
	// ring rather than requiring per-member cleanup.
	if status == "resolved" && r.FormValue("freeze") == "on" {
		members, err := a.store.NetworkCaseMembers(r.Context(), nc)
		if err != nil {
			a.logger.WarnContext(r.Context(), "resolve case members", "case_id", nc.ID, "error", err)
		} else {
			for _, m := range members {
				if err := a.store.FreezeAllowance(r.Context(), m.AccountType, m.SubjectID, adminEmail, nc.ID, note); err != nil {
					a.logger.WarnContext(r.Context(), "freeze member", "subject", m.SubjectID, "error", err)
				}
			}
			a.logger.InfoContext(r.Context(), "ring members frozen", "case_id", nc.ID, "members", len(members))
		}
	} else if status == "false_positive" {
		members, err := a.store.NetworkCaseMembers(r.Context(), nc)
		if err == nil {
			for _, m := range members {
				if _, err := a.store.UnfreezeAllowance(r.Context(), m.AccountType, m.SubjectID, adminEmail); err != nil {
					a.logger.WarnContext(r.Context(), "unfreeze member", "subject", m.SubjectID, "error", err)
				}
			}
		}
	}
	a.logger.InfoContext(r.Context(), "network case resolved",
		"case_id", nc.ID, "status", nc.Status, "members", nc.MemberCount)
	http.Redirect(w, r, "/admin/network", http.StatusSeeOther)
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
	// Freeze posture on the profile page: an investigator must see that
	// money movement is blocked for this subject, and can lift it here.
	frozen, err := a.store.SubjectIsFrozen(r.Context(), store.AccountIndividual, userID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "subject freeze check", "error", err)
		frozen = false
	}
	prefs, err := a.store.PaymentPreferencesForUser(r.Context(), userID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "payment preferences", "error", err)
		prefs = nil
	}
	var prefLines []string
	for _, p := range prefs {
		name := service.RailDisplayName(p.Rail)
		if name == "" {
			name = p.Rail
		}
		surface := "chat"
		if p.Channel == store.PreferenceChannelWeb {
			surface = "web"
		}
		if p.Scope == store.PreferenceScopeMerchant && p.ScopeRef != "" {
			prefLines = append(prefLines, name+" — "+surface+" (merchant "+p.ScopeRef+") updated "+p.UpdatedAt.Format("02 Jan 2006 15:04"))
			continue
		}
		prefLines = append(prefLines, name+" — "+surface+" (default) updated "+p.UpdatedAt.Format("02 Jan 2006 15:04"))
	}
	a.renderAdmin(w, "admin_kyc_profile.html", r, "KYC profile", map[string]any{
		"Profile":          profile,
		"UserID":           id,
		"DisplayName":      displayName,
		"Timeline":         timeline,
		"SubjectLinks":     subjectLinks,
		"TimelineCount":    len(timeline),
		"Frozen":           frozen,
		"PaymentPrefs":     prefs,
		"PaymentPrefLines": prefLines,
		"HasPaymentPrefs":  len(prefs) > 0,
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
