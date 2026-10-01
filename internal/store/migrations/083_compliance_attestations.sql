-- Spec G8 (CBN_AML_COMPLIANCE_SPEC.md §5): the annual independent-validation
-- attestation. The CBN Baseline Standards expect an OFI's automated AML
-- solution to be independently validated on a documented cadence; the
-- evidence — who validated, the scope, what they found, and the remediation
-- — must be retrievable for the regulator. One row per validation cycle,
-- tracked from scheduled to submitted, so the admin console can show the
-- clock as well as the evidence.

CREATE TABLE IF NOT EXISTS compliance_attestations (
    id            BIGSERIAL PRIMARY KEY,
    cycle_year    INT NOT NULL UNIQUE,
    scope         TEXT NOT NULL,
    summary       TEXT NOT NULL DEFAULT '',
    findings      TEXT NOT NULL DEFAULT '',
    remediation   TEXT NOT NULL DEFAULT '',
    evidence_url  TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'scheduled'
                  CHECK (status IN ('scheduled','in_progress','submitted')),
    due_date      DATE NOT NULL,
    completed_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
