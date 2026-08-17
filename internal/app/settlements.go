// S1: settlement + payout surfaces. Merchants self-serve through the signed
// Partner API (/api/v1/settlements, /settlement-accounts, /payouts); operators
// manage accounts, retries, and reversals through the admin console.
package app

import (
	"context"
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

	"whatsapp-payment-demo/internal/store"
)

const (
	settlementMaxBatchNoLen   = 64
	settlementMaxReasonLen    = 500
	settlementAccountMaxName  = 120
	settlementAccountMaxField = 64
)

// ---------------------------------------------------------------------------
// Partner API: merchant self-serve
// ---------------------------------------------------------------------------

type apiCreateSettlementRequest struct {
	BatchNo string `json:"batch_no"`
}

// apiCreateSettlement cuts the authenticated merchant's payable into a batch.
// Idempotent on batch_no: replaying a known batch returns it unchanged.
func (a *App) apiCreateSettlement(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	var req apiCreateSettlementRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	batchNo := strings.TrimSpace(req.BatchNo)
	if batchNo == "" || len(batchNo) > settlementMaxBatchNoLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "batch_no_invalid", "batch_no is required (max 64 characters)")
		return
	}
	batch, err := a.settlements.Cut(r.Context(), auth.merchant.ID, batchNo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "nothing to settle") {
			writeAPIError(w, http.StatusUnprocessableEntity, "nothing_to_settle", err.Error())
			return
		}
		a.logger.Error("api create settlement failed", "merchant_id", auth.merchant.ID, "batch_no", batchNo, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "settlement cut failed")
		return
	}
	a.writeSettlementJSON(w, r, batch)
}

// apiSettlementStatus returns a batch plus its lines and payout.
func (a *App) apiSettlementStatus(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	batchNo := chi.URLParam(r, "batch_no")
	batch, err := a.store.SettlementBatchByBatchNo(r.Context(), batchNo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no settlement with that batch_no")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "settlement lookup failed")
		return
	}
	if batch.MerchantID != auth.merchant.ID {
		writeAPIError(w, http.StatusNotFound, "not_found", "no settlement with that batch_no")
		return
	}
	a.writeSettlementJSON(w, r, batch)
}

type apiRequestPayoutRequest struct {
	DestinationID string `json:"destination_id"`
}

// apiRequestPayout settles a batch to the merchant's default or supplied
// destination account.
func (a *App) apiRequestPayout(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	batchNo := chi.URLParam(r, "batch_no")
	var req apiRequestPayoutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	var destinationID *uuid.UUID
	if strings.TrimSpace(req.DestinationID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(req.DestinationID))
		if err != nil {
			writeAPIError(w, http.StatusUnprocessableEntity, "destination_id_invalid", "destination_id is not a valid UUID")
			return
		}
		destinationID = &id
	}
	payout, err := a.settlements.RequestPayout(r.Context(), batchNo, destinationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no batch or destination account with that reference")
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "payout_failed", err.Error())
		return
	}
	if payout.MerchantID != auth.merchant.ID {
		writeAPIError(w, http.StatusNotFound, "not_found", "no settlement with that batch_no")
		return
	}
	a.writePayoutJSON(w, payout)
}

// apiPayoutStatus returns one payout by id or batch reference.
func (a *App) apiPayoutStatus(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	payout, err := a.store.PayoutByBatchNo(r.Context(), reference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "no payout for that batch_no")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal", "payout lookup failed")
		return
	}
	if payout.MerchantID != auth.merchant.ID {
		writeAPIError(w, http.StatusNotFound, "not_found", "no payout for that batch_no")
		return
	}
	a.writePayoutJSON(w, payout)
}

type apiReversePayoutRequest struct {
	Reason string `json:"reason"`
}

// apiReversePayout reverses a failed payout so the batch can be dispatched to
// a new destination.
func (a *App) apiReversePayout(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	reference := chi.URLParam(r, "reference")
	var req apiReversePayoutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > settlementMaxReasonLen {
		writeAPIError(w, http.StatusUnprocessableEntity, "reason_invalid", "reason is required (max 500 characters)")
		return
	}
	payout, err := a.store.PayoutByBatchNo(r.Context(), reference)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "no payout for that batch_no")
		return
	}
	if payout.MerchantID != auth.merchant.ID {
		writeAPIError(w, http.StatusNotFound, "not_found", "no payout for that batch_no")
		return
	}
	if err := a.settlements.Reverse(r.Context(), payout.ID, reason); err != nil {
		writeAPIError(w, http.StatusConflict, "payout_not_reversible", err.Error())
		return
	}
	a.audit(r, "api.payout.reversed", "payout", payout.ID.String(), map[string]any{
		"merchant_id": payout.MerchantID.String(), "batch_no": reference, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": payout.ID.String(), "batch_no": reference, "status": store.PayoutReversed,
	})
}

type apiCreateSettlementAccountRequest struct {
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
}

