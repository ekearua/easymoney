package app

// Spec G8: the annual independent-validation attestation console. The
// checklist shows every validation cycle with its due date, status and
// evidence link; the operator records the validation (scope, findings,
// remediation, evidence URL) once the independent review happens. Every
// mutation is audited with the acting admin's identity.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"whatsapp-payment-demo/internal/store"
)

// adminCompliance renders the attestation checklist, oldest-due first with
// overdue cycles highlighted.
func (a *App) adminCompliance(w http.ResponseWriter, r *http.Request) {
	cycles, err := a.store.ListComplianceAttestations(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "compliance attestation list", "error", err)
		http.Error(w, "attestations unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_compliance.html", r, "Compliance attestations", map[string]any{
		"Cycles": cycles,
		"Now":    time.Now(),
	})
}

// adminComplianceRecord records or updates a cycle's evidence. Status comes
// from the form as one of the three valid values; an empty status keeps the
// stored one so partial updates (findings before submission) are possible.
func (a *App) adminComplianceRecord(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad attestation id", http.StatusBadRequest)
		return
	}
	scope := strings.TrimSpace(r.FormValue("scope"))
	if scope == "" {
		http.Error(w, "scope is required", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	switch status {
	case store.AttestationScheduled, store.AttestationInProgress, store.AttestationSubmitted:
	case "":
		// keep current status below
	default:
		http.Error(w, "status must be scheduled, in_progress or submitted", http.StatusBadRequest)
		return
	}

	due, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("due_date")))
	if err != nil {
		http.Error(w, "due date must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}

	var completedAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("completed_at")); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			http.Error(w, "completed date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		completedAt = &t
	}

	current, err := a.store.ListComplianceAttestations(r.Context())
	if err != nil {
		http.Error(w, "attestations unavailable", http.StatusInternalServerError)
		return
	}
	var existing *store.ComplianceAttestation
	for i := range current {
		if current[i].ID == id {
			existing = &current[i]
			break
		}
	}
	if existing == nil {
		http.Error(w, "attestation not found", http.StatusNotFound)
		return
	}
	if status == "" {
		status = existing.Status
	}
	if status == store.AttestationSubmitted && completedAt == nil && existing.CompletedAt == nil {
		http.Error(w, "a submitted attestation needs the completion date", http.StatusBadRequest)
		return
	}
	if completedAt == nil && existing.CompletedAt != nil {
		completedAt = existing.CompletedAt
	}

	cycle, err := a.store.UpdateComplianceAttestation(r.Context(), id,
		scope,
		strings.TrimSpace(r.FormValue("summary")),
		strings.TrimSpace(r.FormValue("findings")),
		strings.TrimSpace(r.FormValue("remediation")),
		strings.TrimSpace(r.FormValue("evidence_url")),
		status, due, completedAt)
	if err != nil {
		a.logger.WarnContext(r.Context(), "compliance attestation update", "error", err)
		http.Error(w, "could not record attestation", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.attestation_recorded", "compliance_attestation", strconv.Itoa(int(cycle.CycleYear)),
		map[string]any{"status": status, "by": adminEmail, "scope": scope})
	http.Redirect(w, r, "/admin/compliance", http.StatusSeeOther)
}
