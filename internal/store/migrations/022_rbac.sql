-- RBAC for the admin console. The single config-driven admin is replaced by
-- named admin accounts with roles. The bootstrap account (created from
-- ADMIN_EMAIL / ADMIN_PASSWORD_HASH) always gets the 'admin' role.
CREATE TABLE IF NOT EXISTS admin_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'compliance', 'support', 'readonly')),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE admin_sessions
    ADD COLUMN IF NOT EXISTS admin_id uuid REFERENCES admin_users(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS admin_sessions_admin_idx ON admin_sessions(admin_id);
