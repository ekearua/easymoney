-- Rotational thrift contribution support for approved individual users.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS account_level text NOT NULL DEFAULT 'customer';

CREATE TABLE IF NOT EXISTS individual_profiles (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    legal_name text NOT NULL,
    date_of_birth date NOT NULL,
    address text NOT NULL,
    occupation text NOT NULL,
    kyc_status text NOT NULL DEFAULT 'approved_simulated',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO merchants(slug,name,category,description,active,search_keywords,sort_order)
VALUES(
    'xego-thrift-contributions',
    'Xego Thrift Contributions',
    'Thrift',
    'System recipient for Xego thrift contribution payments.',
    false,
    'xego thrift contribution ajo rotational savings',
    9999
)
ON CONFLICT(slug) DO UPDATE
SET name=EXCLUDED.name,
    category=EXCLUDED.category,
    description=EXCLUDED.description,
    active=false,
    search_keywords=EXCLUDED.search_keywords,
    updated_at=now();

CREATE TABLE IF NOT EXISTS thrift_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    contribution_amount_kobo bigint NOT NULL CHECK (contribution_amount_kobo > 0),
    frequency text NOT NULL CHECK (frequency IN ('weekly','monthly')),
    target_member_count integer NOT NULL CHECK (target_member_count BETWEEN 2 AND 12),
    invite_code text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'inviting',
    current_cycle integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz,
    completed_at timestamptz,
    cancelled_at timestamptz
);

CREATE INDEX IF NOT EXISTS thrift_groups_creator_idx ON thrift_groups(creator_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS thrift_groups_status_idx ON thrift_groups(status, updated_at);

CREATE TABLE IF NOT EXISTS thrift_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id uuid NOT NULL REFERENCES thrift_groups(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'confirmed',
    payout_position integer,
    joined_at timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz,
    UNIQUE(group_id, user_id),
    UNIQUE(group_id, payout_position)
);

CREATE INDEX IF NOT EXISTS thrift_members_user_idx ON thrift_members(user_id, joined_at DESC);

CREATE TABLE IF NOT EXISTS thrift_cycles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id uuid NOT NULL REFERENCES thrift_groups(id) ON DELETE CASCADE,
    cycle_number integer NOT NULL CHECK (cycle_number > 0),
    due_at timestamptz NOT NULL,
    payout_member_id uuid NOT NULL REFERENCES thrift_members(id),
    status text NOT NULL DEFAULT 'pending_contributions',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(group_id, cycle_number)
);

CREATE INDEX IF NOT EXISTS thrift_cycles_group_idx ON thrift_cycles(group_id, cycle_number DESC);
CREATE INDEX IF NOT EXISTS thrift_cycles_status_idx ON thrift_cycles(status, due_at);

CREATE TABLE IF NOT EXISTS thrift_contributions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cycle_id uuid NOT NULL REFERENCES thrift_cycles(id) ON DELETE CASCADE,
    member_id uuid NOT NULL REFERENCES thrift_members(id) ON DELETE CASCADE,
    payment_id uuid UNIQUE REFERENCES payments(id) ON DELETE SET NULL,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    status text NOT NULL DEFAULT 'awaiting_payment',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    paid_at timestamptz,
    UNIQUE(cycle_id, member_id)
);

CREATE INDEX IF NOT EXISTS thrift_contributions_status_idx ON thrift_contributions(status, updated_at);

CREATE TABLE IF NOT EXISTS thrift_payouts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cycle_id uuid NOT NULL UNIQUE REFERENCES thrift_cycles(id) ON DELETE CASCADE,
    payout_member_id uuid NOT NULL REFERENCES thrift_members(id),
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS thrift_payouts_status_idx ON thrift_payouts(status, updated_at);

CREATE TABLE IF NOT EXISTS thrift_events (
    id bigserial PRIMARY KEY,
    group_id uuid REFERENCES thrift_groups(id) ON DELETE CASCADE,
    cycle_id uuid REFERENCES thrift_cycles(id) ON DELETE CASCADE,
    member_id uuid REFERENCES thrift_members(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
