package app

// Spec G7 residual: scheduled audit chain-verification with alerting. The
// sweep runs on a worker ticker (AUDIT_CHAIN_VERIFY_INTERVAL, default 1h)
// and is also available on demand at the audit page; a break raises one
// alert with a human disposition (acknowledged bug vs confirmed tampering)
// instead of waiting for an admin to load a page.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// RunAuditChainVerification is the worker tick: verify, persist the sweep,
// and log loudly while a break is live so it reaches the ops channel.
func (a *App) RunAuditChainVerification(ctx context.Context) error {
	check, err := a.store.VerifyAuditChainScheduled(ctx, time.Now())
	if err != nil {
		return err
	}
	if check.BrokenAt >= 0 {
		a.logger.ErrorContext(ctx, "AUDIT CHAIN BROKEN",
			"entry_position", check.BrokenAt,
			"rows_checked", check.RowsChecked,
			"alert_open", check.AlertOpen)
	}
	return nil
}

// adminAuditChainDispose records the reviewer's disposition on the open
// chain alert. The act itself is audited, so the trail stays attributable.
func (a *App) adminAuditChainDispose(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad alert id", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("outcome_note"))
	confirm := r.FormValue("confirm_tampering") == "on"
	disposed, err := a.store.DisposeAuditChainAlert(r.Context(), id, adminEmail, note, confirm)
	if err != nil {
		a.logger.WarnContext(r.Context(), "dispose audit chain alert", "error", err)
		http.Error(w, "could not dispose alert", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.audit_chain_disposed", "audit_chain_alert", strconv.FormatInt(disposed.ID, 10),
		map[string]any{"status": disposed.Status, "by": adminEmail})
	http.Redirect(w, r, "/admin/audit", http.StatusSeeOther)
}
