// S2: refund + dispute surfaces. Merchants self-serve refunds through the
// signed Partner API; operators manage disputes and review refunds through the
// admin console.
package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

const refundMaxReasonLen = 500

// ---------------------------------------------------------------------------
// Partner API: merchant self-serve
// ---------------------------------------------------------------------------

type apiRefundRequest struct {
	Reason string `json:"reason"`
}

// apiRefundPayment processes a full refund for a succeeded payment. The payment
// must not already be refunded or in a processed settlement batch.
func (a *App) apiRefundPayment(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	if reference == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "reference is required")
		return
	}
	var req apiRefundRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, apiKeyMaxBodyBytes))
	_ = json.Unmarshal(body, &req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "merchant-initiated refund"
	}
	if len(reason) > refundMaxReasonLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "reason_invalid", "reason too long (max 500 chars)")
		return
	}

	// Look up the payment by merchant reference.
	payment, err := a.store.PaymentByMerchantReference(r.Context(), auth.merchant.ID, reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "payment not found")
			return
		}
		a.logger.Error("api refund lookup failed", "merchant_id", auth.merchant.ID, "reference", reference, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "refund lookup failed")
		return
	}
	if payment.Status != domain.StatusSucceeded {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_status",
			"payment must be in succeeded status to refund (current: "+string(payment.Status)+")")
		return
	}

	refund, err := a.refunds.Refund(r.Context(), payment.ID.String(), reason)
	if err != nil {
		if strings.Contains(err.Error(), "settlement batch") {
			writeAPIError(w, http.StatusConflict, "batch_conflict", err.Error())
			return
		}
		a.logger.Error("api refund failed", "payment_id", payment.ID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "refund failed")
		return
	}
	a.audit(r, "api.payment.refunded", "refund", refund.ID.String(), map[string]any{
		"merchant_id": auth.merchant.ID.String(), "payment_id": payment.ID.String(),
	})
	a.writeRefundJSON(w, refund)
}

// apiRefundStatus returns the current state of a refund.
func (a *App) apiRefundStatus(w http.ResponseWriter, r *http.Request) {
	refundID := chi.URLParam(r, "id")
	refund, err := a.refunds.RefundByID(r.Context(), refundID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "refund not found")
		return
	}
	a.writeRefundJSON(w, refund)
}

// apiListDisputes returns the merchant's disputes.
func (a *App) apiListDisputes(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	disputes, err := a.disputes.ListDisputes(r.Context(), auth.merchant.ID.String(), 100)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "dispute lookup failed")
		return
	}
	view := make([]map[string]any, 0, len(disputes))
	for _, d := range disputes {
		view = append(view, a.disputeJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"disputes": view})
}

// apiDisputeDetail returns one dispute by id.
func (a *App) apiDisputeDetail(w http.ResponseWriter, r *http.Request) {
	disputeID := chi.URLParam(r, "id")
	d, err := a.disputes.DisputeByID(r.Context(), disputeID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "dispute not found")
		return
	}
	writeJSON(w, http.StatusOK, a.disputeJSON(d))
}

// ---------------------------------------------------------------------------
// JSON views
// ---------------------------------------------------------------------------

func (a *App) writeRefundJSON(w http.ResponseWriter, r store.Refund) {
	writeJSON(w, http.StatusOK, map[string]any{
		"refund_id":       r.ID.String(),
		"payment_id":      r.PaymentID.String(),
		"merchant_id":     r.MerchantID.String(),
		"amount":          map[string]any{"value": r.AmountKobo, "currency": "NGN"},
		"reason":          r.Reason,
		"status":          r.Status,
		"provider_refund": r.ProviderRefundID,
		"error":           r.LastError,
		"created_at":      r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"completed_at":    timePtr(r.CompletedAt),
	})
}

func (a *App) disputeJSON(d store.Dispute) map[string]any {
	return map[string]any{
		"dispute_id":  d.ID.String(),
		"payment_id":  d.PaymentID.String(),
		"merchant_id": d.MerchantID.String(),
		"reason":      d.Reason,
		"status":      d.Status,
		"created_at":  d.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"resolved_at": timePtr(d.ResolvedAt),
	}
}

func timePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// ---------------------------------------------------------------------------
// Admin console
// ---------------------------------------------------------------------------

// adminRefunds lists all refunds with merchant names.
func (a *App) adminRefunds(w http.ResponseWriter, r *http.Request) {
	refunds, err := a.store.ListAllRefunds(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list refunds", "error", err)
		http.Error(w, "refunds unavailable", http.StatusInternalServerError)
		return
	}
	merchantNames := map[uuid.UUID]string{}
	for _, ref := range refunds {
		if _, ok := merchantNames[ref.MerchantID]; !ok {
			if m, err := a.store.MerchantByID(r.Context(), ref.MerchantID); err == nil {
				merchantNames[ref.MerchantID] = m.Name
			}
		}
	}
	a.renderAdmin(w, "admin_refunds.html", r, "Refunds", map[string]any{
		"Refunds":       refunds,
		"MerchantNames": merchantNames,
		"Result":        r.URL.Query().Get("result"),
		"Error":         r.URL.Query().Get("error"),
	})
}

// adminRefundFail marks a pending refund as failed (operator override).
func (a *App) adminRefundFail(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	refundID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid refund id", http.StatusBadRequest)
		return
	}
	if err := a.store.FailRefund(r.Context(), refundID, "operator override"); err != nil {
		a.logger.WarnContext(r.Context(), "fail refund", "refund_id", refundID, "error", err)
		http.Redirect(w, r, "/admin/refunds?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.refund.failed", "refund", refundID.String(), nil)
	http.Redirect(w, r, "/admin/refunds?result=failed", http.StatusSeeOther)
}

// adminDisputes lists all disputes with merchant names.
func (a *App) adminDisputes(w http.ResponseWriter, r *http.Request) {
	disputes, err := a.store.ListAllDisputes(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list disputes", "error", err)
		http.Error(w, "disputes unavailable", http.StatusInternalServerError)
		return
	}
	merchantNames := map[uuid.UUID]string{}
	for _, d := range disputes {
		if _, ok := merchantNames[d.MerchantID]; !ok {
			if m, err := a.store.MerchantByID(r.Context(), d.MerchantID); err == nil {
				merchantNames[d.MerchantID] = m.Name
			}
		}
	}
	a.renderAdmin(w, "admin_disputes.html", r, "Disputes", map[string]any{
		"Disputes":      disputes,
		"MerchantNames": merchantNames,
		"Result":        r.URL.Query().Get("result"),
		"Error":         r.URL.Query().Get("error"),
	})
}

// adminResolveDispute resolves a dispute as won or lost.
func (a *App) adminResolveDispute(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	disputeID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid dispute id", http.StatusBadRequest)
		return
	}
	outcome := r.FormValue("outcome")
	if outcome != "won" && outcome != "lost" {
		http.Error(w, "outcome must be 'won' or 'lost'", http.StatusBadRequest)
		return
	}
	if err := a.disputes.Resolve(r.Context(), disputeID.String(), outcome); err != nil {
		a.logger.WarnContext(r.Context(), "resolve dispute", "dispute_id", disputeID, "error", err)
		http.Redirect(w, r, "/admin/disputes?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.dispute.resolved", "dispute", disputeID.String(), map[string]any{"outcome": outcome})
	http.Redirect(w, r, "/admin/disputes?result=resolved", http.StatusSeeOther)
}
