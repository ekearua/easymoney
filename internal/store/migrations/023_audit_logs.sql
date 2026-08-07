-- Tamper-evident audit log for privileged actions (C4/C26).
-- Entries are hash-chained: each row stores the SHA-256 of the previous row's
-- hash, and every mutation (insert/update/delete) is rejected by trigger.
CREATE TABLE IF NOT EXISTS audit_logs (
    id bigserial PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_type text NOT NULL CHECK (actor_type IN ('admin', 'merchant', 'system')),
    actor_id uuid,
    actor_email text,
    ip text,
    action text NOT NULL,
    resource_type text,
    resource_id text,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    prev_hash text NOT NULL DEFAULT '',
    hash text NOT NULL UNIQUE
);

CREATE INDEX IF NOT EXISTS audit_logs_actor_idx ON audit_logs(actor_email, occurred_at);
CREATE INDEX IF NOT EXISTS audit_logs_action_idx ON audit_logs(action, occurred_at);
CREATE INDEX IF NOT EXISTS audit_logs_occurred_idx ON audit_logs(occurred_at DESC);

-- Append-only enforcement: rows may never be updated or deleted through SQL.
CREATE OR REPLACE FUNCTION reject_audit_logs_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_logs_no_mutation ON audit_logs;
CREATE TRIGGER audit_logs_no_mutation
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION reject_audit_logs_mutation();
