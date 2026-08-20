// S5: admin analytics dashboard. Read-only settlement and revenue reports
// with CSV export for data analysis.
package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"whatsapp-payment-demo/internal/store"
)

// adminAnalytics renders the settlement and revenue analytics dashboard.
func (a *App) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	defaultFrom := now.AddDate(0, 0, -30) // last 30 days

	from := defaultFrom
	if s := r.URL.Query().Get("from"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			from = t
		}
	}
	to := now
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			to = t.Add(24*time.Hour - time.Second) // end of day
		}
	}

	revenue, err := a.store.RevenueSummary(r.Context(), from, to)
	if err != nil {
		a.logger.WarnContext(r.Context(), "revenue summary", "error", err)
		revenue = []store.RevenueDay{}
	}

	merchants, err := a.store.MerchantSettlementSummary(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "merchant settlement summary", "error", err)
		merchants = []store.MerchantSettlementRow{}
	}

	payouts, err := a.store.PayoutReport(r.Context(), &from, &to, 500)
	if err != nil {
		a.logger.WarnContext(r.Context(), "payout report", "error", err)
		payouts = []store.PayoutReportRow{}
	}

	volume, err := a.store.TransactionVolume(r.Context(), from, to)
	if err != nil {
		a.logger.WarnContext(r.Context(), "transaction volume", "error", err)
		volume = []store.TransactionVolumeRow{}
	}

	// Compute summary cards.
	var totalCollections, totalFees, totalRefunds, totalPayouts int64
	var totalPayments, totalBatches int
	for _, d := range revenue {
		totalCollections += d.Collections
		totalFees += d.Fees
		totalRefunds += d.Refunds
		totalPayments += d.PaymentsCount
	}
	for _, m := range merchants {
		totalBatches += m.BatchCount
		totalPayouts += m.PayoutKobo
	}

	a.renderAdmin(w, "admin_analytics.html", r, "Analytics", map[string]any{
		"From":             from,
		"To":               to,
		"Revenue":          revenue,
		"Merchants":        merchants,
		"Payouts":          payouts,
		"Volume":           volume,
		"TotalPayments":    totalPayments,
		"TotalBatches":     totalBatches,
		"TotalCollections": totalCollections,
		"TotalFees":        totalFees,
		"TotalRefunds":     totalRefunds,
		"TotalPayouts":     totalPayouts,
		"NetRevenue":       totalFees - totalRefunds,
	})
}

// adminAnalyticsExport writes analytics data as CSV or JSON download.
func (a *App) adminAnalyticsExport(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30)
	to := now
	if s := r.URL.Query().Get("from"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			from = t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			to = t.Add(24*time.Hour - time.Second)
		}
	}

	report := r.URL.Query().Get("report")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}

	filename := fmt.Sprintf("analytics-%s-%s.%s", report, now.Format("20060102"), format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	switch report {
	case "revenue":
		data, err := a.store.RevenueSummary(r.Context(), from, to)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		if format == "json" {
			w.Header().Set("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			if err := enc.Encode(data); err != nil {
				a.logger.WarnContext(r.Context(), "json encode revenue", "error", err)
				return
			}
		} else {
			w.Header().Set("Content-Type", "text/csv")
			wr := csv.NewWriter(w)
			_ = wr.Write([]string{"date", "payments", "collections_kobo", "fees_kobo", "refunds_kobo", "net_revenue_kobo"})
			for _, d := range data {
				_ = wr.Write([]string{
					d.Date.Format("2006-01-02"),
					fmt.Sprintf("%d", d.PaymentsCount),
					fmt.Sprintf("%d", d.Collections),
					fmt.Sprintf("%d", d.Fees),
					fmt.Sprintf("%d", d.Refunds),
					fmt.Sprintf("%d", d.NetRevenue),
				})
			}
			wr.Flush()
			if err := wr.Error(); err != nil {
				a.logger.WarnContext(r.Context(), "csv write revenue", "error", err)
				return
			}
		}
	case "merchants":
		data, err := a.store.MerchantSettlementSummary(r.Context())
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		if format == "json" {
			w.Header().Set("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			if err := enc.Encode(data); err != nil {
				a.logger.WarnContext(r.Context(), "json encode merchants", "error", err)
				return
			}
		} else {
			w.Header().Set("Content-Type", "text/csv")
			wr := csv.NewWriter(w)
			_ = wr.Write([]string{"merchant_id", "merchant_name", "batches", "total_kobo", "fee_kobo", "payout_kobo", "payouts"})
			for _, m := range data {
				_ = wr.Write([]string{m.MerchantID, m.MerchantName, fmt.Sprintf("%d", m.BatchCount),
					fmt.Sprintf("%d", m.TotalKobo), fmt.Sprintf("%d", m.FeeKobo),
					fmt.Sprintf("%d", m.PayoutKobo), fmt.Sprintf("%d", m.PayoutCount)})
			}
			wr.Flush()
			if err := wr.Error(); err != nil {
				a.logger.WarnContext(r.Context(), "csv write merchants", "error", err)
				return
			}
		}
	case "payouts":
		data, err := a.store.PayoutReport(r.Context(), &from, &to, 5000)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		if format == "json" {
			w.Header().Set("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			if err := enc.Encode(data); err != nil {
				a.logger.WarnContext(r.Context(), "json encode payouts", "error", err)
				return
			}
		} else {
			w.Header().Set("Content-Type", "text/csv")
			wr := csv.NewWriter(w)
			_ = wr.Write([]string{"payout_id", "batch_no", "merchant_id", "amount_kobo", "status", "external_ref", "provider", "created_at", "completed_at"})
			for _, p := range data {
				completed := ""
				if p.CompletedAt != nil {
					completed = p.CompletedAt.UTC().Format(time.RFC3339)
				}
				_ = wr.Write([]string{p.PayoutID, p.BatchNo, p.MerchantID, fmt.Sprintf("%d", p.AmountKobo),
					p.Status, p.ExternalRef, p.Provider, p.CreatedAt.UTC().Format(time.RFC3339), completed})
			}
			wr.Flush()
			if err := wr.Error(); err != nil {
				a.logger.WarnContext(r.Context(), "csv write payouts", "error", err)
				return
			}
		}
	case "volume":
		data, err := a.store.TransactionVolume(r.Context(), from, to)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		if format == "json" {
			w.Header().Set("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			if err := enc.Encode(data); err != nil {
				a.logger.WarnContext(r.Context(), "json encode volume", "error", err)
				return
			}
		} else {
			w.Header().Set("Content-Type", "text/csv")
			wr := csv.NewWriter(w)
			_ = wr.Write([]string{"date", "status", "count", "total_kobo", "average_kobo"})
			for _, v := range data {
				_ = wr.Write([]string{v.Date.Format("2006-01-02"), v.Status,
					fmt.Sprintf("%d", v.Count), fmt.Sprintf("%d", v.TotalKobo), fmt.Sprintf("%d", v.AverageKobo)})
			}
			wr.Flush()
			if err := wr.Error(); err != nil {
				a.logger.WarnContext(r.Context(), "csv write volume", "error", err)
				return
			}
		}
	default:
		http.Error(w, "unknown report", http.StatusBadRequest)
	}
	a.audit(r, "admin.analytics.exported", "analytics", report, map[string]any{"format": format})
}
