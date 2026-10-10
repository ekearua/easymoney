package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
)

// merchantPayoutRow is one settlement batch as shown on the merchant payouts
// page, joined with its payout (if any) so a request's outcome is visible.
type merchantPayoutRow struct {
	BatchNo      string
	Status       string
	NetKobo      int64
	CanRequest   bool
	PayoutID     uuid.UUID
	PayoutStatus string
	PayoutError  string
}

// merchantPayouts is the merchant-portal payout surface. It lists the
// merchant's settlement batches and their payout state, and hosts the G13
// step-up form when a high-risk payout needs a fresh second factor.
func (a *App) merchantPayouts(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	batches, err := a.store.ListSettlementBatches(r.Context(), merchantID, 50)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list settlement batches", "error", err)
		http.Error(w, "payouts unavailable", http.StatusInternalServerError)
		return
	}
	payouts, err := a.store.ListPayouts(r.Context(), merchantID, 50)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list payouts", "error", err)
		http.Error(w, "payouts unavailable", http.StatusInternalServerError)
		return
	}
	byBatch := make(map[uuid.UUID]store.Payout, len(payouts))
	for _, p := range payouts {
		byBatch[p.BatchID] = p
	}
	rows := make([]merchantPayoutRow, 0, len(batches))
	for _, b := range batches {
		row := merchantPayoutRow{
			BatchNo:    b.BatchNo,
			Status:     b.Status,
			NetKobo:    b.TotalKobo - b.FeeKobo,
			CanRequest: b.Status == store.SettlementBatchOpen || b.Status == store.SettlementBatchScheduled,
		}
		if p, ok := byBatch[b.ID]; ok {
			row.PayoutID = p.ID
			row.PayoutStatus = p.Status
			row.PayoutError = p.LastError
		}
		rows = append(rows, row)
	}
	notice := r.URL.Query().Get("notice")
	a.renderMerchant(w, "merchant_payouts.html", r, "Payouts", map[string]any{
		"Rows":            rows,
		"BatchNo":         strings.TrimSpace(r.URL.Query().Get("batch")),
		"Notice":          merchantPayoutNotice(notice),
		"ShowStepUp":      notice == "step-up-required",
		"StepUpAvailable": a.merchantStepUpAvailable(r.Context(), merchantID),
		"StepUpEnabled":   a.cfg.StepUpEnabled,
	})
}

// merchantRequestPayout settles one batch through the shared settlement
// service. A high-risk owner without a fresh step-up grant is refused and sent
// to the step-up form.
func (a *App) merchantRequestPayout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	batchNo := strings.TrimSpace(chi.URLParam(r, "batchNo"))
	payout, err := a.settlements.RequestPayout(r.Context(), batchNo, nil)
	switch {
	case errors.Is(err, service.ErrStepUpRequired):
		a.redirectMerchantPayouts(w, r, "step-up-required", batchNo)
	case err != nil:
		a.logger.WarnContext(r.Context(), "merchant payout request failed", "batch", batchNo, "error", err)
		a.redirectMerchantPayouts(w, r, "error", batchNo)
	default:
		a.audit(r, "merchant.payout_requested", "merchant", payout.MerchantID.String(),
			map[string]any{"batch": payout.BatchID.String(), "payout_id": payout.ID.String()})
		a.redirectMerchantPayouts(w, r, "requested", batchNo)
	}
}

// merchantStepUpVerify checks a fresh authenticator code against the merchant's
// enrolled TOTP factor and, on success, records a short-lived payout grant.
func (a *App) merchantStepUpVerify(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	batchNo := strings.TrimSpace(r.FormValue("batch"))
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		a.redirectMerchantPayouts(w, r, "step-up-invalid", batchNo)
		return
	}
	factor, err := a.store.MFAFactorByKind(r.Context(), store.MFAScopeMerchant, merchantID, store.MFAKindTOTP)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load merchant totp factor", "error", err)
		a.redirectMerchantPayouts(w, r, "error", batchNo)
		return
	}
	if factor == nil || factor.ConfirmedAt == nil || len(factor.SecretCipher) == 0 {
		a.redirectMerchantPayouts(w, r, "step-up-unavailable", batchNo)
		return
	}
	secret, err := totp.DecryptSecret(a.totpKey, factor.SecretCipher)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "decrypt merchant totp secret", "error", err)
		a.redirectMerchantPayouts(w, r, "error", batchNo)
		return
	}
	if !totp.Validate(code, secret) {
		a.redirectMerchantPayouts(w, r, "step-up-invalid", batchNo)
		return
	}
	if err := a.store.RecordStepUpGrant(r.Context(), store.StepUpScopeMerchant, merchantID,
		store.StepUpActionMerchantPayout, store.MFAKindTOTP, a.cfg.StepUpTTL); err != nil {
		a.logger.ErrorContext(r.Context(), "record step-up grant", "error", err)
		a.redirectMerchantPayouts(w, r, "error", batchNo)
		return
	}
	a.audit(r, "merchant.step_up", "merchant", merchantID.String(),
		map[string]any{"action": store.StepUpActionMerchantPayout, "factor": store.MFAKindTOTP})
	a.redirectMerchantPayouts(w, r, "step-up-granted", batchNo)
}

// merchantStepUpAvailable reports whether the merchant can complete a step-up:
// authenticator apps configured and a confirmed TOTP factor enrolled.
func (a *App) merchantStepUpAvailable(ctx context.Context, merchantID uuid.UUID) bool {
	if !a.totpAvailable() {
		return false
	}
	factor, err := a.store.MFAFactorByKind(ctx, store.MFAScopeMerchant, merchantID, store.MFAKindTOTP)
	return err == nil && factor != nil && factor.ConfirmedAt != nil && len(factor.SecretCipher) > 0
}

// redirectMerchantPayouts returns to the payout page carrying one notice and
// the batch under action.
func (a *App) redirectMerchantPayouts(w http.ResponseWriter, r *http.Request, notice, batchNo string) {
	q := url.Values{"notice": {notice}}
	if batchNo != "" {
		q.Set("batch", batchNo)
	}
	http.Redirect(w, r, "/merchant/payouts?"+q.Encode(), http.StatusSeeOther)
}

// merchantPayoutNotice turns a redirect keyword into page copy.
func merchantPayoutNotice(keyword string) string {
	switch keyword {
	case "requested":
		return "Payout requested."
	case "step-up-required":
		return "This payout is in the high-risk band. Confirm a fresh authenticator code to release it."
	case "step-up-granted":
		return "Identity confirmed. Request the payout again while the step-up window is open."
	case "step-up-invalid":
		return "That authenticator code was not valid. Try again."
	case "step-up-unavailable":
		return "Add an authenticator app under Security before requesting a high-risk payout."
	case "error":
		return "That payout could not be requested."
	}
	return ""
}
