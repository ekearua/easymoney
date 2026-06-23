CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    whatsapp_number text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    onboarding_complete boolean NOT NULL DEFAULT false,
    last_inbound_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS merchants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    category text NOT NULL,
    description text NOT NULL,
    logo_url text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS conversation_sessions (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state text NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS payments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    merchant_id uuid NOT NULL REFERENCES merchants(id),
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency text NOT NULL DEFAULT 'NGN',
    status text NOT NULL,
    provider text NOT NULL DEFAULT 'paystack',
    provider_reference text NOT NULL UNIQUE,
    checkout_url text NOT NULL DEFAULT '',
    receipt_token text NOT NULL UNIQUE,
    failure_reason text NOT NULL DEFAULT '',
    paid_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS payments_user_created_idx ON payments(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS payments_status_idx ON payments(status, updated_at);

CREATE TABLE IF NOT EXISTS payment_events (
    id bigserial PRIMARY KEY,
    payment_id uuid NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status text NOT NULL,
    source text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id bigserial PRIMARY KEY,
    provider text NOT NULL,
    event_key text NOT NULL,
    signature_valid boolean NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    processing_status text NOT NULL DEFAULT 'received',
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    error_message text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE(provider, event_key)
);

CREATE TABLE IF NOT EXISTS inbound_messages (
    provider_message_id text PRIMARY KEY,
    sender text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    last_error text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS inbound_messages_pending_idx ON inbound_messages(status, available_at);

CREATE TABLE IF NOT EXISTS message_outbox (
    id bigserial PRIMARY KEY,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    recipient text NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS message_outbox_pending_idx ON message_outbox(status, available_at);

CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash bytea PRIMARY KEY,
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
