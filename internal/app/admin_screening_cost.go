package app

import "net/http"

// adminScreeningCost renders an operations tracking page for onboarding and
// screening activity: customer screening results (with decision split), adverse
// media screenings and payment-time screenings over the configurable window.
// An optional per-check estimate is shown when non-zero, so the cost column
// remains a display-only meter in line with the messaging cost view.
func (a *App) adminScreeningCost(w http.ResponseWriter, r *http.Request) {
	const windowDays = 30
	costPer := a.cfg.ScreeningCostNGN
	summary, err := a.store.OnboardingScreeningCostSummary(r.Context(), windowDays, costPer)
	if err != nil {
		a.logger.WarnContext(r.Context(), "screening cost summary failed", "error", err)
		http.Error(w, "could not load screening cost summary", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_screening_cost.html", r, "Onboarding & screening cost", map[string]any{
		"Summary": summary,
	})
}
