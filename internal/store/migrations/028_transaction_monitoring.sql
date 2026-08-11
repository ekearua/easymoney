-- C14: transaction monitoring. transaction_alerts stores each suspicious
-- activity detection raised by internal/kyc.RunTransactionMonitor. Alerts
-- carry a rule + severity, link to the customer and (optionally) the offending
-- payment, and flow through the compliance queue just like manual review
-- cases. The manual_review_cases case_type CHECK is extended with 'monitoring'
-- so alert escalations can open a review case.
CREATE TABLE IF NOT EXISTS transaction_alerts (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    payment_id uuid REFERENCES payments(id) ON DELETE SET NULL,
    rule text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('low','medium','high')),
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged','escalated','resolved')),
    reviewer_email text,
    reviewed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS transaction_alerts_status_idx
    ON transaction_alerts(status, created_at DESC);
CREATE INDEX IF NOT EXISTS transaction_alerts_user_idx
    ON transaction_alerts(user_id, created_at DESC);

ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_case_type_check;
ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_case_type_check
    CHECK (case_type IN ('tier_upgrade','screening','edd','document','monitoring'));
