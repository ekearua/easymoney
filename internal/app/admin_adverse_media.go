package app

// Spec gap G9: the adverse-media screening queue. Screenings are recorded
// during onboarding and rescreen; a non-clear determination opens an
// 'adverse_media' case on the KYC review queue, which is where the disposition
// happens. This page is a read-only view of the adverse-media lane.

import (
	"net/http"
)

// adminAdverseMedia renders recent adverse-media screenings (newest first).
func (a *App) adminAdverseMedia(w http.ResponseWriter, r *http.Request) {
	screenings, err := a.store.ListAdverseMediaScreenings(r.Context(), 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "adverse-media screenings list", "error", err)
		http.Error(w, "adverse-media queue unavailable", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "adverse-media queue rendered", "count", len(screenings))
	a.renderAdmin(w, "admin_adverse_media.html", r, "Adverse media", map[string]any{
		"Screenings": screenings,
	})
}
