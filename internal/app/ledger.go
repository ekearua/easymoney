// C16: double-entry append-only journal admin surface. The /admin/ledger page
// shows recent postings, the chart-of-accounts balance (which must sum to
// zero), and hash-chain integrity. Admins can post an offsetting reversal for
// a journal reference, which unwinds it without mutating original rows.
package app

import (
	"net/http"
	"net/url"
	"strings"

	"whatsapp-payment-demo/internal/store"
)

// adminLedger renders the journal with balances and chain verification.
func (a *App) adminLedger(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListLedgerEntries(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list ledger entries", "error", err)
		http.Error(w, "ledger unavailable", http.StatusInternalServerError)
		return
	}
	balances, err := a.store.LedgerBalanceSummary(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "ledger balance summary", "error", err)
		http.Error(w, "ledger unavailable", http.StatusInternalServerError)
		return
	}
	count, broken, err := a.store.VerifyLedgerChain(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "verify ledger chain", "error", err)
		http.Error(w, "ledger unavailable", http.StatusInternalServerError)
		return
	}
	var total int64
	for _, b := range balances {
		total += b.NetKobo
	}
	a.renderAdmin(w, "admin_ledger.html", r, "Ledger", map[string]any{
		"Entries":        entries,
		"Balances":       balances,
		"ChainCount":     count,
		"ChainBroken":    broken,
		"ChainSound":     broken < 0,
		"NetTotalKobo":   total,
		"AccountNames":   ledgerAccountNames(),
		"ReversalResult": r.URL.Query().Get("result"),
		"ReversalError":  r.URL.Query().Get("error"),
	})
}

// adminLedgerReverse posts offsetting entries for a journal reference so the
// book returns to zero for that reference (C16 reversals via offsetting
// entries).
func (a *App) adminLedgerReverse(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	journalRef := strings.TrimSpace(r.FormValue("journal_ref"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if journalRef == "" || reason == "" {
		http.Error(w, "journal reference and reason are required", http.StatusBadRequest)
		return
	}
	adminEmail := adminEmailFromContext(r.Context())
	reversed, err := a.store.PostLedgerReversal(r.Context(), journalRef, reason, adminEmail)
	if err != nil {
		a.logger.WarnContext(r.Context(), "ledger reversal failed", "journal_ref", journalRef, "error", err)
		http.Redirect(w, r, "/admin/ledger?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(r, "admin.ledger.reversed", "ledger", journalRef, map[string]any{"entries": reversed, "reason": reason})
	a.logger.InfoContext(r.Context(), "ledger reversal posted", "journal_ref", journalRef, "entries", reversed)
	http.Redirect(w, r, "/admin/ledger?result=reversed", http.StatusSeeOther)
}

func ledgerAccountNames() map[string]string {
	return map[string]string{
		store.LedgerAccountOperatingBank:   "Operating bank",
		store.LedgerAccountCustomerFloat:   "Customer float",
		store.LedgerAccountMerchantPayable: "Merchant payable",
		store.LedgerAccountSalesRevenue:    "Sales revenue",
		store.LedgerAccountProviderCost:    "Provider cost",
		store.LedgerAccountThriftPool:      "Thrift pool",
	}
}
