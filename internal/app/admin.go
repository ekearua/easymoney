package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"net"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

func (a *App) adminMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := a.store.Metrics(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	// The dashboard surfaces the same 7-day AI extraction spend the media
	// report charts, so the operations headline carries the cost without a
	// detour: tokens summed across both rails, priced with AI_TOKENS_PER_NGN.
	tokenDays, err := a.store.DailyTokens(r.Context(), 7)
	if err != nil {
		a.logger.WarnContext(r.Context(), "daily token stats failed", "error", err)
		// The dashboard still renders; the tile just shows the zero state.
	}
	var weekTokens int64
	for _, day := range tokenDays {
		weekTokens += day.Tokens
	}
	// Kobo math keeps sub-naira weeks honest (100 tokens at 250/₦ ≈ ₦0.40);
	// the money template func takes kobo like everywhere else.
	var spendKobo int64
	if a.cfg.AITokensPerNGN > 0 {
		spendKobo = weekTokens * 100 / a.cfg.AITokensPerNGN
	}
	// M5 intelligence KPIs: false-positive rate, resolution speed, signal
	// coverage and the risk-band mix, straight from the Trust Fabric plan.
	kpis, err := a.store.TrustFabricKPIs(r.Context(), 7*24*time.Hour)
	if err != nil {
		a.logger.WarnContext(r.Context(), "trust KPI stats failed", "error", err)
		// The dashboard still renders; the tile just shows the zero state.
	}
	resolutionLabel := "—"
	if mins := kpis.MedianAlertResolutionMins; mins > 0 {
		if mins >= 1 {
			resolutionLabel = fmt.Sprintf("%.0f min", mins)
		} else {
			resolutionLabel = fmt.Sprintf("%.0f sec", mins*60)
		}
	}
	data := a.securitySummaryData(r.Context(), store.MFAScopeAdmin, adminIDFromContext(r.Context()))
	data["Metrics"] = metrics
	data["AITokens7d"] = weekTokens
	data["AITokensPerNGN"] = a.cfg.AITokensPerNGN
	data["AISpendKobo"] = spendKobo
	data["TrustKPIs"] = kpis
	data["MedianResolutionLabel"] = resolutionLabel
	// Backup/PITR health: age the scheduler's heartbeats against the
	// staleness thresholds so a dead WAL ship or missed drill is visible
	// here, not at restore time. Failure degrades the panel, never the
	// page — an operator still needs the rest of the dashboard when the
	// backup table itself is unreachable.
	backupHealth, err := a.store.BackupHealth(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "backup health", "error", err)
	}
	backupPanel := buildBackupPanel(backupHealth, time.Now(),
		a.cfg.BackupBaseStaleAfter, a.cfg.WALShipStaleAfter, a.cfg.RestoreDrillStaleAfter)
	if backupHealth.LastBaseBackupAt == nil && backupHealth.LastWALShipAt == nil && backupHealth.ArchiverUnavailable == "" {
		// First boot / harness: no heartbeats recorded yet. The panel still
		// renders (everything "never"), but the page-level nudge says why.
		data["BackupNeverRun"] = true
	}
	data["BackupPanel"] = backupPanel
	// Post-deploy synthetic checks: the last rollout's stages plus the trailing
	// pass rate, and the outbox backlog the checks assert on. A store failure
	// degrades the panel, never the page.
	syntheticSummary, err := a.store.SyntheticCheckSummary(r.Context(), 7*24*time.Hour)
	if err != nil {
		a.logger.WarnContext(r.Context(), "synthetic check stats failed", "error", err)
	}
	outboxBacklog, err := a.store.OutboxBacklog(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "outbox backlog stats failed", "error", err)
	}
	data["SyntheticPanel"] = buildSyntheticPanel(syntheticSummary, outboxBacklog, time.Now(), a.cfg.SyntheticOutboxMaxAge)
	// Saved-methods evidence (docs/payments/SAVED_CARD_METHODS_PLAN.md Phase 0.5): the figures
	// behind the card-on-file decision, so it rests on the funnel rather than on
	// how annoying re-entering card details sounds. A store failure degrades the
	// panel, never the page.
	const reentryWindow = 30 * 24 * time.Hour
	reentryMetrics, err := a.store.CardReentryMetricsSince(r.Context(), time.Now().Add(-reentryWindow))
	if err != nil {
		a.logger.WarnContext(r.Context(), "card re-entry metrics failed", "error", err)
	}
	reentryByMerchant, err := a.store.CardReentryMetricsByMerchant(r.Context(), time.Now().Add(-reentryWindow), 8)
	if err != nil {
		a.logger.WarnContext(r.Context(), "card re-entry metrics by merchant failed", "error", err)
	}
	data["ReentryPanel"] = buildReentryPanel(reentryMetrics, reentryByMerchant, reentryWindow)
	a.renderAdmin(w, "metrics.html", r, "Metrics", data)
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
	bulkEnabled, err := a.store.ListMerchantBulkPayments(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	members, err := a.store.MerchantMembersByMerchant(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	settings := make(map[uuid.UUID]store.MerchantBulkSettings, len(merchants))
	for _, merchant := range merchants {
		if value, serr := a.store.MerchantBulkSettings(r.Context(), merchant.ID); serr == nil {
			settings[merchant.ID] = value
		}
	}
	inviteLink := ""
	if token := strings.TrimSpace(r.URL.Query().Get("invite")); token != "" {
		inviteLink = strings.TrimRight(a.cfg.BaseURL, "/") + "/merchant/invite?token=" + token
	}
	data := map[string]any{
		"Merchants": merchants, "Registrations": registrations, "BulkEnabled": bulkEnabled,
		"Members": members, "Settings": settings, "InviteLink": inviteLink,
		"Error": r.URL.Query().Get("error"),
	}
	if merchantCreated := r.URL.Query().Get("merchant_created"); merchantCreated == "1" {
		data["MerchantCreated"] = true
		if resetURL := r.URL.Query().Get("reset_url"); resetURL != "" {
			data["ResetURL"] = resetURL
		}
		if emailError := r.URL.Query().Get("email_error"); emailError != "" {
			data["EmailError"] = emailError
		}
	}
	if passwordSet := r.URL.Query().Get("password_set"); passwordSet == "1" {
		data["PasswordSet"] = true
	}
	if termsSet := r.URL.Query().Get("terms_set"); termsSet == "1" {
		data["TermsSet"] = true
	}
	a.renderAdmin(w, "merchants.html", r, "Merchants", data)
}

func (a *App) adminMerchantCreateForm(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list users for merchant create failed", "error", err)
	}
	a.renderAdmin(w, "admin_merchant_create.html", r, "Create Merchant", map[string]any{"Users": users})
}

