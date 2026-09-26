-- Trust Fabric network layer, linked cases: when a ring (a connected
-- component of identities sharing devices, accounts or channel
-- identifiers) contains a member with a confirmed alert outcome, the
-- compliance queue raises ONE network case covering all members, so the
-- reviewer disposes of the ring once instead of alert-by-alert. Cases are
-- keyed by their sorted member set, so re-detection never duplicates an
-- open case; a new confirmed hit reopens the resolved case's member set
-- (history preserved in outcomes/audit).
CREATE TABLE IF NOT EXISTS network_cases (
    id bigserial PRIMARY KEY,
    members_key text NOT NULL UNIQUE,
    member_count integer NOT NULL,
    members jsonb NOT NULL DEFAULT '[]'::jsonb,
    link_kinds jsonb NOT NULL DEFAULT '[]'::jsonb,
    score double precision NOT NULL DEFAULT 0,
    confirmed_alerts integer NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','resolved','false_positive')),
    reviewer_email text NOT NULL DEFAULT '',
    outcome_note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS network_cases_status_idx
    ON network_cases(status, updated_at DESC);
