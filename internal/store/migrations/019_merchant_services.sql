-- Merchant-created services/events that customers can pay for.
-- Supports inventory tracking, expiry, and multi-use (e.g. movie ticket for 4).
CREATE TABLE IF NOT EXISTS merchant_services (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    unit_price_kobo bigint NOT NULL CHECK (unit_price_kobo > 0),
    quantity_available int NOT NULL DEFAULT -1,
    expires_at timestamptz,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS merchant_services_merchant_active_idx
    ON merchant_services(merchant_id, is_active);

CREATE TABLE IF NOT EXISTS service_purchases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id uuid NOT NULL REFERENCES merchant_services(id) ON DELETE CASCADE,
    payment_id uuid NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    quantity int NOT NULL CHECK (quantity > 0),
    unit_price_kobo bigint NOT NULL,
    total_kobo bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS service_purchases_service_idx
    ON service_purchases(service_id, created_at DESC);

CREATE INDEX IF NOT EXISTS service_purchases_payment_idx
    ON service_purchases(payment_id);

ALTER TABLE receipt_scan_tokens
    ADD COLUMN IF NOT EXISTS uses_total int NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS uses_remaining int NOT NULL DEFAULT 1;
