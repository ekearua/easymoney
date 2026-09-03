-- C9-tiers: KYC/KYB allowance ladder. Per-account-tier payment ceilings replace
-- the old flat global cap as the primary limit; the global PAYMENT_* env bounds
-- remain as absolute platform floors/ceilings on top of the tier caps.
--
-- kyc_tier_limits       admin-editable limits per (account type, tier, direction)
-- allowance_usage       append-only record of money-in/out against a subject,
--                       keyed by transaction_ref so re-application is a no-op
-- business_kyb_profiles the KYB twin of kyc_profiles for merchant accounts

CREATE TABLE IF NOT EXISTS kyc_tier_limits (
    id               bigserial PRIMARY KEY,
    account_type     text NOT NULL CHECK (account_type IN ('individual','business')),
    tier             text NOT NULL,
    direction        text NOT NULL CHECK (direction IN ('in','out')),
    single_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (single_limit_kobo >= 0),
    daily_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (daily_limit_kobo >= 0),
    monthly_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (monthly_limit_kobo >= 0),
    updated_by       uuid REFERENCES admin_users(id),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_type, tier, direction),
    CHECK (
        (account_type = 'individual' AND tier IN ('L0','L1','L2','L3','L4'))
        OR (account_type = 'business' AND tier IN ('B0','B1','B2','B3'))
    )
);

-- CBN-aligned default ceilings (kobo), single/daily/monthly, equal for in/out.
-- Administrators may tune these through the admin console; the rows below are
-- only defaults and are safe to re-apply on fresh installs.
INSERT INTO kyc_tier_limits (account_type, tier, direction, single_limit_kobo, daily_limit_kobo, monthly_limit_kobo) VALUES
    ('individual','L0','in',   2000000,   2000000,    10000000),
    ('individual','L0','out',  2000000,   2000000,    10000000),
    ('individual','L1','in',   5000000,   5000000,    30000000),
    ('individual','L1','out',  5000000,   5000000,    30000000),
    ('individual','L2','in',   20000000,  20000000,   50000000),
    ('individual','L2','out',  20000000,  20000000,   50000000),
    ('individual','L3','in',   100000000, 100000000,  1000000000),
    ('individual','L3','out',  100000000, 100000000,  1000000000),
    ('individual','L4','in',   500000000, 500000000,  5000000000),
    ('individual','L4','out',  500000000, 500000000,  5000000000),
    ('business','B0','in',     20000000,  20000000,   100000000),
    ('business','B0','out',    20000000,  20000000,   100000000),
    ('business','B1','in',     100000000, 100000000,  500000000),
    ('business','B1','out',    100000000, 100000000,  500000000),
    ('business','B2','in',     500000000, 500000000,  5000000000),
    ('business','B2','out',    500000000, 500000000,  5000000000),
    ('business','B3','in',     1000000000,1000000000, 10000000000),
    ('business','B3','out',    1000000000,1000000000, 10000000000)
ON CONFLICT (account_type, tier, direction) DO NOTHING;

-- Append-only usage. recorded_at drives the daily (day boundary) and monthly
-- rollups; transaction_ref guarantees idempotent application of the same
-- payment/payout record.
CREATE TABLE IF NOT EXISTS allowance_usage (
    id              bigserial PRIMARY KEY,
    account_type    text NOT NULL CHECK (account_type IN ('individual','business')),
    subject_id      uuid NOT NULL,
    direction       text NOT NULL CHECK (direction IN ('in','out')),
    tier_at_time    text NOT NULL,
    amount_kobo     bigint NOT NULL CHECK (amount_kobo > 0),
    transaction_ref text NOT NULL,
    recorded_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (transaction_ref)
);

CREATE INDEX IF NOT EXISTS allowance_usage_subject_idx
    ON allowance_usage (account_type, subject_id, direction, recorded_at);
CREATE INDEX IF NOT EXISTS allowance_usage_period_idx
    ON allowance_usage (subject_id, recorded_at);

-- KYB profile for merchant accounts: mirrors kyc_profiles but keyed on the
-- merchant rather than a user, with the B0-B3 business tier ladder.
CREATE TABLE IF NOT EXISTS business_kyb_profiles (
    merchant_id uuid PRIMARY KEY REFERENCES merchants(id) ON DELETE CASCADE,
    tier text NOT NULL DEFAULT 'B0' CHECK (tier IN ('B0','B1','B2','B3')),
    tier_updated_at timestamptz NOT NULL DEFAULT now(),
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_screening_decision text,
    last_screen_at timestamptz,
    rescreen_due timestamptz,
    review_status text NOT NULL DEFAULT 'none' CHECK (review_status IN ('none','pending','approved','rejected')),
    reviewed_by uuid REFERENCES admin_users(id),
    reviewed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS business_kyb_profiles_review_idx
    ON business_kyb_profiles (review_status, updated_at);
CREATE INDEX IF NOT EXISTS business_kyb_profiles_rescreen_idx
    ON business_kyb_profiles (rescreen_due);

-- Grandfather every existing merchant into the baseline B0 tier with the
-- registration-approval evidence they already hold so KYB enforcement starts
-- from the current behaviour instead of blocking live merchants. They are
-- treated as screened at migration time with a normal rescreen window.
INSERT INTO business_kyb_profiles (merchant_id, tier, evidence, last_screening_decision, last_screen_at, rescreen_due, review_status)
SELECT m.id, 'B0', '["registration_approved"]'::jsonb, 'clear', now(), now() + interval '90 days', 'approved'
FROM merchants m
ON CONFLICT (merchant_id) DO NOTHING;