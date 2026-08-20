package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/store"
)

func (a *App) merchantScanner(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Services": services}
	a.renderMerchant(w, "merchant_scanner.html", r, "Receipt scanner", data)
}

func (a *App) merchantScanPage(w http.ResponseWriter, r *http.Request) {
	a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{})
}

func (a *App) merchantScanPost(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Error": "Enter a scan token, manual code, or scan URL."})
		return
	}
	result, err := a.store.MerchantValidateScanToken(r.Context(), merchantID, token, "")
	if err != nil {
		a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Error": "Scan validation failed."})
		return
	}
	a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Result": result})
}

func (a *App) merchantUpdateServiceWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	owned := false
	for _, svc := range services {
		if svc.ID == serviceID {
			owned = true
			break
		}
	}
	if !owned {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	whitelist := strings.TrimSpace(r.FormValue("phone_whitelist"))
	if err := a.store.UpdateServicePhoneWhitelist(r.Context(), serviceID, whitelist); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/scanner?whitelist_saved=1", http.StatusSeeOther)
}

func (a *App) merchantServicesList(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	services, err := a.store.ListMerchantServices(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	purchases, err := a.store.ServicePurchasesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_services_list.html", r, "Services", map[string]any{
		"Services": services, "Purchases": purchases,
	})
}

func (a *App) merchantServiceNewForm(w http.ResponseWriter, r *http.Request) {
	a.renderMerchant(w, "merchant_service_form.html", r, "New service", map[string]any{"Edit": false, "CustomFields": []store.ServiceCustomField{}})
}

func (a *App) merchantServiceCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	unitPrice, _ := strconv.ParseInt(r.FormValue("unit_price_kobo"), 10, 64)
	qtyAvailable, _ := strconv.Atoi(r.FormValue("quantity_available"))
	if name == "" || unitPrice <= 0 {
		http.Error(w, "name and unit price required", http.StatusBadRequest)
		return
	}
	if qtyAvailable < -1 {
		qtyAvailable = -1
	}
	var expiresAt *time.Time
	if expiryStr := strings.TrimSpace(r.FormValue("expires_at")); expiryStr != "" {
		if t, err := time.Parse("2006-01-02", expiryStr); err == nil {
			expiresAt = &t
		}
	}
	svc, err := a.store.CreateMerchantService(r.Context(), merchantID, name, description, unitPrice, qtyAvailable, expiresAt)
	if err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := a.saveCustomFields(r, svc.ID); err != nil {
		a.logger.ErrorContext(r.Context(), "save custom fields", "error", err)
	}
	http.Redirect(w, r, "/merchant/services?created=1", http.StatusSeeOther)
}

func (a *App) saveCustomFields(r *http.Request, serviceID uuid.UUID) error {
	names := r.Form["cf_name[]"]
	types := r.Form["cf_type[]"]
	reqStrs := r.Form["cf_required[]"]
	options := r.Form["cf_options[]"]
	var fields []store.CustomFieldSpec
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		ft := "text"
		if i < len(types) {
			ft = types[i]
		}
		isReq := false
		if i < len(reqStrs) && reqStrs[i] == "1" {
			isReq = true
		}
		opts := ""
		if i < len(options) {
			opts = options[i]
		}
		fields = append(fields, store.CustomFieldSpec{
			FieldName:    n,
			FieldType:    ft,
			FieldOptions: opts,
			IsRequired:   isReq,
			SortOrder:    i,
		})
	}
	return a.store.SetServiceCustomFields(r.Context(), serviceID, fields)
}

func (a *App) merchantServiceEditForm(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	customFields, _ := a.store.ListServiceCustomFields(r.Context(), serviceID)
	a.renderMerchant(w, "merchant_service_form.html", r, "Edit service", map[string]any{
		"Service": svc, "Edit": true, "CustomFields": customFields,
	})
}

func (a *App) merchantServiceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	unitPrice, _ := strconv.ParseInt(r.FormValue("unit_price_kobo"), 10, 64)
	qtyAvailable, _ := strconv.Atoi(r.FormValue("quantity_available"))
	if name == "" || unitPrice <= 0 {
		http.Error(w, "name and unit price required", http.StatusBadRequest)
		return
	}
	if qtyAvailable < -1 {
		qtyAvailable = -1
	}
	var expiresAt *time.Time
	if expiryStr := strings.TrimSpace(r.FormValue("expires_at")); expiryStr != "" {
		if t, err := time.Parse("2006-01-02", expiryStr); err == nil {
			expiresAt = &t
		}
	}
	if err := a.store.UpdateMerchantService(r.Context(), serviceID, name, description, unitPrice, qtyAvailable, expiresAt); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := a.saveCustomFields(r, serviceID); err != nil {
		a.logger.ErrorContext(r.Context(), "save custom fields", "error", err)
	}
	http.Redirect(w, r, "/merchant/services?saved=1", http.StatusSeeOther)
}

