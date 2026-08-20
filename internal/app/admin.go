package app

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
)

func (a *App) adminMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := a.store.Metrics(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "metrics.html", r, "Metrics", map[string]any{"Metrics": metrics, "TOTPEnabled": a.cfg.TOTPEnabled})
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "users.html", r, "Users", map[string]any{"Users": users})
}

func (a *App) adminMerchants(w http.ResponseWriter, r *http.Request) {
	merchants, err := a.store.ListMerchants(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	registrations, err := a.store.ListMerchantRegistrations(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "merchants.html", r, "Merchants", map[string]any{"Merchants": merchants, "Registrations": registrations})
}

func (a *App) adminAdmins(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListAdminUsers(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_users.html", r, "Admin accounts", map[string]any{"Admins": users, "CurrentAdminID": adminIDFromContext(r.Context())})
}

// adminAuditLog renders the tamper-evident audit trail with chain status.
func (a *App) adminAuditLog(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListAuditLogs(r.Context(), 200)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	count, broken, err := a.store.VerifyAuditChain(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_audit.html", r, "Audit log", map[string]any{
		"Entries":     entries,
		"ChainCount":  count,
		"ChainBroken": broken,
	})
}

// adminArchive renders the append-only archive of records removed by retention.
func (a *App) adminArchive(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListArchiveLedger(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list archive ledger", "error", err)
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_archive.html", r, "Retention archive", map[string]any{"Entries": entries})
}

// adminLegalHolds lists retention-exempt subjects (C29).
func (a *App) adminLegalHolds(w http.ResponseWriter, r *http.Request) {
	holds, err := a.store.ListLegalHolds(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "list legal holds", "error", err)
		http.Error(w, "legal holds unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_legal_holds.html", r, "Legal holds", map[string]any{"Holds": holds})
}

// adminAddLegalHold freezes a subject so retention never purges it.
func (a *App) adminAddLegalHold(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	subjectType := strings.TrimSpace(r.FormValue("subject_type"))
	subjectID := strings.TrimSpace(r.FormValue("subject_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	validTypes := map[string]bool{"user": true, "payment": true, "invoice": true, "thrift_group": true}
	if !validTypes[subjectType] || subjectID == "" || reason == "" {
		http.Error(w, "subject type, subject id and reason are required", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("expires_at")); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			http.Error(w, "expiry must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		expiresAt = &parsed
	}
	adminEmail := adminEmailFromContext(r.Context())
	if err := a.store.AddLegalHold(r.Context(), subjectType, subjectID, reason, adminEmail, expiresAt); err != nil {
		a.logger.WarnContext(r.Context(), "add legal hold failed", "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.legal_holds.created", subjectType, subjectID, map[string]any{"reason": reason, "expires_at": expiresAt})
	a.logger.InfoContext(r.Context(), "legal hold added", "subject_type", subjectType, "subject_id", subjectID)
	http.Redirect(w, r, "/admin/legal-holds", http.StatusSeeOther)
}

// adminRemoveLegalHold clears a freeze.
func (a *App) adminRemoveLegalHold(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	subjectType := strings.TrimSpace(r.FormValue("subject_type"))
	subjectID := strings.TrimSpace(r.FormValue("subject_id"))
	if subjectType == "" || subjectID == "" {
		http.Error(w, "subject type and id are required", http.StatusBadRequest)
		return
	}
	if err := a.store.RemoveLegalHold(r.Context(), subjectType, subjectID); err != nil {
		a.logger.WarnContext(r.Context(), "remove legal hold failed", "error", err)
		http.Error(w, "remove failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.legal_holds.removed", subjectType, subjectID, map[string]any{})
	http.Redirect(w, r, "/admin/legal-holds", http.StatusSeeOther)
}

// adminKYC renders the customer identity ladder and the pending review queue.
func (a *App) adminKYC(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.store.ListKYCProfiles(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list kyc profiles", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	cases, err := a.store.ListManualReviewCases(r.Context(), "", 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list review cases", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	alerts, err := a.store.ListTransactionAlerts(r.Context(), "", 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list transaction alerts", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	byTier := map[string]int{"L0": 0, "L1": 0, "L2": 0, "L3": 0, "L4": 0}
	for _, p := range profiles {
		byTier[p.Tier]++
	}
	a.renderAdmin(w, "admin_kyc.html", r, "KYC ladder", map[string]any{
		"Profiles": profiles,
		"Cases":    cases,
		"Alerts":   alerts,
		"ByTier":   byTier,
	})
}

// adminResolveTransactionAlert moves a monitoring alert to a terminal status
// (acknowledged / escalated / resolved) and records the reviewer.
func (a *App) adminResolveTransactionAlert(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	alertID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || alertID <= 0 {
		http.Error(w, "invalid alert id", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	adminEmail := adminEmailFromContext(r.Context())
	alert, err := a.store.ResolveTransactionAlert(r.Context(), alertID, status, adminEmail)
	if err != nil {
		a.logger.WarnContext(r.Context(), "resolve transaction alert failed", "alert_id", alertID, "error", err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "transaction alert resolved", "alert_id", alert.ID, "status", alert.Status)
	http.Redirect(w, r, "/admin/kyc", http.StatusSeeOther)
}

// adminKYCReview approves or rejects a manual review case.
func (a *App) adminKYCReview(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || caseID <= 0 {
		http.Error(w, "invalid case id", http.StatusBadRequest)
		return
	}
	approve := strings.TrimSpace(r.FormValue("decision")) == "approve"
	note := strings.TrimSpace(r.FormValue("note"))
	adminID := adminIDFromContext(r.Context())
	adminEmail := adminEmailFromContext(r.Context())
	actor := &store.AuditLog{
		ActorType:  "admin",
		ActorID:    uuid.NullUUID{UUID: adminID, Valid: true},
		ActorEmail: sql.NullString{String: adminEmail, Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
	}
	c, err := a.store.ReviewManualReviewCase(r.Context(), caseID, approve, adminEmail, note, actor)
	if err != nil {
		a.logger.WarnContext(r.Context(), "review kyc case failed", "case_id", caseID, "error", err)
		http.Error(w, "review failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "kyc case reviewed", "case_id", c.ID, "status", c.Status, "user_id", c.UserID.String())
	http.Redirect(w, r, "/admin/kyc", http.StatusSeeOther)
}

func (a *App) adminCreateAdmin(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")
	if email == "" || password == "" || !validAdminRole(role) {
		http.Error(w, "email, password and role are required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateAdminUser(r.Context(), email, string(hash), role); err != nil {
		a.logger.WarnContext(r.Context(), "create admin user failed", "email", email, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.created", "admin_user", email, map[string]any{"role": role})
	a.logger.InfoContext(r.Context(), "admin user created", "email", email, "role", role)
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminUpdateAdminRole(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if !validAdminRole(role) {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if id == adminIDFromContext(r.Context()) {
		http.Error(w, "you cannot change your own role", http.StatusForbidden)
		return
	}
	admin, err := a.store.AdminUserByID(r.Context(), id)
	if err != nil || admin == nil {
		http.Error(w, "admin not found", http.StatusNotFound)
		return
	}
	oldRole := admin.Role
	if err := a.store.UpdateAdminUserRole(r.Context(), id, role); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.role_changed", "admin_user", id.String(), map[string]any{"old_role": oldRole, "new_role": role})
	_ = a.store.DeleteAdminSessionsByUser(r.Context(), id)
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminToggleAdminEnabled(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	if id == adminIDFromContext(r.Context()) {
		http.Error(w, "you cannot disable your own account", http.StatusForbidden)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if err := a.store.SetAdminUserEnabled(r.Context(), id, enabled); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.enabled_toggled", "admin_user", id.String(), map[string]any{"enabled": enabled})
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminResetAdminPassword(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateAdminUserPassword(r.Context(), id, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.password_reset", "admin_user", id.String(), nil)
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func validAdminRole(role string) bool {
	switch role {
	case store.RoleAdmin, store.RoleCompliance, store.RoleSupport, store.RoleReadOnly:
		return true
	}
	return false
}

func (a *App) adminApproveMerchantRegistration(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid registration id", http.StatusBadRequest)
		return
	}
	merchant, err := a.store.ApproveMerchantRegistration(r.Context(), id)
	if err != nil {
		a.logger.WarnContext(r.Context(), "approve merchant registration failed", "registration_id", id, "error", err)
		http.Error(w, "approval failed", http.StatusInternalServerError)
		return
	}
	owner, err := a.store.MerchantOwnerByRegistrationID(r.Context(), id)
	if err == nil {
		var setPasswordURL string
		token, err := randomToken(32)
		if err == nil {
			if err := a.store.CreateMerchantPasswordResetToken(r.Context(), merchant.ID, owner.ID, token, time.Now().Add(72*time.Hour)); err == nil {
				setPasswordURL = a.cfg.BaseURL + "/merchant/set-password?token=" + token
			}
		}
		a.conversation.NotifyMerchantApproved(r.Context(), owner, merchant.Name, setPasswordURL)
	}
	a.audit(r, "admin.merchants.approved", "merchant", merchant.ID.String(), map[string]any{"name": merchant.Name, "registration_id": id.String()})
	http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
}

func (a *App) adminSetMerchantPassword(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	password := strings.TrimSpace(r.FormValue("password"))
	if password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateMerchantPassword(r.Context(), id, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.password_reset", "merchant", id.String(), nil)
	http.Redirect(w, r, "/admin/merchants?password_set=1", http.StatusSeeOther)
}

func (a *App) adminSetMerchantPaymentTerms(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	allowPartial := r.FormValue("allow_partial_payments") == "on"
	minInvoiceKobo, _ := strconv.ParseInt(r.FormValue("min_invoice_amount_kobo"), 10, 64)
	upfrontPct, _ := strconv.Atoi(r.FormValue("upfront_percent"))
	minInstallPct, _ := strconv.Atoi(r.FormValue("min_installment_percent"))
	maxInstallments, _ := strconv.Atoi(r.FormValue("max_installments"))
	allowFullAlways := r.FormValue("allow_full_pay_always") == "on"
	if err := a.store.UpdateMerchantPaymentTerms(r.Context(), id, allowPartial, minInvoiceKobo, upfrontPct, minInstallPct, maxInstallments, allowFullAlways); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.payment_terms_updated", "merchant", id.String(), map[string]any{
		"allow_partial": allowPartial, "min_invoice_kobo": minInvoiceKobo, "upfront_percent": upfrontPct,
		"min_installment_percent": minInstallPct, "max_installments": maxInstallments, "allow_full_always": allowFullAlways,
	})
	http.Redirect(w, r, "/admin/merchants?terms_set=1", http.StatusSeeOther)
}

func (a *App) adminSetPhoneWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	whitelist := strings.TrimSpace(r.FormValue("phone_whitelist"))
	if err := a.store.UpdateServicePhoneWhitelist(r.Context(), id, whitelist); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.scanning.whitelist_updated", "service", id.String(), map[string]any{"whitelist": whitelist})
	http.Redirect(w, r, "/admin/scanning?whitelist_set=1", http.StatusSeeOther)
}

func (a *App) adminPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := a.store.ListPayments(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "payments.html", r, "Payments", map[string]any{"Payments": payments})
}

func (a *App) adminDataOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.store.ListDataOrders(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	smsRequests, err := a.store.ListSMSRequests(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "data_orders.html", r, "Data Orders", map[string]any{"Orders": orders, "SMSRequests": smsRequests})
}

func (a *App) adminThrift(w http.ResponseWriter, r *http.Request) {
	groups, err := a.store.ListThriftGroups(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	contributions, err := a.store.ListThriftContributions(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	payouts, err := a.store.ListThriftPayouts(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "thrift.html", r, "Thrift", map[string]any{"Groups": groups, "Contributions": contributions, "Payouts": payouts})
}

func (a *App) adminCompleteThriftPayout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid payout id", http.StatusBadRequest)
		return
	}
	if err := a.store.MarkThriftPayoutCompleted(r.Context(), id); err != nil {
		a.logger.WarnContext(r.Context(), "complete thrift payout failed", "payout_id", id, "error", err)
		http.Error(w, "payout completion failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.thrift.payout_completed", "thrift_payout", id.String(), nil)
	http.Redirect(w, r, "/admin/thrift", http.StatusSeeOther)
}

func (a *App) adminAcceptedNumbers(w http.ResponseWriter, r *http.Request) {
	numbers := a.conversation.AcceptedInvoiceNumbers()
	a.renderAdmin(w, "accepted_numbers.html", r, "Accepted Invoice Numbers", map[string]any{"Numbers": numbers})
}

func (a *App) adminUpdateAcceptedNumbers(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	raw := r.FormValue("numbers")
	var numbers []string
	for _, s := range strings.Split(raw, "\n") {
		s = strings.TrimSpace(s)
		if s != "" {
			numbers = append(numbers, s)
		}
	}
	a.conversation.SetAcceptedInvoiceNumbers(numbers)
	a.audit(r, "admin.scanning.accepted_numbers_updated", "config", "accepted_invoice_numbers", map[string]any{"count": len(numbers)})
	http.Redirect(w, r, "/admin/accepted-numbers", http.StatusSeeOther)
}

func (a *App) adminScanning(w http.ResponseWriter, r *http.Request) {
	a.renderScanningAdmin(w, r, nil)
}

func (a *App) adminCreateScanningService(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ttlHours, _ := strconv.Atoi(r.FormValue("ttl_hours"))
	if ttlHours <= 0 {
		ttlHours = 24
	}
	var merchantID uuid.NullUUID
	if raw := strings.TrimSpace(r.FormValue("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid merchant", http.StatusBadRequest)
			return
		}
		merchantID = uuid.NullUUID{UUID: id, Valid: true}
	}
	_, err := a.store.CreateRegisteredService(
		r.Context(),
		r.FormValue("name"),
		r.FormValue("service_type"),
		merchantID,
		r.FormValue("accepted_receipt_types"),
		ttlHours*3600,
		r.FormValue("active") == "on",
	)
	if err != nil {
		a.renderScanningAdmin(w, r, map[string]any{"Error": "Could not save registered service."})
		return
	}
	a.audit(r, "admin.scanning.service_created", "service", r.FormValue("name"), map[string]any{"service_type": r.FormValue("service_type")})
	http.Redirect(w, r, "/admin/scanning", http.StatusSeeOther)
}

func (a *App) adminCreateServiceReader(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(r.FormValue("service_id")))
	if err != nil {
		http.Error(w, "invalid service", http.StatusBadRequest)
		return
	}
	reader, key, err := a.store.CreateServiceReader(r.Context(), serviceID, r.FormValue("name"))
	if err != nil {
		a.renderScanningAdmin(w, r, map[string]any{"Error": "Could not create reader."})
		return
	}
	a.audit(r, "admin.scanning.reader_created", "service_reader", reader.ID.String(), map[string]any{"name": r.FormValue("name")})
	a.renderScanningAdmin(w, r, map[string]any{"NewReader": reader, "NewReaderKey": key})
}

func (a *App) renderScanningAdmin(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	services, err := a.store.ListRegisteredServices(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	readers, err := a.store.ListServiceReaders(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	tokens, err := a.store.ListReceiptScanTokens(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	attempts, err := a.store.ListReceiptScanAttempts(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	merchants, err := a.store.ListMerchants(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Services": services, "Readers": readers, "Tokens": tokens,
		"Attempts": attempts, "Merchants": merchants,
	}
	for key, value := range extra {
		data[key] = value
	}
	a.renderAdmin(w, "scanning.html", r, "Receipt scanning", data)
}

func (a *App) adminWebhooks(w http.ResponseWriter, r *http.Request) {
	webhooks, err := a.store.ListWebhooks(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "webhooks.html", r, "Webhooks", map[string]any{"Webhooks": webhooks})
}

func (a *App) adminDeadLetter(w http.ResponseWriter, r *http.Request) {
	webhooks, err := a.store.ListFailedWebhooks(r.Context(), 200)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	events, err := a.store.ListFailedBusinessEvents(r.Context(), 200)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "dead_letter.html", r, "Dead-Letter Queue", map[string]any{"Webhooks": webhooks, "Events": events})
}

func (a *App) adminDeadLetterReplay(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "webhook":
		if err := a.store.ReplayDeadLetter(r.Context(), id); err != nil {
			http.Error(w, "replay failed", http.StatusInternalServerError)
			return
		}
	case "event":
		if err := a.store.ReplayBusinessEvent(r.Context(), id); err != nil {
			http.Error(w, "replay failed", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	a.audit(r, "admin.dead_letter.replay", "dead_letter", strconv.FormatInt(id, 10), map[string]any{"kind": kind, "id": id})
	http.Redirect(w, r, "/admin/dead-letter", http.StatusSeeOther)
}
