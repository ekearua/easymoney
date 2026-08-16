// C17: three-way reconciliation. A run compares the three money-movement legs
// that exist in the demo — internal payments state, the C16 double-entry
// ledger, and the simulated bank rail — and persists every discrepancy found.
// Runs are stored so the CBN paper trail shows automated daily runs plus
// manual weekly runs.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ReconciliationRun is one snapshot of a three-way reconciliation.
type ReconciliationRun struct {
	ID              int64
	RunType         string // "auto" or "manual"
	CreatedBy       string
	InternalCount   int
	LedgerCount     int
	BankCount       int
	DiscrepancyCount int
	Status          string // "clean" or "discrepancies"
	CreatedAt       time.Time
}

// ReconciliationItem is one discrepancy found by a run.
type ReconciliationItem struct {
	ID          int64
	RunID       int64
	Category    string
	Reference   string
	ExpectedKobo int64
	ActualKobo  int64
	Detail      string
}

// RunReconciliation compares internal payments vs ledger money-in postings vs
// the simulated bank rail and stores the outcome. It returns the run and the
// discrepancies found (empty slice means clean).
func (s *Store) RunReconciliation(ctx context.Context, runType, createdBy string) (ReconciliationRun, []ReconciliationItem, error) {
	if runType != "auto" && runType != "manual" {
		runType = "auto"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	defer tx.Rollback(ctx)

	// Leg 1: succeeded payments (internal state machine).
	type paymentLeg struct {
		ID       uuid.UUID
		Ref      string
		Amount   int64
		Currency string
		Provider string
	}
	internal := map[string]paymentLeg{}
	rows, err := tx.Query(ctx, `
		SELECT id, provider_reference, amount_kobo, currency, provider
		FROM payments WHERE status='succeeded'`)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	for rows.Next() {
		var p paymentLeg
		if err := rows.Scan(&p.ID, &p.Ref, &p.Amount, &p.Currency, &p.Provider); err != nil {
			rows.Close()
			return ReconciliationRun{}, nil, err
		}
		internal[p.ID.String()] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReconciliationRun{}, nil, err
	}

	// Leg 2: ledger money-in postings (debit to operating bank) per journal ref.
	ledger := map[string]int64{}
	rows, err = tx.Query(ctx, `
		SELECT journal_ref, SUM(amount_kobo)
		FROM ledger_entries
		WHERE entry_type='debit' AND account=$1 AND source_type='payment' AND reversal_of IS NULL
		GROUP BY journal_ref`, LedgerAccountOperatingBank)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	for rows.Next() {
		var ref string
		var amount int64
		if err := rows.Scan(&ref, &amount); err != nil {
			rows.Close()
			return ReconciliationRun{}, nil, err
		}
		ledger[ref] = amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReconciliationRun{}, nil, err
	}

	// Leg 3: confirmed bank rail (simulated transfers).
	type bankLeg struct {
		PaymentID uuid.UUID
		Amount    int64
		Currency  string
	}
	bank := map[string]bankLeg{}
	rows, err = tx.Query(ctx, `
		SELECT bts.payment_id, p.amount_kobo, p.currency
		FROM bank_transfer_simulations bts
		JOIN payments p ON p.id=bts.payment_id
		WHERE bts.status='user_confirmed'`)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	for rows.Next() {
		var b bankLeg
		if err := rows.Scan(&b.PaymentID, &b.Amount, &b.Currency); err != nil {
			rows.Close()
			return ReconciliationRun{}, nil, err
		}
		bank[b.PaymentID.String()] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReconciliationRun{}, nil, err
	}

	run := ReconciliationRun{
		RunType:   runType,
		CreatedBy: createdBy,
	}
	if createdBy == "" {
		run.CreatedBy = "system"
	}
	var items []ReconciliationItem
	add := func(category, reference, detail string, expected, actual int64) {
		items = append(items, ReconciliationItem{
			Category: category, Reference: reference,
			ExpectedKobo: expected, ActualKobo: actual, Detail: detail,
		})
	}

	// Internal vs ledger: every succeeded payment must have a money-in posting
	// of the same amount; every money-in posting must belong to a succeeded
	// payment.
	for ref, p := range internal {
		run.InternalCount++
		posted, ok := ledger[ref]
		if !ok {
			add("internal_without_ledger", ref, "succeeded payment has no ledger money-in posting", p.Amount, 0)
			continue
		}
		if posted != p.Amount {
			add("amount_mismatch", ref,
				fmt.Sprintf("payment %d vs ledger %d", p.Amount, posted), p.Amount, posted)
		}
	}
	for ref, posted := range ledger {
		run.LedgerCount++
		p, ok := internal[ref]
		if !ok {
			add("ledger_without_internal", ref,
				fmt.Sprintf("ledger posting %d has no succeeded payment", posted), posted, 0)
			continue
		}
		_ = p
	}

	// Bank rail vs internal: confirmed transfers must be succeeded payments.
	for ref, b := range bank {
		run.BankCount++
		p, ok := internal[ref]
		if !ok {
			add("bank_without_internal", ref,
				"bank transfer confirmed but payment is not succeeded", b.Amount, 0)
			continue
		}
		if p.Amount != b.Amount {
			add("bank_amount_mismatch", ref,
				fmt.Sprintf("payment %d vs bank %d", p.Amount, b.Amount), p.Amount, b.Amount)
		}
	}
	for ref, p := range internal {
		if p.Provider != "bank_transfer" {
			continue
		}
		if _, ok := bank[ref]; !ok {
			add("internal_without_bank", ref,
				"succeeded bank transfer has no confirmed bank rail record", p.Amount, 0)
		}
	}

	// Leg 4 (S1): completed payouts must have a money-out posting on the
	// settlement payable account; every such posting must belong to a completed
	// payout. Money-out is the debit to 3200 with source_type 'payout'.
	type payoutLeg struct {
		BatchNo string
		Amount  int64
	}
	completedPayouts := map[string]payoutLeg{}
	rows, err = tx.Query(ctx, `
		SELECT p.id, b.batch_no, p.amount_kobo
		FROM payouts p JOIN settlement_batches b ON b.id=p.batch_id
		WHERE p.status='completed'`)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var p payoutLeg
		if err := rows.Scan(&id, &p.BatchNo, &p.Amount); err != nil {
			rows.Close()
			return ReconciliationRun{}, nil, err
		}
		completedPayouts[id.String()] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReconciliationRun{}, nil, err
	}

	payoutLedger := map[string]int64{}
	rows, err = tx.Query(ctx, `
		SELECT source_id, SUM(amount_kobo)
		FROM ledger_entries
		WHERE entry_type='debit' AND account=$1 AND source_type='payout' AND reversal_of IS NULL
		GROUP BY source_id`, LedgerAccountSettlementPayable)
	if err != nil {
		return ReconciliationRun{}, nil, err
	}
	for rows.Next() {
		var ref string
		var amount int64
		if err := rows.Scan(&ref, &amount); err != nil {
			rows.Close()
			return ReconciliationRun{}, nil, err
		}
		payoutLedger[ref] = amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReconciliationRun{}, nil, err
	}

	for ref, p := range completedPayouts {
		posted, ok := payoutLedger[ref]
		if !ok {
			add("payout_without_ledger", p.BatchNo,
				"completed payout has no money-out posting", p.Amount, 0)
			continue
		}
		if posted != p.Amount {
			add("payout_amount_mismatch", p.BatchNo,
				fmt.Sprintf("payout %d vs ledger %d", p.Amount, posted), p.Amount, posted)
		}
	}
	for ref, posted := range payoutLedger {
		if _, ok := completedPayouts[ref]; !ok {
			add("ledger_without_payout", ref,
				fmt.Sprintf("money-out posting %d has no completed payout", posted), posted, 0)
		}
	}

	run.DiscrepancyCount = len(items)
	run.Status = "clean"
	if len(items) > 0 {
		run.Status = "discrepancies"
	}

	var runID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO reconciliations(run_type,created_by,internal_expected,ledger_expected,
			bank_expected,discrepancy_count,status)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at`,
		run.RunType, run.CreatedBy, run.InternalCount, run.LedgerCount,
		run.BankCount, run.DiscrepancyCount, run.Status).Scan(&runID, &run.CreatedAt); err != nil {
		return ReconciliationRun{}, nil, err
	}
	run.ID = runID

	for i := range items {
		items[i].RunID = runID
		if _, err := tx.Exec(ctx, `
			INSERT INTO reconciliation_items(run_id,category,reference,expected_kobo,actual_kobo,detail)
			VALUES($1,$2,$3,$4,$5,$6)`,
			runID, items[i].Category, items[i].Reference, items[i].ExpectedKobo, items[i].ActualKobo, items[i].Detail); err != nil {
			return ReconciliationRun{}, nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ReconciliationRun{}, nil, err
	}
	return run, items, nil
}

// ListReconciliationRuns returns recent runs, newest first.
func (s *Store) ListReconciliationRuns(ctx context.Context, limit int) ([]ReconciliationRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_type, created_by, internal_expected, ledger_expected,
		       bank_expected, discrepancy_count, status, created_at
		FROM reconciliations ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []ReconciliationRun
	for rows.Next() {
		var r ReconciliationRun
		if err := rows.Scan(&r.ID, &r.RunType, &r.CreatedBy, &r.InternalCount,
			&r.LedgerCount, &r.BankCount, &r.DiscrepancyCount, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ReconciliationItemsByRun returns the discrepancies recorded for one run.
func (s *Store) ReconciliationItemsByRun(ctx context.Context, runID int64) ([]ReconciliationItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, category, reference, expected_kobo, actual_kobo, detail
		FROM reconciliation_items WHERE run_id=$1 ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ReconciliationItem
	for rows.Next() {
		var it ReconciliationItem
		if err := rows.Scan(&it.ID, &it.RunID, &it.Category, &it.Reference,
			&it.ExpectedKobo, &it.ActualKobo, &it.Detail); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
