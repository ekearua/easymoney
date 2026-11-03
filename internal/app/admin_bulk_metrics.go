package app

import (
	"net/http"
)

// adminBulkMetrics shows bulk payment metrics and tracking across merchants.
func (a *App) adminBulkMetrics(w http.ResponseWriter, r *http.Request) {
	overview, err := a.store.GetAdminBulkOverview(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "load bulk overview failed", "error", err)
	}
	merchants, err := a.store.ListAdminBulkMerchants(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "load bulk merchants failed", "error", err)
	}
	batches, err := a.store.ListAdminBulkBatches(r.Context(), 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "load bulk batches failed", "error", err)
	}
	a.renderAdmin(w, "admin_bulk_metrics.html", r, "Bulk Metrics", map[string]any{
		"Overview":  overview,
		"Merchants": merchants,
		"Batches":   batches,
	})
}
