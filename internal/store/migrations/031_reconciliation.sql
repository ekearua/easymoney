-- C17: three-way reconciliation. A run snapshots the three legs that exist in
-- the demo — internal payments state, the C16 double-entry ledger, and the
-- simulated bank rail (bank_transfer_simulations) — and records every
-- discrepancy found. Runs are stored so the CBN paper trail shows automated
-- daily runs plus manual weekly runs.
CREATE TABLE IF NOT EXISTS reconciliations (
    id bigserial PRIMARY KEY,
    run_type text NOT NULL CHECK (run_type IN ('auto','manual')),
    created_by text NOT NULL DEFAULT 'system',
    internal_expected int NOT NULL DEFAULT 0,
    ledger_expected int NOT NULL DEFAULT 0,
    bank_expected int NOT NULL DEFAULT 0,
    discrepancy_count int NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'clean' CHECK (status IN ('clean','discrepancies')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS reconciliations_created_idx ON reconciliations(created_at DESC);

CREATE TABLE IF NOT EXISTS reconciliation_items (
    id bigserial PRIMARY KEY,
    run_id bigint NOT NULL REFERENCES reconciliations(id) ON DELETE CASCADE,
    category text NOT NULL,
    reference text NOT NULL,
    expected_kobo bigint NOT NULL DEFAULT 0,
    actual_kobo bigint NOT NULL DEFAULT 0,
    detail text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS reconciliation_items_run_idx ON reconciliation_items(run_id);
