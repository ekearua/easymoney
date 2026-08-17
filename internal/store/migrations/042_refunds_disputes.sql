-- S2: refunds and disputes.
CREATE TABLE IF NOT EXISTS refunds (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id       uuid NOT NULL REFERENCES payments(id),
    merchant_id      uuid NOT NULL,
    amount_kobo      bigint NOT NULL CHECK (amount_kobo > 0),
    reason           text NOT NULL DEFAULT '',
    status           text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','succeeded','failed')),
    provider_refund_id text NOT NULL DEFAULT '',
    last_error       text NOT NULL DEFAULT '',
    admin_id         uuid REFERENCES admin_users(id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz
);
CREATE INDEX IF NOT EXISTS idx_refunds_payment ON refunds(payment_id);
CREATE INDEX IF NOT EXISTS idx_refunds_merchant ON refunds(merchant_id);

CREATE TABLE IF NOT EXISTS disputes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id  uuid NOT NULL REFERENCES payments(id),
    merchant_id uuid NOT NULL,
    reason      text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','won','lost','expired')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_disputes_payment ON disputes(payment_id);
CREATE INDEX IF NOT EXISTS idx_disputes_merchant ON disputes(merchant_id);
