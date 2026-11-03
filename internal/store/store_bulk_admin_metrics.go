package store

import (
	"context"
)

// AdminBulkOverview aggregates platform-wide bulk payment metrics.
type AdminBulkOverview struct {
	TotalDisbursedKobo   int64
	PendingAmountKobo    int64
	TotalBatches         int
	CompletedBatches     int
	PendingBatches       int
	FailedBatches        int
	TotalRecipientsPaid  int64
	ActiveMerchants      int
	MerchantsWithBulk    int
}

// GetAdminBulkOverview returns platform-wide bulk payment aggregates.
func (s *Store) GetAdminBulkOverview(ctx context.Context) (AdminBulkOverview, error) {
	var o AdminBulkOverview
	err := s.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT SUM(amount_kobo*total_targets) FROM bulk_batches WHERE status='completed'),0),
			COALESCE((SELECT SUM(amount_kobo*total_targets) FROM bulk_batches WHERE status IN ('submitted','approved','processing')),0),
			COALESCE((SELECT COUNT(*) FROM bulk_batches),0),
			COALESCE((SELECT COUNT(*) FROM bulk_batches WHERE status='completed'),0),
			COALESCE((SELECT COUNT(*) FROM bulk_batches WHERE status IN ('submitted','approved','processing')),0),
			COALESCE((SELECT COUNT(*) FROM bulk_batches WHERE status='failed'),0),
			COALESCE((SELECT COUNT(*) FROM bulk_batch_items WHERE status='paid'),0),
			COALESCE((SELECT COUNT(DISTINCT merchant_id) FROM bulk_batches),0),
			COALESCE((SELECT COUNT(*) FROM merchants WHERE COALESCE(bulk_enabled,false)=true),0)
	`).Scan(&o.TotalDisbursedKobo, &o.PendingAmountKobo, &o.TotalBatches, &o.CompletedBatches,
		&o.PendingBatches, &o.FailedBatches, &o.TotalRecipientsPaid, &o.ActiveMerchants, &o.MerchantsWithBulk)
	return o, err
}

// ListAdminBulkMerchants returns bulk-enabled merchants with wallet and spend aggregates.
func (s *Store) ListAdminBulkMerchants(ctx context.Context, limit int) ([]AdminBulkMerchantRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.name, m.slug, m.active, COALESCE(m.bulk_enabled,false),
		       COALESCE(w.balance,0),
		       (SELECT COALESCE(SUM(amount_kobo*total_targets),0) FROM bulk_batches b WHERE b.merchant_id=m.id AND b.status='completed'),
		       (SELECT COUNT(*) FROM bulk_batches b WHERE b.merchant_id=m.id AND b.status='completed'),
		       (SELECT COUNT(*) FROM bulk_batches b WHERE b.merchant_id=m.id AND b.status IN ('submitted','approved','processing')),
		       (SELECT COUNT(*) FROM bulk_batches b WHERE b.merchant_id=m.id),
		       (SELECT COUNT(*) FROM bulk_batch_items i JOIN bulk_batches b ON b.id=i.batch_id WHERE b.merchant_id=m.id AND i.status='paid'),
		       (SELECT COALESCE(SUM(amount_kobo*total_targets),0) FROM bulk_batches b WHERE b.merchant_id=m.id AND b.status IN ('submitted','approved','processing')),
		       (SELECT MAX(created_at) FROM bulk_batches b WHERE b.merchant_id=m.id)
		FROM merchants m
		LEFT JOIN LATERAL (
			SELECT wb.balance
			FROM wallets w
			LEFT JOIN wallet_balances wb ON wb.account_code=w.account_code
			WHERE w.owner_type='business' AND w.owner_id=m.id AND w.status='active'
			LIMIT 1
		) w ON true
		WHERE COALESCE(m.bulk_enabled,false)=true
		ORDER BY m.name ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminBulkMerchantRow
	for rows.Next() {
		var r AdminBulkMerchantRow
		if err := rows.Scan(&r.MerchantID, &r.MerchantName, &r.MerchantSlug, &r.Active, &r.BulkEnabled,
			&r.WalletBalance, &r.TotalSpentKobo, &r.CompletedBatches, &r.PendingBatches, &r.TotalBatches,
			&r.RecipientsPaid, &r.PendingAmountKobo, &r.LastBatchAt); err != nil {
			return nil, err
		}
		r.HasWallet = true
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAdminBulkBatches returns recent batches across all merchants for tracking.
func (s *Store) ListAdminBulkBatches(ctx context.Context, limit int) ([]AdminBulkBatchRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.batch_no, b.merchant_id, m.name, b.status, b.amount_kobo, b.total_targets,
		       (SELECT COUNT(*) FROM bulk_batch_items i WHERE i.batch_id=b.id AND i.status='paid'),
		       (SELECT COUNT(*) FROM bulk_batch_items i WHERE i.batch_id=b.id AND i.status='failed'),
		       b.created_at, b.submitted_at, b.approved_at
		FROM bulk_batches b
		JOIN merchants m ON m.id=b.merchant_id
		ORDER BY b.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminBulkBatchRow
	for rows.Next() {
		var r AdminBulkBatchRow
		if err := rows.Scan(&r.BatchID, &r.BatchNo, &r.MerchantID, &r.MerchantName, &r.Status, &r.AmountKobo, &r.TotalTargets,
			&r.CompletedCount, &r.FailedCount, &r.CreatedAt, &r.SubmittedAt, &r.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