func (a *App) adminCreateMerchantDirect(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "business name required", http.StatusBadRequest)
		return
	}

	var ownerUserID *uuid.UUID
	if ownerIDStr := strings.TrimSpace(r.FormValue("owner_user_id")); ownerIDStr != "" {
		if id, err := uuid.Parse(ownerIDStr); err == nil {
			ownerUserID = &id
		}
	}

	allowPartial := r.FormValue("allow_partial_payments") == "on"
	minInvoiceKobo, _ := strconv.ParseInt(r.FormValue("min_invoice_amount_kobo"), 10, 64)
	upfrontPct, _ := strconv.Atoi(r.FormValue("upfront_percent"))
	minInstallPct, _ := strconv.Atoi(r.FormValue("min_installment_percent"))
	maxInstallments, _ := strconv.Atoi(r.FormValue("max_installments"))
	allowFullAlways := r.FormValue("allow_full_pay_always") == "on"
	isSponsor := r.FormValue("is_sponsor") == "on"
	createScanning := r.FormValue("create_scanning_service") != "off" // default true
	if r.FormValue("create_scanning_service") == "" {
		createScanning = true
	}
	active := r.FormValue("active") != "off"
	if r.FormValue("active") == "" {
		active = true
	}

	sendReset := r.FormValue("send_password_reset") == "on" || r.FormValue("send_password_reset") == "1"
	if r.FormValue("send_password_reset") == "" {
		sendReset = ownerUserID == nil // send reset if creating new owner
	}

	params := store.CreateMerchantDirectParams{
		Name:                  name,
		Slug:                  strings.TrimSpace(r.FormValue("slug")),
		Category:              strings.TrimSpace(r.FormValue("category")),
		Description:           strings.TrimSpace(r.FormValue("description")),
		LogoURL:               strings.TrimSpace(r.FormValue("logo_url")),
		Active:                active,
		OwnerUserID:           ownerUserID,
		OwnerEmail:            strings.TrimSpace(r.FormValue("owner_email")),
		OwnerName:             strings.TrimSpace(r.FormValue("owner_name")),
		OwnerPhone:            strings.TrimSpace(r.FormValue("owner_phone")),
		OwnerPassword:         strings.TrimSpace(r.FormValue("owner_password")),
		SendResetToken:        sendReset,
		IsSponsor:             isSponsor,
		AllowPartialPayments:  allowPartial,
		MinInvoiceAmountKobo:  minInvoiceKobo,
		UpfrontPercent:        upfrontPct,
		MinInstallmentPercent: minInstallPct,
		MaxInstallments:       maxInstallments,
		AllowFullPayAlways:    allowFullAlways,
		CreateScanningService: createScanning,
		BulkPaymentsEnabled:   r.FormValue("bulk_payments_enabled") == "on",
	}

	merchant, resetToken, err := a.store.CreateMerchantDirect(r.Context(), params)
	if err != nil {
		a.logger.WarnContext(r.Context(), "create merchant direct failed", "name", name, "error", err)
		http.Error(w, "merchant creation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Bulk switches live on their own handler; apply the create-form values so a
	// new merchant starts with the admin's payout and location choices.
	payoutMode := strings.TrimSpace(r.FormValue("bulk_payout_mode"))
	if payoutMode != "wallet" && payoutMode != "bank" {
		payoutMode = "wallet"
	}
	if err := a.store.SetMerchantBulkSettings(r.Context(), merchant.ID,
		r.FormValue("bulk_payments_enabled") == "on",
		r.FormValue("bulk_location_required") == "on", payoutMode); err != nil {
		a.logger.WarnContext(r.Context(), "apply merchant bulk settings failed", "merchant_id", merchant.ID.String(), "error", err)
	}

	var setPasswordURL string
	var emailError string
	if resetToken != nil && *resetToken != "" {
		setPasswordURL = a.cfg.BaseURL + "/merchant/set-password?token=" + *resetToken

		// Resolve the linked owner after creation. The form email is empty when
		// an administrator chooses an existing user, so it is not a reliable
		// delivery address for the password-reset link.
		ownerID, err := a.store.MerchantOwnerID(r.Context(), merchant.ID)
		if err != nil {
			a.logger.WarnContext(r.Context(), "find merchant owner for password reset notification failed", "merchant_id", merchant.ID.String(), "error", err)
		} else {
			owner, err := a.store.UserByID(r.Context(), ownerID)
			if err != nil {
				a.logger.WarnContext(r.Context(), "find merchant owner for password reset notification failed", "merchant_id", merchant.ID.String(), "owner_id", ownerID.String(), "error", err)
			} else {
				a.conversation.NotifyMerchantApproved(r.Context(), owner, merchant.Name, setPasswordURL)
				if err := a.sendMerchantOwnerNotificationEmail(r.Context(), owner.Email, merchant.Name, setPasswordURL); err != nil {
					a.logger.WarnContext(r.Context(), "send merchant password reset email failed", "merchant_id", merchant.ID.String(), "owner_id", owner.ID.String(), "error", err)
					emailError = err.Error()
				}
			}
		}
	}

	a.audit(r, "admin.merchants.created_direct", "merchant", merchant.ID.String(), map[string]any{"name": merchant.Name, "slug": merchant.Slug, "is_sponsor": isSponsor})
	a.logger.InfoContext(r.Context(), "merchant created directly by admin", "merchant_id", merchant.ID.String(), "name", merchant.Name)

	if setPasswordURL != "" {
		redirectURL := "/admin/merchants?merchant_created=1"
		if emailError != "" {
			redirectURL += "&email_error=" + url.QueryEscape(emailError)
		}
		http.Redirect(w, r, redirectURL+"&reset_url="+url.QueryEscape(setPasswordURL), http.StatusSeeOther)
		return
	}
	if emailError != "" {
		http.Redirect(w, r, "/admin/merchants?merchant_created=1&email_error="+url.QueryEscape(emailError), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/merchants?merchant_created=1", http.StatusSeeOther)
}

// sendMerchantOwnerNotificationEmail sends the owner a one-time URL for
// choosing their merchant password after an administrator creates the account.
func (a *App) sendMerchantOwnerNotificationEmail(ctx context.Context, recipient, merchantName, setPasswordURL string) error {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		a.logger.WarnContext(ctx, "send merchant password reset email skipped: empty recipient", "merchant_name", merchantName)
		return nil
	}
	if a.email == nil {
		a.logger.WarnContext(ctx, "send merchant password reset email skipped: email delivery not configured", "merchant_name", merchantName)
		return fmt.Errorf("email delivery is not configured")
	}
	addr := net.JoinHostPort(a.cfg.SMTPHost, strconv.Itoa(a.cfg.SMTPPort))
	a.logger = a.logger.With("smtp_host", addr, "smtp_from", a.cfg.SMTPFrom, "to", recipient)
	if setPasswordURL == "" {
		a.logger.WarnContext(ctx, "send merchant password reset email skipped: empty reset URL", "merchant_name", merchantName)
		return fmt.Errorf("password reset URL is empty")
	}

	subject := fmt.Sprintf("Set your %s merchant password", a.cfg.AppName)
	body := fmt.Sprintf(
		"Your %s merchant account for %s has been created.\n\nSet your password using this link (it expires in 72 hours):\n%s\n\nIf you were not expecting this email, please contact support.",
		a.cfg.AppName,
		merchantName,
		setPasswordURL,
	)
	if err := a.email.Send(ctx, recipient, subject, body); err != nil {
		a.logger.ErrorContext(ctx, "send merchant password reset email failed", "merchant_name", merchantName, "to", recipient, "error", err)
		return fmt.Errorf("send merchant password reset email: %w", err)
	}
	return nil
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
	// Scheduled sweep history and the open chain alert (G7 residual): the
	// dashboard shows the on-demand verdict alongside the persistent record.
	chainChecks, err := a.store.RecentAuditChainChecks(r.Context(), 10)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	openAlert, hasAlert, err := a.store.OpenAuditChainAlert(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_audit.html", r, "Audit log", map[string]any{
		"Entries":       entries,
		"ChainCount":    count,
		"ChainBroken":   broken,
		"ChainChecks":   chainChecks,
		"ChainAlert":    openAlert,
		"HasChainAlert": hasAlert,
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
	alertRail := strings.TrimSpace(r.URL.Query().Get("rail"))
	alerts, err := a.store.ListTransactionAlertsFiltered(r.Context(), "", alertRail, 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list transaction alerts", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	byTier := map[string]int{kyc.TierL0: 0, kyc.TierL1: 0, kyc.TierL2: 0, kyc.TierL3: 0}
	ids := make([]uuid.UUID, 0, len(profiles))
	for _, p := range profiles {
		byTier[p.Tier]++
		ids = append(ids, p.UserID)
	}
	// M4 feedback loop made actionable: rules whose labelled false-positive
	// share crosses the configured threshold get a tuning suggestion in the
	// queue (the reviewer, not the system, moves any threshold).
	fpStats, err := a.store.RuleFalsePositiveRates(r.Context(), 7*24*time.Hour)
	if err != nil {
		a.logger.WarnContext(r.Context(), "rule fp rates", "error", err)
		fpStats = nil
	}
	th := store.DefaultTuningThresholds()
	if a.cfg.TuningFPPercent > 0 {
		th.FPPercent = a.cfg.TuningFPPercent
	}
	if a.cfg.TuningMinLabelled > 0 {
		th.MinLabelled = a.cfg.TuningMinLabelled
	}
	suggestions := store.BuildTuningSuggestions(fpStats, th)
	// M2 signal surface: devices seen per profile, so an investigator can
	// spot one browser behind many accounts (the inverse pattern matters too).
	devices, err := a.store.DeviceSummaryForUsers(r.Context(), ids)
	if err != nil {
		a.logger.WarnContext(r.Context(), "device summaries", "error", err)
		devices = map[uuid.UUID]store.DeviceSummary{}
	}
	a.renderAdmin(w, "admin_kyc.html", r, "KYC ladder", map[string]any{
		"Profiles": profiles,
		"Cases":    cases,
		"Alerts":   alerts,
		"ByTier":   byTier,
		"Devices":  devices,
		// Rail filter for the monitoring queue: the rails the engine tags
		// alerts with, plus the active selection (empty = all rails).
		"AlertRails":  []string{kyc.RailPayment, kyc.RailTopup, kyc.RailStandingCredit, kyc.RailRefund, kyc.RailPayout},
		"AlertRail":   alertRail,
		"Suggestions": suggestions,
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
	// M4 feedback loop: on a resolve the reviewer states the disposition
	// (false_positive by default, or confirmed); acknowledged/escalated map
	// to undetermined/confirmed automatically.
	outcome := strings.TrimSpace(r.FormValue("outcome"))
	if outcome != "false_positive" && outcome != "confirmed" && outcome != "" {
		http.Error(w, "invalid outcome", http.StatusBadRequest)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())
	alert, err := a.store.ResolveTransactionAlertOutcome(r.Context(), alertID, status, adminEmail, outcome)
	if err != nil {
		a.logger.WarnContext(r.Context(), "resolve transaction alert failed", "alert_id", alertID, "error", err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	// The moment an alert is confirmed, the ring-case pipeline runs: any
	// ring containing a member with confirmed evidence gets its network
	// case raised (or reopened) immediately, not the next time someone
	// opens /admin/network. A refresh failure must not fail the resolve —
	// the next confirmation or page load heals it.
	if label, _ := alert.Details["outcome"].(string); label == "confirmed" {
		if n, err := a.store.RefreshNetworkCases(r.Context(), 1); err != nil {
			a.logger.WarnContext(r.Context(), "refresh network cases after confirm", "error", err)
		} else if n > 0 {
			a.logger.InfoContext(r.Context(), "network cases raised from confirmation", "raised", n, "alert_id", alert.ID)
		}
	}
	a.logger.InfoContext(r.Context(), "transaction alert resolved", "alert_id", alert.ID, "status", alert.Status)
	next := strings.TrimSpace(r.FormValue("next"))
	if next != "/admin/fraud" {
		next = "/admin/kyc"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// adminKYCReview approves or rejects a manual review case.
func (a *App) adminOnboardingReminders(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.OnboardingReminderStats(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "onboarding reminder stats", "error", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_onboarding_reminders.html", r, "Onboarding reminders", map[string]any{"Stats": stats})
}

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
	// G1 (spec §3.3a): a pep_edd case is disposed by senior management with
	// its own decision vocabulary (sm_approve / sm_reject) — the ordinary
	// approve/reject must not be able to clear a PEP determination.
	if strings.TrimSpace(r.FormValue("decision")) == "sm_approve" || strings.TrimSpace(r.FormValue("decision")) == "sm_reject" {
		c, err := a.store.ReviewPEPEDDCase(r.Context(), caseID, strings.TrimSpace(r.FormValue("decision")) == "sm_approve", adminEmail, note, actor)
		if err != nil {
			a.logger.WarnContext(r.Context(), "pep_edd review failed", "case_id", caseID, "error", err)
			http.Error(w, "pep_edd review failed", http.StatusInternalServerError)
			return
		}
		a.logger.InfoContext(r.Context(), "pep_edd case reviewed", "case_id", c.ID, "status", c.Status, "user_id", c.UserID.String())
		http.Redirect(w, r, "/admin/kyc", http.StatusSeeOther)
		return
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
		var emailError string
		token, err := randomToken(32)
		if err == nil {
			if err := a.store.CreateMerchantPasswordResetToken(r.Context(), merchant.ID, owner.ID, token, time.Now().Add(72*time.Hour)); err == nil {
				setPasswordURL = a.cfg.BaseURL + "/merchant/set-password?token=" + token
			}
		}
		a.conversation.NotifyMerchantApproved(r.Context(), owner, merchant.Name, setPasswordURL)
		if setPasswordURL != "" {
			if err := a.sendMerchantOwnerNotificationEmail(r.Context(), owner.Email, merchant.Name, setPasswordURL); err != nil {
				a.logger.WarnContext(r.Context(), "send merchant password reset email failed", "merchant_id", merchant.ID.String(), "owner_id", owner.ID.String(), "error", err)
				emailError = err.Error()
			}
		}
		if emailError != "" {
			http.Redirect(w, r, "/admin/merchants?merchant_created=1&email_error="+url.QueryEscape(emailError), http.StatusSeeOther)
			return
		}
		a.audit(r, "admin.merchants.approved", "merchant", merchant.ID.String(), map[string]any{"name": merchant.Name, "registration_id": id.String()})
		http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
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

func (a *App) adminSetMerchantBulkPayments(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("bulk_payments_enabled") == "on"
	if err := a.store.SetMerchantBulkPayments(r.Context(), id, enabled); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.bulk_payments_updated", "merchant", id.String(), map[string]any{
		"enabled": enabled,
	})
	http.Redirect(w, r, "/admin/merchants?bulk_set=1", http.StatusSeeOther)
}

// adminSetMerchantActive toggles the merchant's active flag. Disabling hides
// the merchant from the catalogue and refuses checkout, but keeps every row —
// the toggle is reversible and never collides with a foreign-key constraint.
func (a *App) adminSetMerchantActive(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	active := r.FormValue("active") == "1"
	if err := a.store.SetMerchantActive(r.Context(), id, active); err != nil {
		a.logger.WarnContext(r.Context(), "set merchant active failed", "merchant_id", id.String(), "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.active_toggled", "merchant", id.String(), map[string]any{"active": active})
	http.Redirect(w, r, "/admin/merchants?active_set=1", http.StatusSeeOther)
}

// adminDeleteMerchant removes a merchant and its cascade-owned rows. Payments
// and invoices reference merchants(id) without CASCADE, so a merchant with
// financial history cannot be hard-deleted: the foreign-key constraint
// surfaces a clear error and the admin must use adminSetMerchantActive(false)
// instead. Financial records are immutable by design (MLPA).
func (a *App) adminDeleteMerchant(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	if err := a.store.DeleteMerchant(r.Context(), id); err != nil {
		a.logger.WarnContext(r.Context(), "delete merchant failed", "merchant_id", id.String(), "error", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.deleted", "merchant", id.String(), nil)
	http.Redirect(w, r, "/admin/merchants?deleted=1", http.StatusSeeOther)
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
