// C15: regulator report extraction. These methods read audited payment and
// screening data into the report row types defined in internal/reports, so the
// compliance tooling and admin surface can emit STR / CTR / PEP filings.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/reports"
)

// STRReport extracts suspicious-transaction report rows from transaction
// monitoring alerts (optionally scoped to alerts at or after since, newest
// first). Payment and merchant data are joined in so each STR carries the
// audited transaction reference and counterparty.
func (s *Store) STRReport(ctx context.Context, since time.Time, limit int) ([]reports.STRRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ta.user_id, ta.payment_id, ta.rule, ta.severity, ta.status, ta.created_at,
		       COALESCE(u.display_name,''), COALESCE(u.whatsapp_number,''),
		       COALESCE(p.provider_reference,''), COALESCE(p.amount_kobo,0),
		       COALESCE(p.currency,''), COALESCE(m.name,''), COALESCE(p.paid_at, p.created_at)
		FROM transaction_alerts ta
		LEFT JOIN users u ON u.id = ta.user_id
		LEFT JOIN payments p ON p.id = ta.payment_id
		LEFT JOIN merchants m ON m.id = p.merchant_id
		WHERE ta.created_at >= $1
		ORDER BY ta.created_at DESC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("STR report query: %w", err)
	}
	defer rows.Close()
	var out []reports.STRRow
	for rows.Next() {
		var (
			r         reports.STRRow
			paymentID *uuid.UUID
			txnDate   time.Time
		)
		if err := rows.Scan(&r.CustomerID, &paymentID, &r.AlertRule, &r.AlertSeverity,
			&r.AlertStatus, &r.ReportDate, &r.CustomerName, &r.CustomerPhone,
			&r.ProviderReference, &r.AmountKobo, &r.Currency, &r.MerchantName, &txnDate); err != nil {
			return nil, fmt.Errorf("STR report scan: %w", err)
		}
		r.TransactionDate = txnDate
		if paymentID != nil {
			r.TransactionID = *paymentID
		}
		r.ReportDate = r.ReportDate.UTC()
		r.TransactionDate = r.TransactionDate.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CTRReport extracts currency transaction report rows: succeeded payments at
// or above thresholdKobo (defaulted to the ₦10,000,000 cash-reporting
// threshold when zero) inside the reporting window, newest first. CBN AML/CFT
// regulations require currency transaction reports for large-value activity.
func (s *Store) CTRReport(ctx context.Context, since time.Time, thresholdKobo int64, limit int) ([]reports.CTRRow, error) {
	if thresholdKobo <= 0 {
		thresholdKobo = 1_000_000_000 // ₦10,000,000
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.user_id, COALESCE(u.display_name,''), COALESCE(u.whatsapp_number,''),
		       COALESCE(p.provider_reference,''), p.amount_kobo, COALESCE(p.currency,''),
		       COALESCE(m.name,''), COALESCE(p.paid_at, p.created_at)
		FROM payments p
		JOIN users u ON u.id = p.user_id
		JOIN merchants m ON m.id = p.merchant_id
		WHERE p.status = 'succeeded' AND p.amount_kobo >= $1 AND COALESCE(p.paid_at, p.created_at) >= $2
		ORDER BY COALESCE(p.paid_at, p.created_at) DESC
		LIMIT $3`, thresholdKobo, since, limit)
	if err != nil {
		return nil, fmt.Errorf("CTR report query: %w", err)
	}
	defer rows.Close()
	var out []reports.CTRRow
	for rows.Next() {
		var r reports.CTRRow
		if err := rows.Scan(&r.TransactionID, &r.CustomerID, &r.CustomerName, &r.CustomerPhone,
			&r.ProviderReference, &r.AmountKobo, &r.Currency, &r.MerchantName, &r.TransactionDate); err != nil {
			return nil, fmt.Errorf("CTR report scan: %w", err)
		}
		r.ReportDate = time.Now().UTC()
		r.TransactionDate = r.TransactionDate.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// PEPReport extracts the politically exposed persons list from the most
// recent screening result per customer whose decision is possible, strong, or
// blocked (i.e. not clear and not manually cleared), with matched names and
// the customer's KYC tier and risk band.
func (s *Store) PEPReport(ctx context.Context, limit int) ([]reports.PEPRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT s.user_id, s.decision, s.matched_names::text, s.screened_at,
		       COALESCE(u.display_name,''), COALESCE(u.whatsapp_number,''),
		       COALESCE(k.tier,''), COALESCE(k.risk_band,'')
		FROM screening_results s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN kyc_profiles k ON k.user_id = s.user_id
		WHERE s.decision IN ('possible','strong','blocked')
		  AND s.screened_at = (
		      SELECT MAX(s2.screened_at) FROM screening_results s2 WHERE s2.user_id = s.user_id
		  )
		ORDER BY s.screened_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("PEP report query: %w", err)
	}
	defer rows.Close()
	var out []reports.PEPRow
	for rows.Next() {
		var (
			r            reports.PEPRow
			matchedNames []byte
		)
		if err := rows.Scan(&r.CustomerID, &r.Decision, &matchedNames, &r.ScreenedAt,
			&r.CustomerName, &r.CustomerPhone, &r.KYCTier, &r.RiskBand); err != nil {
			return nil, fmt.Errorf("PEP report scan: %w", err)
		}
		_ = json.Unmarshal(matchedNames, &r.MatchedNames)
		if r.MatchedNames == nil {
			r.MatchedNames = []string{}
		}
		r.ReportDate = time.Now().UTC()
		r.ScreenedAt = r.ScreenedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
