package app

import (
	"net/http"

	"whatsapp-payment-demo/internal/store"
)

// screeningCostRates holds the percentage splits shown on the screening-cost
// page. They are computed here rather than in the template so the template
// needs only the shared "percent" helper (also used by the messaging view).
type screeningCostRates struct {
	Clear  float64
	Review float64
	Block  float64
}

func newScreeningCostRates(summary store.OnboardingScreeningCost) screeningCostRates {
	if summary.ScreeningsTotal <= 0 {
		return screeningCostRates{}
	}
	total := float64(summary.ScreeningsTotal)
	return screeningCostRates{
		Clear:  float64(summary.ScreeningsClear) * 100 / total,
		Review: float64(summary.ScreeningsPossible+summary.ScreeningsStrong) * 100 / total,
		Block:  float64(summary.ScreeningsBlocked) * 100 / total,
	}
}

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
		"Rates":   newScreeningCostRates(summary),
	})
}
