-- Merchant staff accounts for bulk payments. A merchant keeps its owner
-- (merchant_owners, full access) and gains two scoped staff roles:
--   records  - maintains the recipient list and its identity documents
--   payments - builds filtered batches and releases them
-- Owner sessions stay member_id = NULL; staff sessions carry member_id and the
-- role so the console can gate routes without re-reading the member row.
CREATE TABLE IF NOT EXISTS merchant_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL DEFAULT '',
    role text NOT NULL CHECK (role IN ('records', 'payments')),
    status text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'active', 'disabled')),
    invite_token_hash text NOT NULL DEFAULT '',
    invite_expires_at timestamptz,
    invited_by_owner boolean NOT NULL DEFAULT false,
    invited_by_member uuid REFERENCES merchant_members(id) ON DELETE SET NULL,
    activated_at timestamptz,
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS merchant_members_merchant_idx
    ON merchant_members (merchant_id, created_at DESC);

-- Password hash uniqueness across owners and staff: a staff email must not
-- shadow an owner login. Owners sign in through merchant_owners, so the member
-- lookup runs first and the owner path is only tried when no member matches.

ALTER TABLE merchant_sessions
    ADD COLUMN IF NOT EXISTS member_id uuid REFERENCES merchant_members(id) ON DELETE CASCADE;
ALTER TABLE merchant_sessions
    ADD COLUMN IF NOT EXISTS member_role text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS merchant_sessions_member_idx ON merchant_sessions (member_id);
