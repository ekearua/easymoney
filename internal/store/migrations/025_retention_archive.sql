-- C8/C29: legal holds + archive ledger + batched-purge indexes.
--
-- Legal holds let an operator freeze subjects (users, payments, invoices,
-- thrift groups) so the retention worker never touches them, even after the
-- normal cutoff. This covers subpoenas, disputes and internal investigations.
CREATE TABLE IF NOT EXISTS legal_holds (
    id bigserial PRIMARY KEY,
    subject_type text NOT NULL CHECK (subject_type IN ('user','payment','invoice','thrift_group')),
    subject_id text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text NOT NULL DEFAULT '',
    expires_at timestamptz,
    UNIQUE (subject_type, subject_id)
);

CREATE INDEX IF NOT EXISTS legal_holds_expiry_idx
    ON legal_holds(expires_at) WHERE expires_at IS NOT NULL;

-- Append-only archive of records removed by retention. Financial records are
-- snapshotted here (hash-chained like audit_logs) before the operational rows
-- are hard-deleted, satisfying MLPA record-keeping for archived payments,
-- invoices and thrift activity.
CREATE TABLE IF NOT EXISTS archive_ledger (
    id bigserial PRIMARY KEY,
    archived_at timestamptz NOT NULL DEFAULT now(),
    record_type text NOT NULL,
    subject_id text NOT NULL,
    payload jsonb NOT NULL,
    prev_hash text NOT NULL DEFAULT '',
    hash text NOT NULL UNIQUE
);

CREATE INDEX IF NOT EXISTS archive_ledger_type_idx ON archive_ledger(record_type, archived_at DESC);
CREATE INDEX IF NOT EXISTS archive_ledger_subject_idx ON archive_ledger(subject_id);

-- Archive ledger is append-only, mirroring the audit log guarantee.
CREATE OR REPLACE FUNCTION reject_archive_ledger_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'archive_ledger is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS archive_ledger_no_mutation ON archive_ledger;
CREATE TRIGGER archive_ledger_no_mutation
    BEFORE UPDATE OR DELETE ON archive_ledger
    FOR EACH ROW EXECUTE FUNCTION reject_archive_ledger_mutation();

-- Indexes so batched retention purges scan by timestamp instead of full scans.
CREATE INDEX IF NOT EXISTS payments_created_idx ON payments(created_at);
CREATE INDEX IF NOT EXISTS invoices_created_idx ON invoices(created_at);
CREATE INDEX IF NOT EXISTS thrift_groups_updated_idx ON thrift_groups(updated_at);
CREATE INDEX IF NOT EXISTS webhook_deliveries_received_idx ON webhook_deliveries(received_at);
CREATE INDEX IF NOT EXISTS merchant_registrations_created_idx ON merchant_registrations(created_at);
