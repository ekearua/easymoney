-- Spec G7 residual (CBN_AML_COMPLIANCE_SPEC.md §5): scheduled audit
-- chain-verification. VerifyAuditChain recomputes the audit_logs hash chain;
-- the admin dashboard already renders chain status on page load. The residual
-- is a persistent record of the scheduled sweep so a break is detectable (and
-- alertable) even when no admin is looking:
--
--   - audit_chain_checks: one row per sweep (rows verified, break position,
--     -1 when intact) with the alert state at that sweep.
--   - audit_chain_alerts: the human decision — one row while a break is
--     unresolved, with reviewer, outcome note and resolution. The sweep
--     opens it on the first failing sweep and marks it recovered (never
--     deletes the evidence) when a later sweep verifies clean.

CREATE TABLE IF NOT EXISTS audit_chain_checks (
    id           BIGSERIAL PRIMARY KEY,
    checked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rows_checked INT NOT NULL,
    broken_at    INT NOT NULL DEFAULT -1,
    alert_open   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS audit_chain_checks_checked_at_idx
    ON audit_chain_checks (checked_at DESC);

CREATE TABLE IF NOT EXISTS audit_chain_alerts (
    id             BIGSERIAL PRIMARY KEY,
    broken_at_row  INT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'open'
                   CHECK (status IN ('open','recovered','acknowledged_bug','confirmed_tampering')),
    opened_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    recovered_at   TIMESTAMPTZ,
    reviewer_email TEXT NOT NULL DEFAULT '',
    outcome_note   TEXT NOT NULL DEFAULT ''
);

-- One live alert at a time; the sweep's alert_open flag keys off this.
CREATE UNIQUE INDEX IF NOT EXISTS audit_chain_alerts_open_one
    ON audit_chain_alerts ((status = 'open')) WHERE status = 'open';
