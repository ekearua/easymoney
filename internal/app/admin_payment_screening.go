package app

// Spec G2: the payment-screening review queue. Ambiguous counterparty screens
// (possible/strong) hold their movement — payout status 'held' with the
// allowance reservation in place, or a refused withdrawal reservation — and a
// reviewer disposes them here. Every disposition is audited.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-payment-demo/internal/store"
)

// adminPaymentScreenings renders the queue: ambiguous hits first (the open
// holds), then recent history, so an investigator sees what is blocking money
// movement now and what has already been decided.
func (a *App) adminPaymentScreenings(w http.ResponseWriter, r *http.Request) {
	holds, err := a.store.ListPaymentScreenings(r.Context(), true, 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "payment screening holds list", "error", err)
		http.Error(w, "screening queue unavailable", http.StatusInternalServerError)
		return
	}
	recent, err := a.store.ListPaymentScreenings(r.Context(), false, 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "payment screening history list", "error", err)
		http.Error(w, "screening queue unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_payment_screenings.html", r, "Payment screening", map[string]any{
		"Holds":  holds,
		"Recent": recent,
		"CSRF":   csrfFromContext(r.Context()),
	})
}

// adminPaymentScreeningDispose applies the reviewer's release/reject decision
// to a held payout. Withdrawal holds have no disposition path: their
// reservations release only by expiry, so the customer re-attempts after
// compliance resolves the screening — noted on the queue page.
func (a *App) adminPaymentScreeningDispose(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	screeningID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || screeningID <= 0 {
		http.Error(w, "invalid screening id", http.StatusBadRequest)
		return
	}
	scr, err := a.store.PaymentScreeningByID(r.Context(), screeningID)
	if err != nil {
		http.Error(w, "screening row not found", http.StatusNotFound)
		return
	}
	if scr.Rail != store.RailMerchantPayout {
		http.Error(w, "only merchant-payout holds can be dispositioned", http.StatusBadRequest)
		return
	}
	if scr.MovementRef == "" {
		http.Error(w, "screening row has no movement reference", http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	approve := action == "release"
	if !approve && action != "reject" {
		http.Error(w, "action must be release or reject", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		http.Error(w, "a disposition reason is required", http.StatusBadRequest)
		return
	}
	payout, err := a.store.PayoutByBatchNo(r.Context(), movementRefBatchNo(scr.MovementRef))
	if err != nil {
		http.Error(w, "payout for screening row not found", http.StatusNotFound)
		return
	}
	reviewer := adminEmailFromContext(r.Context())
	if _, err := a.store.ReleaseHeldPayout(r.Context(), payout.ID, approve, reviewer, reason); err != nil {
		a.logger.WarnContext(r.Context(), "payment screening disposition", "screening_id", screeningID, "error", err)
		http.Error(w, "disposition failed: payout must still be held", http.StatusBadRequest)
		return
	}
	a.audit(r, "admin.payment_screening_disposition", "payment_screening", scr.MovementRef,
		map[string]any{"action": action, "reviewer": reviewer, "decision": scr.Decision,
			"counterparty": scr.CounterpartyName, "amount_kobo": scr.AmountKobo})
	http.Redirect(w, r, "/admin/payment-screenings", http.StatusSeeOther)
}

// movementRefBatchNo strips the "payout:" prefix from a payout movement ref.
func movementRefBatchNo(ref string) string {
	return strings.TrimPrefix(ref, "payout:")
}