// apiCreateSettlementAccount registers a payout destination for the merchant.
func (a *App) apiCreateSettlementAccount(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	var req apiCreateSettlementAccountRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, apiKeyMaxBodyBytes)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	bankCode := strings.TrimSpace(req.BankCode)
	accountNumber := strings.TrimSpace(req.AccountNumber)
	accountName := strings.TrimSpace(req.AccountName)
	if bankCode == "" || len(bankCode) > settlementAccountMaxField || accountNumber == "" || len(accountNumber) > settlementAccountMaxField {
		writeAPIError(w, http.StatusUnprocessableEntity, "account_invalid", "bank_code and account_number are required")
		return
	}
	if accountName == "" || len(accountName) > settlementAccountMaxName {
		writeAPIError(w, http.StatusUnprocessableEntity, "account_invalid", "account_name is required")
		return
	}
	account, err := a.store.CreateSettlementAccount(r.Context(), auth.merchant.ID, bankCode, accountNumber, accountName)
	if err != nil {
		a.logger.Error("api create settlement account failed", "merchant_id", auth.merchant.ID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal", "settlement account registration failed")
		return
	}
	a.audit(r, "api.settlement_account.created", "settlement_account", account.ID.String(), map[string]any{
		"merchant_id": auth.merchant.ID.String(), "bank_code": bankCode, "account_number": accountNumber,
	})
	a.writeSettlementAccountJSON(w, account)
}

// apiListSettlementAccounts returns the merchant's payout destinations.
func (a *App) apiListSettlementAccounts(w http.ResponseWriter, r *http.Request) {
	auth, _ := apiKeyAuthFromContext(r.Context())
	accounts, err := a.store.ListSettlementAccounts(r.Context(), auth.merchant.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "settlement account lookup failed")
		return
	}
	view := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		view = append(view, a.settlementAccountJSON(account))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": view})
}

// ---------------------------------------------------------------------------
// JSON views
// ---------------------------------------------------------------------------

func (a *App) writeSettlementJSON(w http.ResponseWriter, r *http.Request, batch store.SettlementBatch) {
	lines, err := a.store.SettlementLinesByBatch(r.Context(), batch.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "settlement lookup failed")
		return
	}
	response := a.settlementBatchJSON(batch, lines)
	if payout, err := a.store.PayoutByBatchID(r.Context(), batch.ID); err == nil {
		response["payout"] = a.payoutJSON(payout)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) settlementBatchJSON(batch store.SettlementBatch, lines []store.SettlementLine) map[string]any {
	lineView := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		lineView = append(lineView, map[string]any{
			"payment_id":  line.PaymentID.String(),
			"amount_kobo": line.AmountKobo,
		})
	}
	return map[string]any{
		"id":             batch.ID.String(),
		"batch_no":       batch.BatchNo,
		"merchant_id":    batch.MerchantID.String(),
		"status":         batch.Status,
		"amount":         map[string]any{"value": batch.TotalKobo, "currency": "NGN"},
		"fee":            map[string]any{"value": batch.FeeKobo, "currency": "NGN", "bps": batch.FeeBps},
		"payout_amount":  map[string]any{"value": batch.TotalKobo - batch.FeeKobo, "currency": "NGN"},
		"line_count":     batch.LineCount,
		"lines":          lineView,
		"ledger_journal": batch.LedgerJournal,
		"cutoff_at":      batch.CutoffAt.UTC().Format(time.RFC3339),
		"created_at":     batch.CreatedAt.UTC().Format(time.RFC3339),
		"processed_at":   formatPtrTime(batch.ProcessedAt),
	}
}

func (a *App) writePayoutJSON(w http.ResponseWriter, payout store.Payout) {
	writeJSON(w, http.StatusOK, a.payoutJSON(payout))
}

func (a *App) payoutJSON(payout store.Payout) map[string]any {
	return map[string]any{
		"id":             payout.ID.String(),
		"batch_no":       payout.BatchID.String(),
		"merchant_id":    payout.MerchantID.String(),
		"status":         payout.Status,
		"amount":         map[string]any{"value": payout.AmountKobo, "currency": "NGN"},
		"provider":       payout.Provider,
		"external_ref":   payout.ExternalRef,
		"attempts":       payout.Attempts,
		"last_error":     payout.LastError,
		"ledger_journal": payout.LedgerJournal,
		"created_at":     payout.CreatedAt.UTC().Format(time.RFC3339),
		"completed_at":   formatPtrTime(payout.CompletedAt),
	}
}

func (a *App) writeSettlementAccountJSON(w http.ResponseWriter, account store.MerchantSettlementAccount) {
	writeJSON(w, http.StatusOK, a.settlementAccountJSON(account))
}

