-- Merchant payment terms, web dashboard auth, and NFC phone whitelist.
ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS password_hash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS allow_partial_payments boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS min_invoice_amount_kobo bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upfront_percent integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS min_installment_percent integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_installments integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS allow_full_pay_always boolean NOT NULL DEFAULT true;

CREATE TABLE IF NOT EXISTS merchant_sessions (
    token_hash bytea PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS merchant_sessions_user_idx ON merchant_sessions(user_id);

ALTER TABLE registered_services
    ADD COLUMN IF NOT EXISTS phone_whitelist text NOT NULL DEFAULT '';
