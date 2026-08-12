// C17: three-way reconciliation admin surface. The /admin/reconciliation page
// lists recent runs (automated daily plus manual weekly), shows any
// discrepancies per run, and lets a compliance officer trigger a manual run.
package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"whatsapp-payment-demo/internal/store"
)

// adminReconciliation renders the run list and the latest run's discrepancies.
func (a *App) adminReconciliation(w http.ResponseWriter, r *http.Request) {
	runs, err := a.store.ListReconciliationRuns(r.Context(), 30)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list reconciliation runs", "error", err)
		http.Error(w, "reconciliation unavailable", http.StatusInternalServerError)
		return
	}
	var items []store.ReconciliationItem
	if len(runs) > 0 {
		items, err = a.store.ReconciliationItemsByRun(r.Context(), runs[0].ID)
		if err != nil {
			a.logger.WarnContext(r.Context(), "reconciliation items", "run_id", runs[0].ID, "error", err)
			http.Error(w, "reconciliation unavailable", http.StatusInternalServerError)
			return
		}
	}
	a.renderAdmin(w, "admin_reconciliation.html", r, "Reconciliation", map[string]any{
		"Runs":  runs,
		"Items": items,
		"Result": r.URL.Query().Get("result"),
		"Error":  r.URL.Query().Get("error"),
	})
}

// adminReconciliationRun triggers a manual three-way run (weekly ritual).
func (a *App) adminReconciliationRun(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())
	run, items, err := a.store.RunReconciliation(r.Context(), "manual", adminEmail)
	if err != nil {
		a.logger.WarnContext(r.Context(), "manual reconciliation failed", "error", err)
		http.Redirect(w, r, "/admin/reconciliation?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.reconciliation.ran", "reconciliation", strconv.FormatInt(run.ID, 10), map[string]any{
		"type": "manual", "discrepancies": len(items), "status": run.Status,
	})
	a.logger.InfoContext(r.Context(), "manual reconciliation complete", "run_id", run.ID, "discrepancies", len(items))
	http.Redirect(w, r, "/admin/reconciliation?result=ran", http.StatusSeeOther)
}

// runReconciliationAuto runs the scheduled daily three-way reconciliation and
// returns whether any discrepancies were found.
func (a *App) runReconciliationAuto(ctx context.Context) (bool, error) {
	run, items, err := a.store.RunReconciliation(ctx, "auto", "system")
	if err != nil {
		return false, err
	}
	if len(items) > 0 {
		a.logger.WarnContext(ctx, "scheduled reconciliation found discrepancies",
			"run_id", run.ID, "discrepancies", len(items), "status", run.Status)
		return true, nil
	}
	a.logger.InfoContext(ctx, "scheduled reconciliation clean", "run_id", run.ID)
	return false, nil
}
