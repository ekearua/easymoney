// S5: settlement and revenue analytics. Read-only aggregate queries that
// power the /admin/analytics dashboard. All functions are pure queries over
// existing tables — no schema changes.
package store

import (
	"context"
	"fmt"
	"time"
)

// RevenueDay is one day's revenue summary.
type RevenueDay struct {
	Date          time.Time `json:"date"`
	PaymentsCount int       `json:"payments_count"`
	Collections   int64     `json:"collections"`
	Fees          int64     `json:"fees"`
	Refunds       int64     `json:"refunds"`
	NetRevenue    int64     `json:"net_revenue"`
}

// MerchantSettlementRow is one merchant's settlement summary.
type MerchantSettlementRow struct {
	MerchantID   string `json:"merchant_id"`
	MerchantName string `json:"merchant_name"`
	BatchCount   int    `json:"batch_count"`
	TotalKobo    int64  `json:"total_kobo"`
	FeeKobo      int64  `json:"fee_kobo"`
	PayoutKobo   int64  `json:"payout_kobo"`
	PayoutCount  int    `json:"payout_count"`
}

// PayoutReportRow is one payout in the report.
type PayoutReportRow struct {
	PayoutID    string     `json:"payout_id"`
	BatchNo     string     `json:"batch_no"`
	MerchantID  string     `json:"merchant_id"`
	AmountKobo  int64      `json:"amount_kobo"`
	Status      string     `json:"status"`
	ExternalRef string     `json:"external_ref"`
	Provider    string     `json:"provider"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// TransactionVolumeRow is one day's payment volume.
type TransactionVolumeRow struct {
	Date        time.Time `json:"date"`
	Status      string    `json:"status"`
	Count       int       `json:"count"`
	TotalKobo   int64     `json:"total_kobo"`
	AverageKobo int64     `json:"average_kobo"`
}

// RevenueSummary returns daily revenue (collections, fees, refunds) for the
// given time window. Each row aggregates one calendar day UTC.
func (s *Store) RevenueSummary(ctx context.Context, from, to time.Time) ([]RevenueDay, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d::date AS date,
		       COALESCE(p.payments, 0),
		       COALESCE(p.collections, 0),
		       COALESCE(f.fees, 0),
		       COALESCE(r.refunds, 0),
		       COALESCE(p.collections, 0) - COALESCE(r.refunds, 0) AS net
		FROM generate_series($1::timestamptz, $2::timestamptz, '1 day') d
		LEFT JOIN LATERAL (
			SELECT count(*) AS payments, SUM(amount_kobo) AS collections
			FROM payments WHERE status='succeeded'
			  AND created_at >= d AND created_at < d + interval '1 day'
		) p ON true
		LEFT JOIN LATERAL (
			SELECT COALESCE(SUM(amount_kobo), 0) AS fees
			FROM ledger_entries
			WHERE account='5200_settlement_fees' AND entry_type='credit'
			  AND created_at >= d AND created_at < d + interval '1 day'
		) f ON true
		LEFT JOIN LATERAL (
			SELECT COALESCE(SUM(le.amount_kobo), 0) AS refunds
			FROM refunds rf
			JOIN ledger_entries le ON le.source_id=rf.id::text AND le.source_type='settlement'
			WHERE le.entry_type='debit' AND le.account='3200_settlement_payable'
			  AND le.created_at >= d AND le.created_at < d + interval '1 day'
		) r ON true
		ORDER BY d`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []RevenueDay
	for rows.Next() {
		var day RevenueDay
		if err := rows.Scan(&day.Date, &day.PaymentsCount, &day.Collections, &day.Fees, &day.Refunds, &day.NetRevenue); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// MerchantSettlementSummary returns one row per merchant showing their
// settlement batch totals, fees, and payout amounts.
func (s *Store) MerchantSettlementSummary(ctx context.Context) ([]MerchantSettlementRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.name,
		       count(DISTINCT sb.id) AS batches,
		       COALESCE(SUM(sb.total_kobo), 0) AS total_kobo,
		       COALESCE(SUM(sb.fee_kobo), 0) AS fee_kobo,
		       COALESCE(SUM(p.amount_kobo), 0) AS payout_kobo,
		       count(DISTINCT p.id) AS payouts
		FROM merchants m
		LEFT JOIN settlement_batches sb ON sb.merchant_id=m.id
		LEFT JOIN payouts p ON p.batch_id=sb.id AND p.status='completed'
		GROUP BY m.id, m.name
		ORDER BY total_kobo DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MerchantSettlementRow
	for rows.Next() {
		var r MerchantSettlementRow
		if err := rows.Scan(&r.MerchantID, &r.MerchantName, &r.BatchCount, &r.TotalKobo, &r.FeeKobo, &r.PayoutKobo, &r.PayoutCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PayoutReport returns all payouts (or a filtered subset) for the report.
func (s *Store) PayoutReport(ctx context.Context, from, to *time.Time, limit int) ([]PayoutReportRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}

	var args []any
	idx := 1
	argf := func(v any) string {
		args = append(args, v)
		p := fmt.Sprintf("$%d", idx)
		idx++
		return p
	}

	query := `SELECT p.id::text, COALESCE(sb.batch_no,''), p.merchant_id::text,
	                 p.amount_kobo, p.status, COALESCE(p.external_ref,''), p.provider,
	                 p.created_at, p.completed_at
	          FROM payouts p
	          LEFT JOIN settlement_batches sb ON sb.id=p.batch_id
	          WHERE true`
	if from != nil {
		query += " AND p.created_at >= " + argf(*from)
	}
	if to != nil {
		query += " AND p.created_at <= " + argf(*to)
	}
	query += " ORDER BY p.created_at DESC LIMIT " + argf(limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PayoutReportRow
	for rows.Next() {
		var r PayoutReportRow
		if err := rows.Scan(&r.PayoutID, &r.BatchNo, &r.MerchantID, &r.AmountKobo,
			&r.Status, &r.ExternalRef, &r.Provider, &r.CreatedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TransactionVolume returns daily payment volume grouped by status.
func (s *Store) TransactionVolume(ctx context.Context, from, to time.Time) ([]TransactionVolumeRow, error) {
	end := to.AddDate(0, 0, 1)
	rows, err := s.pool.Query(ctx, `
		SELECT created_at::date AS date, status, count(*), SUM(amount_kobo),
		       CASE WHEN count(*)>0 THEN SUM(amount_kobo)/count(*) ELSE 0 END
		FROM payments
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY created_at::date, status
		ORDER BY created_at::date, status`, from, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransactionVolumeRow
	for rows.Next() {
		var r TransactionVolumeRow
		if err := rows.Scan(&r.Date, &r.Status, &r.Count, &r.TotalKobo, &r.AverageKobo); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