func (a *App) merchantServiceToggle(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	if err := a.store.ToggleMerchantService(r.Context(), serviceID, merchantID); err != nil {
		http.Error(w, "toggle failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/services?toggled=1", http.StatusSeeOther)
}

func (a *App) merchantServicePayments(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	purchases, err := a.store.ServicePurchasesByServiceID(r.Context(), serviceID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	purchaseIDs := make([]uuid.UUID, len(purchases))
	for i, p := range purchases {
		purchaseIDs[i] = p.ID
	}
	customData, _ := a.store.PurchaseCustomDataByPurchaseIDs(r.Context(), purchaseIDs)
	a.renderMerchant(w, "merchant_service_payments.html", r, "Service payments", map[string]any{
		"Service": svc, "Purchases": purchases, "CustomData": customData,
	})
}

func (a *App) merchantInvoices(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	invoices, err := a.store.InvoicesByMerchantID(r.Context(), merchantID, 50, 0)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_invoices.html", r, "Invoices", map[string]any{"Invoices": invoices})
}

func (a *App) merchantPayments(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	payments, err := a.store.PaymentsByMerchantID(r.Context(), merchantID, 50, 0)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_payments.html", r, "Payments", map[string]any{"Payments": payments})
}

func (a *App) merchantSettings(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	qrTTL, err := a.store.MerchantServiceTTL(r.Context(), merchantID)
	if err != nil {
		qrTTL = 86400
	}
	apiKeys, err := a.store.ListMerchantAPIKeys(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	webhookCfg, err := a.store.MerchantWebhookConfig(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_settings.html", r, "Payment settings", map[string]any{
		"Merchant": merchant, "Services": services, "QRValidityHours": qrTTL / 3600, "TOTPEnabled": a.cfg.TOTPEnabled,
		"APIKeys": apiKeys, "WebhookURL": webhookCfg.URL, "WebhookSecretSet": webhookCfg.Secret != "",
	})
}

func (a *App) merchantUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
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
	if err := a.store.UpdateMerchantPaymentTerms(r.Context(), merchantID, allowPartial, minInvoiceKobo, upfrontPct, minInstallPct, maxInstallments, allowFullAlways); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if hours, err := strconv.Atoi(r.FormValue("qr_validity_hours")); err == nil && hours >= 1 && hours <= 720 {
		_ = a.store.UpdateMerchantServiceTTL(r.Context(), merchantID, hours*3600)
	}
	http.Redirect(w, r, "/merchant/settings?saved=1", http.StatusSeeOther)
}

func (a *App) merchantProfile(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_profile.html", r, "Business profile", map[string]any{"Merchant": merchant})
}

func (a *App) merchantUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	category := strings.TrimSpace(r.FormValue("category"))
	description := strings.TrimSpace(r.FormValue("description"))
	logoURL := strings.TrimSpace(r.FormValue("logo_url"))
	if name == "" {
		name = "Untitled"
	}
	if err := a.store.UpdateMerchantProfile(r.Context(), merchantID, name, category, description, logoURL); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/profile?saved=1", http.StatusSeeOther)
}

func (a *App) merchantSetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	merchantID, _, err := a.store.ValidateMerchantPasswordResetToken(r.Context(), token)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "This link has expired or has already been used.", "Invalid": true})
		return
	}
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid link.", "Invalid": true})
		return
	}
	a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Merchant": merchant, "Token": token})
}

func (a *App) merchantSetPasswordPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	if token == "" || password == "" {
		http.Error(w, "token and password required", http.StatusBadRequest)
		return
	}
	if password != confirm {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Token": token, "Error": "Passwords do not match."})
		return
	}
	if len(password) < 8 {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Token": token, "Error": "Password must be at least 8 characters."})
		return
	}
	merchantID, _, err := a.store.ValidateMerchantPasswordResetToken(r.Context(), token)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "This link has expired or has already been used.", "Invalid": true})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateMerchantPassword(r.Context(), merchantID, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	_ = a.store.UseMerchantPasswordResetToken(r.Context(), token)
	a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Done": true})
}
