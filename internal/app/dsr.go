// C20: NDPR data-subject rights admin surface. Compliance staff can answer an
// access request with a full JSON export of a user's data, create access or
// erasure requests, and complete/reject them. Erasure archives a snapshot to
// the append-only ledger, releases the cooling-off hold, and deletes the user.
package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminDataSubjects renders the data-subject rights dashboard: the request
// queue, a customer lookup, and (when a user is selected) their consent
// records and data inventory with export and request actions.
func (a *App) adminDataSubjects(w http.ResponseWriter, r *http.Request) {
	requests, err := a.store.ListDataSubjectRequests(r.Context(), "", 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list data subject requests", "error", err)
		http.Error(w, "data subject dashboard unavailable", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Requests": requests,
		"Lookup":   "",
		"Found":    nil,
		"Consents": nil,
		"Export":   nil,
		"Counts":   map[string]int{},
	}

	if phone := strings.TrimSpace(r.URL.Query().Get("lookup")); phone != "" {
		data["Lookup"] = phone
		user, err := a.store.FindUserByPhone(r.Context(), phone)
		if err != nil {
			a.logger.WarnContext(r.Context(), "data subject lookup", "error", err)
		} else if user.ID != uuid.Nil {
			data["Found"] = user
			a.fillDataSubject(r, user, data)
		}
	}
	if rawID := strings.TrimSpace(r.URL.Query().Get("user_id")); rawID != "" {
		if id, err := uuid.Parse(rawID); err == nil {
			if user, err := a.store.GetUserByID(r.Context(), id); err == nil && user.ID != uuid.Nil {
				data["Found"] = user
				a.fillDataSubject(r, user, data)
			}
		}
	}

	a.renderAdmin(w, "admin_dsr.html", r, "Data subject rights", data)
}

// fillDataSubject loads consents and the export inventory for a selected user.
func (a *App) fillDataSubject(r *http.Request, user store.User, data map[string]any) {
	ctx := r.Context()
	consents, err := a.store.ListConsents(ctx, user.ID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list consents", "error", err)
	} else {
		data["Consents"] = consents
	}
	export, err := a.store.ExportUserData(ctx, user.ID)
	if err != nil {
		a.logger.WarnContext(r.Context(), "export data subject", "error", err)
		data["ExportError"] = err.Error()
		return
	}
	data["Export"] = export
	counts := map[string]int{
		"Payments":     jsonArrayLen(export.Payments),
		"DataOrders":   jsonArrayLen(export.DataOrders),
		"Invoices":     jsonArrayLen(export.Invoices),
		"ThriftGroups": jsonArrayLen(export.ThriftGroups),
		"Screening":    jsonArrayLen(export.Screening),
		"Alerts":       jsonArrayLen(export.TransactionAlerts),
	}
	data["Counts"] = counts
}

func jsonArrayLen(raw json.RawMessage) int {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// adminDataSubjectExport streams a user's complete data inventory as JSON
// (NDPR right of access / data portability).
func (a *App) adminDataSubjectExport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("user_id")))
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	export, err := a.store.ExportUserData(r.Context(), id)
	if err != nil {
		a.logger.WarnContext(r.Context(), "export data subject", "user_id", id, "error", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="user-data-%s.json"`, id.String()))
	writeJSON(w, http.StatusOK, export)
}

// adminDataSubjectRequest creates an access or erasure request. Erasure
// requests place a 30-day cooling-off legal hold on the user.
func (a *App) adminDataSubjectRequest(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(r.FormValue("user_id")))
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	requestType := strings.TrimSpace(r.FormValue("request_type"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	adminEmail := adminEmailFromContext(r.Context())
	req, err := a.store.CreateDataSubjectRequest(r.Context(), userID, requestType, reason, adminEmail)
	if err != nil {
		a.logger.WarnContext(r.Context(), "create data subject request", "user_id", userID, "error", err)
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "data subject request created", "request_id", req.ID, "type", req.RequestType, "user_id", userID.String())
	http.Redirect(w, r, "/admin/data-subjects?user_id="+userID.String(), http.StatusSeeOther)
}

// adminDataSubjectResolve completes or rejects a request. Completing an
// erasure archives the user's data and deletes the account.
func (a *App) adminDataSubjectResolve(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	requestID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || requestID <= 0 {
		http.Error(w, "invalid request id", http.StatusBadRequest)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())
	note := strings.TrimSpace(r.FormValue("note"))
	decision := strings.TrimSpace(r.FormValue("decision")) // "complete" | "reject"

	req, err := a.store.GetDataSubjectRequest(r.Context(), requestID)
	if err != nil {
		http.Error(w, "request not found", http.StatusNotFound)
		return
	}

	if decision == "complete" {
		_, err = a.store.CompleteDataSubjectRequest(r.Context(), requestID, adminEmail, note)
	} else {
		_, err = a.store.RejectDataSubjectRequest(r.Context(), requestID, adminEmail, note)
	}
	if err != nil {
		a.logger.WarnContext(r.Context(), "resolve data subject request", "request_id", requestID, "decision", decision, "error", err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "data subject request resolved", "request_id", requestID, "decision", decision, "user_id", req.UserID.String())
	http.Redirect(w, r, "/admin/data-subjects?user_id="+req.UserID.String(), http.StatusSeeOther)
}
