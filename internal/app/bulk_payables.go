package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// merchantPayables is the payments role's payable list: verified recipients
// only, with identity documents withheld.
func (a *App) merchantPayables(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	filter := recipientFilterFromRequest(r)
	filter.Verification = store.RecipientVerified
	rows, err := a.store.ListBulkRecipients(r.Context(), merchantID, filter, 200, 0)
	if err != nil {
		http.Error(w, "payables unavailable", http.StatusInternalServerError)
		return
	}
	count, err := a.store.CountBulkRecipients(r.Context(), merchantID, filter)
	if err != nil {
		http.Error(w, "payables unavailable", http.StatusInternalServerError)
		return
	}
	settings, err := a.store.MerchantBulkSettings(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "payables unavailable", http.StatusInternalServerError)
		return
	}
	summary, _ := a.store.MerchantBulkSpendingSummary(r.Context(), merchantID)
	a.renderMerchant(w, "merchant_payables.html", r, "Payable recipients", map[string]any{
		"Rows": rows, "Filter": filter, "Count": count, "Settings": settings,
		"Error": r.URL.Query().Get("error"), "Role": merchantRoleFromContext(r.Context()),
		"WalletBalance": summary.WalletBalance, "HasWallet": summary.HasWallet,
		"TotalSpentKobo": summary.TotalSpentKobo, "CompletedBatches": summary.CompletedBatches,
		"TotalBatches": summary.TotalBatches, "PendingBatches": summary.PendingBatches,
		"RecipientsPaid": summary.RecipientsPaid, "PendingAmountKobo": summary.PendingAmountKobo,
		"LastBatchAt": summary.LastBatchAt,
	})
}

// merchantPayableDetail shows one recipient's payment history. Identity values
// are stripped before rendering: the payments role never sees them.
func (a *App) merchantPayableDetail(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid recipient", http.StatusBadRequest)
		return
	}
	recipient, err := a.store.BulkRecipientByID(r.Context(), merchantID, id)
	if err != nil {
		http.Error(w, "recipient not found", http.StatusNotFound)
		return
	}
	recipient.BVN, recipient.NIN, recipient.VIN = "", "", ""
	recipient.AccountNumber = maskAccountNumber(recipient.AccountNumber)
	recipient.AccountName = ""
	summary, _ := a.store.RecipientPaymentSummary(r.Context(), merchantID, id)
	history, _ := a.store.RecipientPaymentHistory(r.Context(), merchantID, id, 50)
	a.renderMerchant(w, "merchant_payable_detail.html", r, "Recipient payments", map[string]any{
		"Recipient": recipient, "Summary": summary, "History": history,
	})
}

func maskAccountNumber(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return value
	}
	return "••••" + value[len(value)-4:]
}

// merchantPayableBatchCreate builds a draft batch from the current filters.
func (a *App) merchantPayableBatchCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	filter := store.RecipientFilter{
		State:       strings.TrimSpace(r.FormValue("state")),
		LGA:         strings.TrimSpace(r.FormValue("lga")),
		Ward:        strings.TrimSpace(r.FormValue("ward")),
		PollingUnit: strings.TrimSpace(r.FormValue("polling_unit")),
		Query:       strings.TrimSpace(r.FormValue("q")),
	}
	amountKobo, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("amount_kobo")), 10, 64)
	if err != nil || amountKobo <= 0 {
		a.redirectPayablesError(w, r, "Enter the amount per recipient in kobo")
		return
	}
	var memberID *uuid.UUID
	if id := merchantMemberIDFromContext(r.Context()); id != uuid.Nil {
		memberID = &id
	}
	batch, err := a.bulkPayments.DraftFromFilter(r.Context(), merchantID, filter, amountKobo, memberID)
	if err != nil {
		a.redirectPayablesError(w, r, bulkPayableMessage(err))
		return
	}
	a.audit(r, "merchant.payables.batch_drafted", "bulk_batch", batch.ID.String(), map[string]any{
		"amount_kobo": amountKobo, "targets": batch.TotalTargets,
	})
	http.Redirect(w, r, "/merchant/payables/batches/"+batch.ID.String(), http.StatusSeeOther)
}

