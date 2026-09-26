-- M4 Trust Fabric feedback loop: alert outcomes become first-class data.
-- Every terminal disposition of a transaction alert records one outcome row
-- (the doctrine's "confirmed fraud, false positives and escalations feed back
-- into weights"), so false-positive rates per rule become computable and any
-- future threshold/model work has labelled data from day one.
CREATE TABLE IF NOT EXISTS transaction_alert_outcomes (
    id bigserial PRIMARY KEY,
    alert_id bigint NOT NULL REFERENCES transaction_alerts(id) ON DELETE CASCADE,
    outcome text NOT NULL CHECK (outcome IN ('false_positive','confirmed','undetermined')),
    rule text NOT NULL,
    severity text NOT NULL,
    reviewer_email text NOT NULL DEFAULT '',
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS transaction_alert_outcomes_rule_idx
    ON transaction_alert_outcomes(rule, outcome);

-- outcome_from_status maps the resolve action to its label: acknowledging a
-- means "not yet determined", escalating means the reviewer confirmed the
-- suspicion, resolving is the reviewer's explicit call (form field
-- `outcome`, defaulting to false_positive — the common disposition when an
-- alert is closed without escalation).
CREATE OR REPLACE FUNCTION outcome_from_status(p_status text, p_outcome text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN p_status = 'acknowledged' THEN 'undetermined'
        WHEN p_status = 'escalated'    THEN 'confirmed'
        ELSE COALESCE(NULLIF(p_outcome, ''), 'false_positive')
    END;
$$;
