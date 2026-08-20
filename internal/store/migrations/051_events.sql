-- Dedicated events feature: merchants create events with ticket tiers,
-- customers purchase tickets over WhatsApp, check-in via the existing scanner.
CREATE TABLE IF NOT EXISTS merchant_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    venue text NOT NULL DEFAULT '',
    event_start_at timestamptz,
    event_end_at timestamptz,
    image_url text NOT NULL DEFAULT '',
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS merchant_events_merchant_active_idx
    ON merchant_events(merchant_id, is_active);

CREATE TABLE IF NOT EXISTS event_ticket_tiers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES merchant_events(id) ON DELETE CASCADE,
    name text NOT NULL,
    price_kobo bigint NOT NULL CHECK (price_kobo > 0),
    capacity int NOT NULL DEFAULT -1,
    sold int NOT NULL DEFAULT 0,
    sort_order int NOT NULL DEFAULT 0,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS event_ticket_tiers_event_idx
    ON event_ticket_tiers(event_id, sort_order);

CREATE TABLE IF NOT EXISTS event_ticket_purchases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id uuid NOT NULL REFERENCES event_ticket_tiers(id) ON DELETE CASCADE,
    payment_id uuid NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    quantity int NOT NULL CHECK (quantity > 0),
    unit_price_kobo bigint NOT NULL,
    total_kobo bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS event_ticket_purchases_tier_idx
    ON event_ticket_purchases(tier_id, created_at DESC);

CREATE INDEX IF NOT EXISTS event_ticket_purchases_payment_idx
    ON event_ticket_purchases(payment_id);

CREATE TABLE IF NOT EXISTS event_custom_fields (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id uuid NOT NULL REFERENCES event_ticket_tiers(id) ON DELETE CASCADE,
    field_name text NOT NULL,
    field_type text NOT NULL DEFAULT 'text',
    field_options text NOT NULL DEFAULT '',
    is_required boolean NOT NULL DEFAULT false,
    sort_order int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS event_custom_fields_tier_idx
    ON event_custom_fields(tier_id, sort_order);

CREATE TABLE IF NOT EXISTS event_purchase_custom_data (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_id uuid NOT NULL REFERENCES event_ticket_purchases(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES event_custom_fields(id) ON DELETE CASCADE,
    field_name text NOT NULL,
    field_value text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS event_purchase_custom_data_purchase_idx
    ON event_purchase_custom_data(purchase_id);
