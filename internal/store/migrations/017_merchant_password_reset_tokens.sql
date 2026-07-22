-- One-time tokens for merchants to set their login password after approval.
CREATE TABLE IF NOT EXISTS merchant_password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS merchant_password_reset_tokens_merchant_idx ON merchant_password_reset_tokens(merchant_id);