func (a *App) settlementAccountJSON(account store.MerchantSettlementAccount) map[string]any {
	return map[string]any{
		"id":             account.ID.String(),
		"bank_code":      account.BankCode,
		"account_number": account.AccountNumber,
		"account_name":   account.AccountName,
		"is_default":     account.IsDefault,
		"status":         account.Status,
		"approved":       account.ApprovedBy != nil,
		"created_at":     account.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func formatPtrTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Admin console
// ---------------------------------------------------------------------------

// adminSettlements renders every settlement account, batch, and payout for
// operator management.
func (a *App) adminSettlements(w http.ResponseWriter, r *http.Request) {
	accounts, err := a.store.ListAllSettlementAccounts(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "list settlement accounts", "error", err)
		http.Error(w, "settlements unavailable", http.StatusInternalServerError)
		return
	}
	batches, err := a.store.ListAllSettlementBatches(r.Context(), 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list settlement batches", "error", err)
		http.Error(w, "settlements unavailable", http.StatusInternalServerError)
		return
	}
	payouts, err := a.store.ListAllPayouts(r.Context(), 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list payouts", "error", err)
		http.Error(w, "settlements unavailable", http.StatusInternalServerError)
		return
	}
	merchantNames := map[uuid.UUID]string{}
	accountMerchants := map[uuid.UUID]string{}
	for _, account := range accounts {
		name, err := a.store.MerchantByID(r.Context(), account.MerchantID)
		if err == nil {
			merchantNames[account.MerchantID] = name.Name
			accountMerchants[account.ID] = name.Name
		}
	}
	a.renderAdmin(w, "admin_settlements.html", r, "Settlements", map[string]any{
		"Accounts":         accounts,
		"Batches":          batches,
		"Payouts":          payouts,
		"MerchantNames":    merchantNames,
		"AccountMerchants": accountMerchants,
		"Result":           r.URL.Query().Get("result"),
		"Error":            r.URL.Query().Get("error"),
	})
}

// adminApproveSettlementAccount approves a merchant payout destination.
func (a *App) adminApproveSettlementAccount(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	accountID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}
	adminID := adminIDFromContext(r.Context())
	if err := a.store.ApproveSettlementAccount(r.Context(), accountID, adminID); err != nil {
		a.logger.WarnContext(r.Context(), "approve settlement account", "account_id", accountID, "error", err)
		http.Redirect(w, r, "/admin/settlements?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.settlement_account.approved", "settlement_account", accountID.String(), nil)
	http.Redirect(w, r, "/admin/settlements?result=approved", http.StatusSeeOther)
}

// adminDisableSettlementAccount disables a payout destination.
func (a *App) adminDisableSettlementAccount(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	accountID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}
	if err := a.store.DisableSettlementAccount(r.Context(), accountID); err != nil {
		a.logger.WarnContext(r.Context(), "disable settlement account", "account_id", accountID, "error", err)
		http.Redirect(w, r, "/admin/settlements?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.settlement_account.disabled", "settlement_account", accountID.String(), nil)
	http.Redirect(w, r, "/admin/settlements?result=disabled", http.StatusSeeOther)
}

// adminRetryPayout re-queues a failed payout for another attempt.
func (a *App) adminRetryPayout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	payoutID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid payout id", http.StatusBadRequest)
		return
	}
	if err := a.settlements.Retry(r.Context(), payoutID); err != nil {
		a.logger.WarnContext(r.Context(), "retry payout", "payout_id", payoutID, "error", err)
		http.Redirect(w, r, "/admin/settlements?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.payout.retried", "payout", payoutID.String(), nil)
	http.Redirect(w, r, "/admin/settlements?result=retried", http.StatusSeeOther)
}

// adminReversePayout reverses a failed payout, reopening the batch.
func (a *App) adminReversePayout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	payoutID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid payout id", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		http.Redirect(w, r, "/admin/settlements?error="+url.QueryEscape("reason is required"), http.StatusSeeOther)
		return
	}
	if err := a.settlements.Reverse(r.Context(), payoutID, reason); err != nil {
		a.logger.WarnContext(r.Context(), "reverse payout", "payout_id", payoutID, "error", err)
		http.Redirect(w, r, "/admin/settlements?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.payout.reversed", "payout", payoutID.String(), map[string]any{"reason": reason})
	http.Redirect(w, r, "/admin/settlements?result=reversed", http.StatusSeeOther)
}

// runSettlementDispatcher processes queued payouts and requeues stale ones.
func (a *App) runSettlementDispatcher(ctx context.Context) {
	if err := a.settlements.DispatchPending(ctx); err != nil {
		a.logger.WarnContext(ctx, "settlement dispatcher failed", "error", err)
	}
	if requeued, err := a.store.RequeueStalePayouts(ctx, 30*time.Minute); err != nil {
		a.logger.WarnContext(ctx, "requeue stale payouts failed", "error", err)
	} else if requeued > 0 {
		a.logger.InfoContext(ctx, "requeued stale payouts", "count", requeued)
	}
}
