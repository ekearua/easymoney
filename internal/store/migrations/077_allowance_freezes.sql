-- Trust Fabric network layer, allowance freeze: resolving a network case as
-- confirmed lets the reviewer freeze money movement for every member of the
-- ring in one action. The freeze is enforced inside ReserveAllowance (the
-- same validated transaction as the tier ceilings), so a frozen subject
-- cannot reserve a new allowance in either direction while the flag is set.
-- Unfreeze restores movement immediately and records who lifted it.
CREATE TABLE IF NOT EXISTS allowance_freezes (
    id bigserial PRIMARY KEY,
    account_type text NOT NULL CHECK (account_type IN ('individual','business')),
    subject_id uuid NOT NULL,
    frozen_by text NOT NULL DEFAULT '',
    case_id bigint REFERENCES network_cases(id) ON DELETE SET NULL,
    reason text NOT NULL DEFAULT '',
    frozen_at timestamptz NOT NULL DEFAULT now(),
    unfrozen_at timestamptz,
    unfrozen_by text NOT NULL DEFAULT '',
    UNIQUE (account_type, subject_id, unfrozen_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS allowance_freezes_active_idx
    ON allowance_freezes(account_type, subject_id)
    WHERE unfrozen_at IS NULL;

CREATE INDEX IF NOT EXISTS allowance_freezes_subject_idx
    ON allowance_freezes(subject_id, frozen_at DESC);
