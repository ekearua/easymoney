package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/fraud"
)

// RunFraudMonitor evaluates the fraud rules (G13) over the lookback window and
// records detections on the fraud lane. Events are attributed to a user — the
// payer for payments, the merchant's primary owner for payouts, disputes and
// refunds (mirroring the AML monitor's merchant-owner attribution) — so a
// merchant-side signal lands on the person able to act on it. Fraud alerts are
// deduped by rule + rail reference and never write a risk_events row: the
// ML/FT score stays AML-only.
func (s *Store) RunFraudMonitor(ctx context.Context, since time.Time, cfg fraud.FraudConfig) (int, error) {
	inputs, err := s.fraudInputs(ctx, since, time.Now().UTC().Add(time.Hour), cfg.NewDestinationWindow)
	if err != nil {
		return 0, err
	}
	byUser := map[uuid.UUID][]fraud.FraudEvent{}
	var order []uuid.UUID
	for _, e := range inputs {
		if _, seen := byUser[e.SubjectID]; !seen {
			order = append(order, e.SubjectID)
		}
		byUser[e.SubjectID] = append(byUser[e.SubjectID], e)
	}
	raised := 0
	for _, userID := range order {
		count, err := s.recordFraudAlerts(ctx, userID, fraud.RunFraudMonitor(byUser[userID], cfg))
		raised += count
		if err != nil {
			return raised, err
		}
	}
	return raised, nil
}

// fraudInputs loads the fraud-rule events inside [since, before): successful
// and failed payments, disputes, refunds and completed payouts. Payments are
// attributed to the payer; the merchant-side rails are attributed to the
// merchant's primary owner. A payout's NewDestination flag is computed here
// from its destination account's created_at against the configured window, so
// the rule engine stays a pure function of its input.
func (s *Store) fraudInputs(ctx context.Context, since, before time.Time, destWindow time.Duration) ([]fraud.FraudEvent, error) {
	rows, err := s.pool.Query(ctx, `
		WITH events AS (
			SELECT 'payment' AS rail, p.id::text AS reference, p.user_id,
			       p.amount_kobo, (p.status='failed') AS failed,
			       COALESCE(p.paid_at, p.updated_at, p.created_at) AS occurred_at,
			       NULL::timestamptz AS destination_created_at
			FROM payments p
			WHERE p.status IN ('succeeded','failed')
			  AND COALESCE(p.paid_at, p.updated_at, p.created_at) >= $1
			  AND COALESCE(p.paid_at, p.updated_at, p.created_at) < $2

			UNION ALL

			SELECT 'dispute', d.id::text, mo.user_id,
			       COALESCE(p.amount_kobo, 0), false, d.created_at, NULL
			FROM disputes d
			JOIN payments p ON p.id = d.payment_id
			JOIN LATERAL (
				SELECT user_id FROM merchant_owners
				WHERE merchant_id = d.merchant_id
				ORDER BY created_at LIMIT 1
			) mo ON true
			WHERE d.created_at >= $1 AND d.created_at < $2

			UNION ALL

			SELECT 'refund', r.id::text, mo.user_id,
			       r.amount_kobo, false, COALESCE(r.completed_at, r.created_at), NULL
			FROM refunds r
			JOIN LATERAL (
				SELECT user_id FROM merchant_owners
				WHERE merchant_id = r.merchant_id
				ORDER BY created_at LIMIT 1
			) mo ON true
			WHERE r.status='succeeded'
			  AND COALESCE(r.completed_at, r.created_at) >= $1
			  AND COALESCE(r.completed_at, r.created_at) < $2

			UNION ALL

			SELECT 'payout', po.id::text, mo.user_id,
			       po.amount_kobo, false, COALESCE(po.completed_at, po.created_at),
			       msa.created_at
			FROM payouts po
			JOIN merchant_settlement_accounts msa ON msa.id = po.destination_id
			JOIN LATERAL (
				SELECT user_id FROM merchant_owners
				WHERE merchant_id = po.merchant_id
				ORDER BY created_at LIMIT 1
			) mo ON true
			WHERE po.status='completed'
			  AND COALESCE(po.completed_at, po.created_at) >= $1
			  AND COALESCE(po.completed_at, po.created_at) < $2
		)
		SELECT rail, reference, user_id, amount_kobo, failed, occurred_at, destination_created_at
		FROM events
		ORDER BY user_id, occurred_at`, since, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []fraud.FraudEvent
	for rows.Next() {
		var e fraud.FraudEvent
		var dest *time.Time
		if err := rows.Scan(&e.Rail, &e.Reference, &e.SubjectID, &e.AmountKobo, &e.Failed, &e.OccurredAt, &dest); err != nil {
			return nil, err
		}
		e.SubjectKind = fraud.SubjectUser
		if dest != nil && destWindow > 0 && !dest.Before(e.OccurredAt.Add(-destWindow)) {
			e.NewDestination = true
		}
		inputs = append(inputs, e)
	}
	return inputs, rows.Err()
}

// recordFraudAlerts dedupes and stores the fraud alerts raised for one user,
// returning the count of new alerts.
func (s *Store) recordFraudAlerts(ctx context.Context, userID uuid.UUID, alerts []fraud.FraudAlert) (int, error) {
	raised := 0
	for _, alert := range alerts {
		recorded, err := s.fraudAlertExists(ctx, userID, alert.Rule, alert.References)
		if err != nil {
			return raised, err
		}
		if recorded {
			continue
		}
		if err := s.insertFraudAlert(ctx, userID, alert); err != nil {
			return raised, err
		}
		raised++
	}
	return raised, nil
}

// fraudAlertExists reports whether a fraud alert with the rule and any of the
// rail references was already recorded, keeping the sweep idempotent.
func (s *Store) fraudAlertExists(ctx context.Context, userID uuid.UUID, rule string, references []string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM transaction_alerts
			WHERE lane='fraud' AND user_id=$1 AND rule=$2
			  AND cardinality($3::text[]) > 0
			  AND details->>'reference' = ANY($3::text[])
		)`, userID, rule, references).Scan(&exists)
	return exists, err
}

// insertFraudAlert stores one fraud-lane alert and opens a review case for a
// medium or high detection. Unlike the AML path it records no risk event, so
// the two lanes stay separable.
func (s *Store) insertFraudAlert(ctx context.Context, userID uuid.UUID, alert fraud.FraudAlert) error {
	details := map[string]any{}
	for key, value := range alert.Details {
		details[key] = value
	}
	if len(alert.Rails) > 0 {
		details["rails"] = alert.Rails
	}
	if len(alert.References) > 0 {
		details["reference"] = alert.References[0]
		if len(alert.References) > 1 {
			details["references"] = alert.References
		}
	}
	details["score"] = fraud.AlertScore(alert.Severity)
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode fraud alert details: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO transaction_alerts(user_id,lane,rule,severity,details)
		VALUES($1,'fraud',$2,$3,$4::jsonb)`,
		userID, alert.Rule, alert.Severity, string(encoded)); err != nil {
		return fmt.Errorf("insert fraud alert: %w", err)
	}
	if alert.Severity != fraud.SeverityLow {
		if _, err := s.CreateManualReviewCase(ctx, ManualReviewCase{
			UserID:   userID,
			CaseType: "fraud",
			Reason:   "fraud alert " + alert.Rule + " (" + alert.Severity + ")",
		}); err != nil {
			return fmt.Errorf("review case for fraud alert: %w", err)
		}
	}
	return nil
}
