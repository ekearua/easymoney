-- C16: double-entry append-only journal. Every money movement is posted as a
-- balanced pair (one debit + one credit) and chained with SHA-256 hashes, so
-- the book is tamper-evident and provably sums to zero. Corrections are never
-- applied by mutating a row: they are posted as new offsetting entries linked
-- through reversal_of, satisfying CBN record-keeping expectations.
CREATE TABLE IF NOT EXISTS ledger_entries (
    id bigserial PRIMARY KEY,
    journal_ref text NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    entry_type text NOT NULL CHECK (entry_type IN ('debit','credit')),
    account text NOT NULL,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency text NOT NULL DEFAULT 'NGN',
    description text NOT NULL DEFAULT '',
    posted_by text NOT NULL DEFAULT 'system',
    reversal_of bigint REFERENCES ledger_entries(id),
    prev_hash text NOT NULL DEFAULT '',
    hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ledger_journal_ref_idx ON ledger_entries(journal_ref);
CREATE INDEX IF NOT EXISTS ledger_source_idx ON ledger_entries(source_type, source_id);
CREATE INDEX IF NOT EXISTS ledger_account_idx ON ledger_entries(account, created_at DESC);

-- The journal is append-only, mirroring the audit log and archive ledger.
CREATE OR REPLACE FUNCTION reject_ledger_entry_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS ledger_entries_no_mutation ON ledger_entries;
CREATE TRIGGER ledger_entries_no_mutation
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_entry_mutation();
