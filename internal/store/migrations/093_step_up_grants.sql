-- Spec G13 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.7): step-up re-authentication. A
-- high-risk merchant owner's payout request must present a fresh second factor
-- before the settlement service will dispatch funds. A verified factor records
-- a short-lived, action-scoped grant here; the merchant portal checks it at
-- payout time. Grants are time-bound (STEP_UP_TTL) and never gate worker
-- dispatches, so a queued payout can still settle.
CREATE TABLE IF NOT EXISTS step_up_grants (
    id          bigserial PRIMARY KEY,
    scope       text NOT NULL CHECK (scope IN ('merchant','admin')),
    subject_id  uuid NOT NULL,
    action      text NOT NULL,
    factor_kind text NOT NULL DEFAULT '',
    granted_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS step_up_grants_lookup_idx
    ON step_up_grants(scope, subject_id, action, expires_at DESC);
