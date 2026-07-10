-- Merchant registration requests submitted from chat. These are review queue
-- records only; they do not become payable merchants until an operator reviews
-- and adds/activates a merchant separately.
CREATE TABLE IF NOT EXISTS merchant_registrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reference text NOT NULL UNIQUE,
    business_name text NOT NULL,
    category text NOT NULL,
    description text NOT NULL,
    contact_email text NOT NULL,
    status text NOT NULL DEFAULT 'awaiting_approval',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS merchant_registrations_status_idx
    ON merchant_registrations(status, created_at DESC);

CREATE INDEX IF NOT EXISTS merchant_registrations_user_idx
    ON merchant_registrations(user_id, created_at DESC);
