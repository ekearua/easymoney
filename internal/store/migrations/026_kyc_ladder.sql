-- C9/C10: KYC/CDD data model foundation + L0-L4 customer tier ladder.
-- Tables are the schema foundation referenced by C9; the tier ladder rules
-- live in internal/kyc and the store layer enforces transitions with audit.
--
-- kyc_profiles        one row per user: current tier, evidence, review status
-- customer_verifications  every identity/channel verification performed
-- screening_results   sanctions/PEP screening outcomes per user
-- risk_events         ML/FT risk observations driving CDD vs EDD
-- authenticators      future authenticator (passkey/hardware) records
-- trusted_devices     recognised devices for a user
-- manual_review_cases KYC review queue for approvals / rejections

CREATE TABLE IF NOT EXISTS kyc_profiles (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tier text NOT NULL DEFAULT 'L0' CHECK (tier IN ('L0','L1','L2','L3','L4')),
    tier_updated_at timestamptz NOT NULL DEFAULT now(),
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_screening_decision text,
    review_status text NOT NULL DEFAULT 'none' CHECK (review_status IN ('none','pending','approved','rejected')),
    reviewer_email text,
    reviewed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS kyc_profiles_tier_idx ON kyc_profiles(tier, updated_at);
CREATE INDEX IF NOT EXISTS kyc_profiles_review_idx ON kyc_profiles(review_status, updated_at);

CREATE TABLE IF NOT EXISTS customer_verifications (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    verification_type text NOT NULL CHECK (verification_type IN (
        'channel_confirmed','email_confirmed','identity_on_file',
        'nin_or_bvn_verified','edd_completed')),
    status text NOT NULL DEFAULT 'completed' CHECK (status IN ('completed','pending','failed','expired')),
    provider text,
    provider_reference text,
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    verified_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz
);

CREATE INDEX IF NOT EXISTS customer_verifications_user_idx
    ON customer_verifications(user_id, verified_at DESC);

CREATE TABLE IF NOT EXISTS screening_results (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider text NOT NULL,
    decision text NOT NULL CHECK (decision IN (
        'clear','possible','strong','blocked','manually_cleared')),
    matched_names jsonb NOT NULL DEFAULT '[]'::jsonb,
    screened_at timestamptz NOT NULL DEFAULT now(),
    rescreen_due timestamptz
);

CREATE INDEX IF NOT EXISTS screening_results_user_idx
    ON screening_results(user_id, screened_at DESC);

CREATE TABLE IF NOT EXISTS risk_events (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    score numeric(4,1),
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS risk_events_user_idx ON risk_events(user_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS authenticators (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('totp','passkey','hardware')),
    external_id text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','revoked')),
    registered_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    UNIQUE (user_id, kind, external_id)
);

CREATE TABLE IF NOT EXISTS trusted_devices (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_fingerprint text NOT NULL,
    label text NOT NULL DEFAULT '',
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, device_fingerprint)
);

CREATE TABLE IF NOT EXISTS manual_review_cases (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    case_type text NOT NULL CHECK (case_type IN (
        'tier_upgrade','screening','edd','document')),
    requested_tier text CHECK (requested_tier IN ('L0','L1','L2','L3','L4')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    reason text NOT NULL DEFAULT '',
    reviewer_email text,
    decision_note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz
);

CREATE INDEX IF NOT EXISTS manual_review_cases_status_idx
    ON manual_review_cases(status, created_at DESC);
CREATE INDEX IF NOT EXISTS manual_review_cases_user_idx
    ON manual_review_cases(user_id);

-- Backfill: users who already have an approved individual profile reach tier
-- L2 (identity on file) with the demo's simulated screening marked clear.
INSERT INTO kyc_profiles (user_id, tier, evidence, last_screening_decision, review_status)
SELECT ip.user_id, 'L2',
       '["channel_confirmed","identity_on_file"]'::jsonb,
       'clear', 'approved'
FROM individual_profiles ip
WHERE ip.kyc_status = 'approved_simulated'
ON CONFLICT (user_id) DO UPDATE
SET tier = 'L2',
    evidence = '["channel_confirmed","identity_on_file"]'::jsonb,
    last_screening_decision = 'clear',
    tier_updated_at = now(),
    updated_at = now();

INSERT INTO customer_verifications (user_id, verification_type, provider, result)
SELECT user_id, 'identity_on_file', 'simulated', jsonb_build_object('status','approved_simulated')
FROM individual_profiles
WHERE kyc_status = 'approved_simulated'
ON CONFLICT DO NOTHING;

INSERT INTO screening_results (user_id, provider, decision)
SELECT user_id, 'simulated', 'clear'
FROM individual_profiles
WHERE kyc_status = 'approved_simulated'
ON CONFLICT DO NOTHING;
