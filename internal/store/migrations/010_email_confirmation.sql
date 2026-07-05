-- Demo-stage email confirmation codes. The raw six-digit code is never stored;
-- only a SHA-256 hash is retained until the code expires or is consumed.
CREATE TABLE IF NOT EXISTS email_verification_codes (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email text NOT NULL,
    code_hash bytea NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS email_verification_codes_user_idx
    ON email_verification_codes(user_id, email, expires_at DESC);

CREATE INDEX IF NOT EXISTS email_verification_codes_expiry_idx
    ON email_verification_codes(expires_at)
    WHERE consumed_at IS NULL;
