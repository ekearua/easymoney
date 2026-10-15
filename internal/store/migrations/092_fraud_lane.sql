-- Spec G13 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.7): the fraud lane. Payment and
-- payout fraud signals (failed-attempt bursts, chargeback/refund velocity,
-- destination churn) are a distinct rule family from the C14 AML monitor, so
-- they share transaction_alerts but carry lane='fraud' and are listed and
-- triaged on their own queue. Existing rows and every writer that predates
-- this migration default to lane='aml', so the AML queue is unchanged.
--
-- Fraud alerts never write a risk_events row: the ML/FT score stays AML-only so
-- the two lanes remain independently measurable and tunable.

ALTER TABLE transaction_alerts
    ADD COLUMN IF NOT EXISTS lane text NOT NULL DEFAULT 'aml';

ALTER TABLE transaction_alerts
    DROP CONSTRAINT IF EXISTS transaction_alerts_lane_check;

ALTER TABLE transaction_alerts
    ADD CONSTRAINT transaction_alerts_lane_check
        CHECK (lane IN ('aml','fraud'));

-- The fraud queue pages by lane + status + recency, exactly like the AML one.
CREATE INDEX IF NOT EXISTS transaction_alerts_lane_idx
    ON transaction_alerts(lane, status, created_at DESC);

-- Fraud escalations open a review case on the same queue; 'fraud' is the type.
ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_case_type_check;

ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_case_type_check
        CHECK (case_type IN ('tier_upgrade','screening','edd','document','monitoring','pep_edd','identity','fraud'));

CREATE INDEX IF NOT EXISTS manual_review_cases_fraud_open_idx
    ON manual_review_cases(user_id)
    WHERE case_type = 'fraud' AND status = 'pending';