func (a *App) redirectPayablesError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/merchant/payables?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// merchantPayableBatchReview shows a draft or released batch with its items.
func (a *App) merchantPayableBatchReview(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	batch, err := a.store.BulkBatchByID(r.Context(), merchantID, batchID)
	if err != nil {
		http.Error(w, "batch not found", http.StatusNotFound)
		return
	}
	approval, err := a.store.BulkBatchApprovalInfo(r.Context(), merchantID, batchID)
	if err != nil {
		http.Error(w, "batch not found", http.StatusNotFound)
		return
	}
	items, err := a.store.ListBulkBatchItems(r.Context(), batchID, "", 500)
	if err != nil {
		http.Error(w, "batch unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_payable_batch.html", r, "Batch "+batch.BatchNo, map[string]any{
		"Batch": batch, "Approval": approval, "Items": bulkItemRows(items),
		"Error": r.URL.Query().Get("error"), "Approved": r.URL.Query().Get("approved") == "1",
	})
}

// merchantPayableBatchSubmit moves the draft into the approval queue.
func (a *App) merchantPayableBatchSubmit(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	if err := a.bulkPayments.SubmitDraft(r.Context(), merchantID, batchID, merchantMemberIDFromContext(r.Context())); err != nil {
		a.redirectBatchError(w, r, batchID, err)
		return
	}
	a.audit(r, "merchant.payables.batch_submitted", "bulk_batch", batchID.String(), nil)
	http.Redirect(w, r, "/merchant/payables/batches/"+batchID.String(), http.StatusSeeOther)
}

// merchantPayableBatchApprove releases the batch and starts payment.
func (a *App) merchantPayableBatchApprove(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	if err := a.bulkPayments.ApproveDraft(r.Context(), merchantID, batchID, merchantMemberIDFromContext(r.Context())); err != nil {
		a.redirectBatchError(w, r, batchID, err)
		return
	}
	a.audit(r, "merchant.payables.batch_approved", "bulk_batch", batchID.String(), nil)
	http.Redirect(w, r, "/merchant/payables/batches/"+batchID.String()+"?approved=1", http.StatusSeeOther)
}

// merchantPayableBatchReject refuses the batch with a reason.
func (a *App) merchantPayableBatchReject(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "Rejected by approver"
	}
	if err := a.bulkPayments.RejectDraft(r.Context(), merchantID, batchID, merchantMemberIDFromContext(r.Context()), reason); err != nil {
		a.redirectBatchError(w, r, batchID, err)
		return
	}
	a.audit(r, "merchant.payables.batch_rejected", "bulk_batch", batchID.String(), map[string]any{"reason": reason})
	http.Redirect(w, r, "/merchant/payables/batches/"+batchID.String(), http.StatusSeeOther)
}

func (a *App) redirectBatchError(w http.ResponseWriter, r *http.Request, batchID uuid.UUID, err error) {
	http.Redirect(w, r, "/merchant/payables/batches/"+batchID.String()+"?error="+url.QueryEscape(bulkPayableMessage(err)), http.StatusSeeOther)
}

// bulkPayableMessage turns a service error into the sentence the approver sees.
func bulkPayableMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case isServiceError(err, service.ErrBulkNoRecipients):
		return "No verified recipients match this filter."
	case isServiceError(err, service.ErrBulkInsufficientFunds):
		return "The business wallet cannot cover this batch."
	case isServiceError(err, service.ErrBulkTooManyTargets):
		return "This selection has too many recipients for one batch."
	case isServiceError(err, service.ErrBulkBankPayoutUnsupported):
		return "Bank payouts for bulk batches are not available yet. Switch this merchant to wallet payout."
	default:
		return err.Error()
	}
}

func isServiceError(err, target error) bool {
	return err != nil && target != nil && strings.Contains(err.Error(), target.Error())
}
