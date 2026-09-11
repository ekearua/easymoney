-- Dynamic virtual account (DVA) instructions for Interswitch bank-transfer
-- collection. Each DVA is a one-time Wema virtual account generated against the
-- payment's provider reference and valid for a short window.
CREATE TABLE IF NOT EXISTS virtual_account_instructions (
    payment_id        uuid PRIMARY KEY REFERENCES payments(id) ON DELETE CASCADE,
    account_number    text NOT NULL,
    account_name      text NOT NULL DEFAULT '',
    bank_name         text NOT NULL DEFAULT '',
    provider_reference text NOT NULL,
    validity_mins     integer NOT NULL DEFAULT 30,
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_virtual_account_instructions_reference
    ON virtual_account_instructions(provider_reference);