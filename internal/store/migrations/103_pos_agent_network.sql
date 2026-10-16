-- POS agent network (cash pickup remittance, v1 = cash-out only). A local agent
-- is a merchant classified `merchant_type='agent'`; it fronts physical cash to a
-- remittance recipient and Xego owes it on a dedicated ledger account
-- (2400_agent_float:<merchant-id>). The recipient needs no Xego account: the
-- sender's money sits on a per-remittance payable (2350_remittance_payable:<id>)
-- and is redeemed with a hashed pickup code.
--
-- Deliberately NO foreign keys to users or merchants (mirroring wallet_accounts
-- 058 and program_purses 100): retention and DSR purge delete those rows, and an
-- agent, region or remittance must never block a purge. Integrity is enforced in
-- the store.
--
-- Numeric limits (per-payout / daily / max outstanding float, commission) are
-- stored per agent so a tier default can change without a schema edit. Their
-- values are configuration, not schema: see POS_AGENT_NETWORK_PLAN.md §12.

-- ---------------------------------------------------------------------------
-- Agent classification on the existing merchant table.
-- ---------------------------------------------------------------------------
ALTER TABLE merchants ADD COLUMN IF NOT EXISTS merchant_type text NOT NULL DEFAULT 'standard';
-- Widen the classification CHECK to admit 'agent'. Migration 100 created the
-- constraint inline (auto-named merchants_merchant_type_check); drop it by
-- definition rather than by a guessed name, mirroring 073.
DO $$
DECLARE c record;
BEGIN
    FOR c IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'merchants'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%merchant_type%'
          AND pg_get_constraintdef(oid) LIKE '%program_acceptor%'
    LOOP
        EXECUTE format('ALTER TABLE merchants DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;

ALTER TABLE merchants
    ADD CONSTRAINT merchants_merchant_type_check
        CHECK (merchant_type IN ('standard', 'sponsor', 'program_acceptor', 'agent'));

-- ---------------------------------------------------------------------------
-- Regions: the geographic destination a sender chooses and an agent is scoped
-- to. Reuses the state/LGA vocabulary of migration 095.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS agent_regions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    state text NOT NULL DEFAULT '',
    lga text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agent_regions_state_idx ON agent_regions (state);

-- ---------------------------------------------------------------------------
-- Agent profile: the extension of a merchant that makes it a cash agent. No FK
-- to merchants (purge-safe); the merchant id is the key.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS agent_profiles (
    merchant_id uuid PRIMARY KEY,
    region_id uuid NOT NULL,
    tier text NOT NULL DEFAULT 'standard' CHECK (tier IN ('standard','super','max')),
    geo_lat double precision,
    geo_lng double precision,
    -- Fronted-cash model (D4): the agent may front up to this much before it
    -- must be settled. per_payout/daily bound a single and a day's payouts.
    max_outstanding_float_kobo bigint NOT NULL DEFAULT 0 CHECK (max_outstanding_float_kobo >= 0),
    per_payout_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (per_payout_limit_kobo >= 0),
    daily_payout_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (daily_payout_limit_kobo >= 0),
    commission_bps integer NOT NULL DEFAULT 0 CHECK (commission_bps >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','suspended')),
    agreement_ref text NOT NULL DEFAULT '',
    approved_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agent_profiles_region_idx ON agent_profiles (region_id, status);
CREATE INDEX IF NOT EXISTS agent_profiles_status_idx ON agent_profiles (status);

-- ---------------------------------------------------------------------------
-- Agent console identities. No FK to agent_profiles (purge-safe).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS agent_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_merchant_id uuid NOT NULL,
    name text NOT NULL DEFAULT '',
    phone text NOT NULL UNIQUE,
    email text NOT NULL DEFAULT '',
    password_hash text NOT NULL DEFAULT '',
    role text NOT NULL DEFAULT 'operator' CHECK (role IN ('owner','operator')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agent_users_agent_idx ON agent_users (agent_merchant_id);

-- ---------------------------------------------------------------------------
-- Remittance: the sender's instruction to pay a named recipient at one agent.
-- Money is held on 2350_remittance_payable:<remittance-id> until redeemed or
-- refunded.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS remittances (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_user_id uuid NOT NULL,
    agent_merchant_id uuid NOT NULL,
    region_id uuid NOT NULL,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    fee_kobo bigint NOT NULL DEFAULT 0 CHECK (fee_kobo >= 0),
    -- The agent's commission, credited to 2400_agent_float at collection and
    -- reversed on expiry (no service rendered). Stored so the reversal is exact
    -- even if the agent's bps later changes.
    commission_kobo bigint NOT NULL DEFAULT 0 CHECK (commission_kobo >= 0),
    recipient_name text NOT NULL,
    recipient_phone text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'awaiting_pickup'
        CHECK (status IN ('awaiting_pickup','paid','expired','refunded','cancelled')),
    payment_id uuid,
    expires_at timestamptz,
    paid_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS remittances_sender_idx ON remittances (sender_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS remittances_agent_idx ON remittances (agent_merchant_id, status);
CREATE INDEX IF NOT EXISTS remittances_status_expiry_idx ON remittances (status, expires_at);
-- One remittance per sender payment: the post-success hook can replay safely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_remittances_payment ON remittances (payment_id) WHERE payment_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Pickup claim: the hashed, single-use, expiring code. Never a plaintext code
-- column. Only one pending code per remittance; a reissue supersedes the old.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS remittance_pickups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    remittance_id uuid NOT NULL,
    code_hash text NOT NULL,
    code_salt text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','redeemed','expired','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    expires_at timestamptz NOT NULL,
    redeemed_at timestamptz,
    redeemed_by_agent_id uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS remittance_pickups_remittance_idx ON remittance_pickups (remittance_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS uq_remittance_pickup_pending
    ON remittance_pickups (remittance_id) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- OTP: the second factor the sender requests within the pickup window and
-- forwards to the recipient (D8). Hashed like the code; only one active OTP per
-- remittance, so a reissue supersedes the previous one. Never a plaintext OTP
-- column.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS remittance_otps (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    remittance_id uuid NOT NULL,
    otp_hash text NOT NULL,
    otp_salt text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','consumed','expired')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS remittance_otps_remittance_idx ON remittance_otps (remittance_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS uq_remittance_otp_active
    ON remittance_otps (remittance_id) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Payout record: the evidence that the agent paid a named, ID-checked person.
-- One payout per remittance.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS remittance_payouts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    remittance_id uuid NOT NULL UNIQUE,
    agent_merchant_id uuid NOT NULL,
    operator_user_id uuid,
    receiver_name text NOT NULL DEFAULT '',
    receiver_id_type text NOT NULL DEFAULT '',
    receiver_id_number_enc text NOT NULL DEFAULT '',
    -- Salted hash of the captured ID, the stable key that lets Xego sum a
    -- recipient's cash-outs across agents for the CBN per-customer daily/weekly
    -- ceiling. The plaintext ID is never queried or stored unsealed.
    receiver_id_hash text NOT NULL DEFAULT '',
    geo_lat double precision,
    geo_lng double precision,
    journal_ref text NOT NULL DEFAULT '',
    paid_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS remittance_payouts_agent_idx ON remittance_payouts (agent_merchant_id, paid_at DESC);
CREATE INDEX IF NOT EXISTS remittance_payouts_receiver_idx ON remittance_payouts (receiver_id_hash, paid_at DESC);

-- ---------------------------------------------------------------------------
-- Agent console sessions. Mirrors merchant_sessions: only the hash of the
-- session token is stored, and the CSRF token is sealed at rest. No FK
-- (purge-safe). The agent_user_id is the console identity; the
-- agent_merchant_id is carried alongside so a handler never has to re-resolve
-- the agent's profile to scope a payout.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS agent_sessions (
    token_hash bytea PRIMARY KEY,
    agent_user_id uuid NOT NULL,
    agent_merchant_id uuid NOT NULL,
    csrf_token text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agent_sessions_user_idx ON agent_sessions (agent_user_id);

-- ---------------------------------------------------------------------------
-- Admit the 'agent' portal as an MFA scope. The second-factor tables (071)
-- constrain scope to admin/merchant; the agent console reuses the same two-step
-- sign-in and step-up machinery, so widen the CHECK by definition rather than
-- by a guessed constraint name (mirroring the merchants CHECK above).
-- ---------------------------------------------------------------------------
DO $$
DECLARE c record;
BEGIN
    FOR c IN
        SELECT conname, conrelid::regclass AS tbl
        FROM pg_constraint
        WHERE contype = 'c'
          AND conrelid IN ('mfa_factors'::regclass, 'mfa_challenges'::regclass)
          AND pg_get_constraintdef(oid) LIKE '%scope%'
          AND pg_get_constraintdef(oid) LIKE '%merchant%'
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', c.tbl, c.conname);
    END LOOP;
END $$;

ALTER TABLE mfa_factors DROP CONSTRAINT IF EXISTS mfa_factors_scope_check;
ALTER TABLE mfa_challenges DROP CONSTRAINT IF EXISTS mfa_challenges_scope_check;
ALTER TABLE mfa_factors ADD CONSTRAINT mfa_factors_scope_check CHECK (scope IN ('admin','merchant','agent'));
ALTER TABLE mfa_challenges ADD CONSTRAINT mfa_challenges_scope_check CHECK (scope IN ('admin','merchant','agent'));
