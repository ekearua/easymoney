package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// recipientFilterFromRequest reads the shared geography/verification filters.
func recipientFilterFromRequest(r *http.Request) store.RecipientFilter {
	q := r.URL.Query()
	return store.RecipientFilter{
		State:        strings.TrimSpace(q.Get("state")),
		LGA:          strings.TrimSpace(q.Get("lga")),
		Ward:         strings.TrimSpace(q.Get("ward")),
		PollingUnit:  strings.TrimSpace(q.Get("polling_unit")),
		Verification: strings.TrimSpace(q.Get("verification")),
		Query:        strings.TrimSpace(q.Get("q")),
	}
}

// merchantRecords lists the merchant's recipient records.
func (a *App) merchantRecords(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	filter := recipientFilterFromRequest(r)
	page, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	const pageSize = 100
	rows, err := a.store.ListBulkRecipients(r.Context(), merchantID, filter, pageSize, (page-1)*pageSize)
	if err != nil {
		http.Error(w, "records unavailable", http.StatusInternalServerError)
		return
	}
	count, err := a.store.CountBulkRecipients(r.Context(), merchantID, filter)
	if err != nil {
		http.Error(w, "records unavailable", http.StatusInternalServerError)
		return
	}
	centers, _ := a.store.ListMerchantCenters(r.Context(), merchantID)
	settings, err := a.store.MerchantBulkSettings(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "records unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_records.html", r, "Recipient records", map[string]any{
		"Rows": rows, "Filter": filter, "Count": count, "Centers": centers,
		"LocationEnabled": settings.LocationRequired,
		"Page":            page, "HasNext": page*pageSize < count,
		"Saved": r.URL.Query().Get("saved"), "Error": r.URL.Query().Get("error"),
	})
}

// merchantRecordDetail shows one record with its payment history.
func (a *App) merchantRecordDetail(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid record", http.StatusBadRequest)
		return
	}
	recipient, err := a.store.BulkRecipientByID(r.Context(), merchantID, id)
	if err != nil {
		http.Error(w, "record not found", http.StatusNotFound)
		return
	}
	summary, _ := a.store.RecipientPaymentSummary(r.Context(), merchantID, id)
	history, _ := a.store.RecipientPaymentHistory(r.Context(), merchantID, id, 50)
	centers, _ := a.store.ListMerchantCenters(r.Context(), merchantID)
	settings, err := a.store.MerchantBulkSettings(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "record unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_record_detail.html", r, recipient.DisplayName+" · record", map[string]any{
		"Recipient": recipient, "Summary": summary, "History": history, "Centers": centers,
		"LocationEnabled": settings.LocationRequired,
		"Saved":           r.URL.Query().Get("saved") == "1", "Error": r.URL.Query().Get("error"),
	})
}

// merchantRecordSave creates or edits a record from the form.
func (a *App) merchantRecordSave(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	input := store.BulkRecipientInput{
		PhoneE164:     r.FormValue("phone"),
		DisplayName:   r.FormValue("name"),
		BVN:           r.FormValue("bvn"),
		NIN:           r.FormValue("nin"),
		VIN:           r.FormValue("vin"),
		AccountNumber: r.FormValue("account_number"),
		BankName:      r.FormValue("bank_name"),
		BankCode:      r.FormValue("bank_code"),
		AccountName:   r.FormValue("account_name"),
		State:         r.FormValue("state"),
		LGA:           r.FormValue("lga"),
		Ward:          r.FormValue("ward"),
		PollingUnit:   r.FormValue("polling_unit"),
		SourceFile:    r.FormValue("source_file"),
		Source:        "manual",
	}
	if centerID := strings.TrimSpace(r.FormValue("center_id")); centerID != "" && a.recordLocationEnabled(r, merchantID) {
		if parsed, err := uuid.Parse(centerID); err == nil {
			input.CenterID = &parsed
		}
	}
	recipient, err := a.store.UpsertBulkRecipient(r.Context(), merchantID, input)
	if err != nil {
		http.Error(w, "could not save record: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.audit(r, "merchant.records.saved", "bulk_recipient", recipient.ID.String(), map[string]any{
		"state": recipient.State, "lga": recipient.LGA,
	})
	http.Redirect(w, r, "/merchant/records/"+recipient.ID.String()+"?saved=1", http.StatusSeeOther)
}

// merchantRecordVerify records the records role's manual attestation.
func (a *App) merchantRecordVerify(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid record", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if status != store.RecipientVerified && status != store.RecipientRejected && status != store.RecipientUnverified {
		http.Error(w, "invalid verification status", http.StatusBadRequest)
		return
	}
	if err := a.store.SetBulkRecipientVerification(r.Context(), merchantID, id,
		merchantMemberIDFromContext(r.Context()), status, r.FormValue("note")); err != nil {
		http.Error(w, "could not save verification", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.records.verification_updated", "bulk_recipient", id.String(), map[string]any{"status": status})
	http.Redirect(w, r, "/merchant/records/"+id.String()+"?saved=1", http.StatusSeeOther)
}

// merchantRecordDelete removes a record for non-owner roles; owner keeps records.
func (a *App) merchantRecordDelete(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	role := merchantRoleFromContext(r.Context())
	if role == "owner" {
		http.Redirect(w, r, "/merchant/records", http.StatusSeeOther)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid record", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteBulkRecipient(r.Context(), merchantID, id); err != nil {
		http.Error(w, "could not delete record", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.records.deleted", "bulk_recipient", id.String(), nil)
	http.Redirect(w, r, "/merchant/records", http.StatusSeeOther)
}

// merchantRecordImport parses an uploaded Records Template file.
func (a *App) merchantRecordImport(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/merchant/records?error=Choose+a+CSV+or+XLSX+file", http.StatusSeeOther)
		return
	}
	defer file.Close()
	var centerID *uuid.UUID
	if raw := strings.TrimSpace(r.FormValue("center_id")); raw != "" && a.recordLocationEnabled(r, merchantID) {
		if parsed, err := uuid.Parse(raw); err == nil {
			centerID = &parsed
		}
	}
	rows, problems := parseRecordsFile(file, header.Filename, centerID, header.Filename)
	if len(rows) == 0 {
		http.Redirect(w, r, "/merchant/records?error=No+usable+rows+found+in+that+file", http.StatusSeeOther)
		return
	}
	saved := 0
	for i := range rows {
		if _, err := a.store.UpsertBulkRecipient(r.Context(), merchantID, rows[i]); err != nil {
			problems = append(problems, "Row "+strconv.Itoa(i+1)+": "+err.Error())
			continue
		}
		saved++
	}
	a.audit(r, "merchant.records.imported", "bulk_recipient", header.Filename, map[string]any{
		"saved": saved, "file": header.Filename, "problems": len(problems),
	})
	if len(problems) > 0 {
		http.Redirect(w, r, "/merchant/records?saved="+strconv.Itoa(saved)+"&error="+url.QueryEscape(strings.Join(problems[:minInt(len(problems), 3)], "; ")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/merchant/records?saved="+strconv.Itoa(saved), http.StatusSeeOther)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// recordLocationEnabled reports whether location assignment belongs in the
// recipient records at all. A merchant created without the location switch
// keeps no location columns on its records, so a submitted centre is ignored.
func (a *App) recordLocationEnabled(r *http.Request, merchantID uuid.UUID) bool {
	settings, err := a.store.MerchantBulkSettings(r.Context(), merchantID)
	if err != nil {
		return false
	}
	return settings.LocationRequired
}
