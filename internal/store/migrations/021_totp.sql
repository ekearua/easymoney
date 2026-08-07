-- TOTP two-factor authentication. The base32 shared secret is never stored in
-- plaintext; only an AES-256-GCM ciphertext under a service-managed key.
CREATE TABLE IF NOT EXISTS totp_secrets (
    id bigserial PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('admin', 'merchant')),
    subject_id uuid,
    secret_cipher bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scope, subject_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS totp_secrets_admin_one
    ON totp_secrets(scope) WHERE scope='admin' AND subject_id IS NULL;

CREATE INDEX IF NOT EXISTS totp_secrets_scope_idx ON totp_secrets(scope);

-- Pending two-factor login steps. A short-lived random token is issued after
-- a successful password check and consumed once the code is verified. The
-- pending secret supports enrollment without persisting plaintext anywhere.
CREATE TABLE IF NOT EXISTS totp_pending_logins (
    token_hash bytea PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('admin', 'merchant')),
    subject_id uuid,
    secret_cipher bytea,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS totp_pending_expiry_idx ON totp_pending_logins(expires_at);
