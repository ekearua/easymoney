-- Multiple second factors per subject. TOTP arrived first and lives in
-- totp_secrets/totp_pending_logins; this pair generalises the model so a
-- subject can hold an authenticator app, an emailed one-time code, and any
-- number of passkeys, and so a pending step survives a mistyped code instead of
-- being consumed by the first wrong guess.
--
-- scope is the portal ('admin' or 'merchant'); subject_id is the admin_users or
-- merchants row. Both are NOT NULL: every enrolled factor belongs to a named
-- account. Pending steps from the legacy table are deliberately not carried
-- over — they live ten minutes, so an interrupted sign-in simply restarts.
CREATE TABLE IF NOT EXISTS mfa_factors (
    id bigserial PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('admin', 'merchant')),
    subject_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('totp', 'email', 'webauthn')),
    label text NOT NULL DEFAULT '',
    secret_cipher bytea,
    credential_id bytea,
    public_key bytea,
    sign_count bigint NOT NULL DEFAULT 0,
    transports text NOT NULL DEFAULT '',
    confirmed_at timestamptz,
    last_used_at timestamptz,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One authenticator app and one email destination per subject; passkeys are
-- unbounded because every device registers its own credential.
CREATE UNIQUE INDEX IF NOT EXISTS mfa_factors_totp_one
    ON mfa_factors(scope, subject_id) WHERE kind='totp';
CREATE UNIQUE INDEX IF NOT EXISTS mfa_factors_email_one
    ON mfa_factors(scope, subject_id) WHERE kind='email';
CREATE UNIQUE INDEX IF NOT EXISTS mfa_factors_credential_one
    ON mfa_factors(credential_id) WHERE credential_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS mfa_factors_subject_idx
    ON mfa_factors(scope, subject_id) WHERE disabled_at IS NULL;

-- Pending second-factor steps. attempts bounds guessing inside one step so a
-- typo stays retryable; code_hash holds a bcrypt hash of an emailed code and
-- webauthn_state the serialised ceremony for a passkey assertion.
CREATE TABLE IF NOT EXISTS mfa_challenges (
    token_hash bytea PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('admin', 'merchant')),
    subject_id uuid NOT NULL,
    method text NOT NULL CHECK (method IN ('totp', 'email', 'webauthn')),
    factor_id bigint REFERENCES mfa_factors(id) ON DELETE CASCADE,
    secret_cipher bytea,
    code_hash bytea,
    webauthn_state bytea,
    attempts integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS mfa_challenges_expiry_idx
    ON mfa_challenges(expires_at) WHERE consumed_at IS NULL;
CREATE INDEX IF NOT EXISTS mfa_challenges_subject_idx
    ON mfa_challenges(scope, subject_id);

-- Backfill existing authenticator enrollments so nobody is forced to re-enroll
-- when this ships. Legacy rows written before admin accounts were identified
-- carry a NULL subject_id and cannot be attributed to an operator, so they stay
-- behind and that account enrolls again at its next sign-in.
INSERT INTO mfa_factors(scope, subject_id, kind, label, secret_cipher, confirmed_at, created_at, updated_at)
SELECT scope, subject_id, 'totp', 'Authenticator app', secret_cipher, created_at, created_at, updated_at
FROM totp_secrets
WHERE subject_id IS NOT NULL
ON CONFLICT DO NOTHING;
