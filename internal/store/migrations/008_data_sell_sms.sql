-- Data sales catalog, order lifecycle, and SMS request-code support.
INSERT INTO merchants (slug, name, category, description, active, search_keywords, sort_order)
VALUES ('xego-data', 'Xego Data', 'Telecoms', 'Mobile data bundles for Nigerian networks.', true, 'data mtn airtel glo 9mobile telecoms', 5)
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    category = EXCLUDED.category,
    description = EXCLUDED.description,
    active = EXCLUDED.active,
    search_keywords = EXCLUDED.search_keywords,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();

CREATE TABLE IF NOT EXISTS data_networks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    sort_order integer NOT NULL DEFAULT 1000,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS data_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    network_id uuid NOT NULL REFERENCES data_networks(id),
    code text NOT NULL UNIQUE,
    display_name text NOT NULL,
    data_size text NOT NULL,
    validity text NOT NULL,
    price_kobo bigint NOT NULL CHECK (price_kobo > 0),
    provider_sku text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    sort_order integer NOT NULL DEFAULT 1000,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS data_plans_network_idx ON data_plans(network_id, active, sort_order);

CREATE TABLE IF NOT EXISTS data_orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    payment_id uuid UNIQUE REFERENCES payments(id) ON DELETE SET NULL,
    channel text NOT NULL DEFAULT 'whatsapp',
    recipient text NOT NULL DEFAULT '',
    beneficiary_phone text NOT NULL,
    network_id uuid NOT NULL REFERENCES data_networks(id),
    plan_id uuid NOT NULL REFERENCES data_plans(id),
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    status text NOT NULL,
    request_code text NOT NULL UNIQUE,
    provider text NOT NULL DEFAULT 'simulated_data',
    provider_reference text NOT NULL DEFAULT '',
    failure_reason text NOT NULL DEFAULT '',
    fulfilled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS data_orders_user_created_idx ON data_orders(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS data_orders_status_idx ON data_orders(status, updated_at);
CREATE INDEX IF NOT EXISTS data_orders_payment_idx ON data_orders(payment_id);

CREATE TABLE IF NOT EXISTS data_order_events (
    id bigserial PRIMARY KEY,
    data_order_id uuid NOT NULL REFERENCES data_orders(id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status text NOT NULL,
    source text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sms_requests (
    provider_message_id text PRIMARY KEY,
    sender text NOT NULL,
    body text NOT NULL,
    command text NOT NULL DEFAULT '',
    request_code text NOT NULL DEFAULT '',
    processing_status text NOT NULL DEFAULT 'received',
    response_body text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX IF NOT EXISTS sms_requests_received_idx ON sms_requests(received_at DESC);

INSERT INTO data_networks (code, name, active, sort_order) VALUES
    ('MTN', 'MTN', true, 10),
    ('AIRTEL', 'Airtel', true, 20),
    ('GLO', 'Glo', true, 30),
    ('9MOBILE', '9mobile', true, 40)
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    active = EXCLUDED.active,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();

INSERT INTO data_plans (network_id, code, display_name, data_size, validity, price_kobo, provider_sku, active, sort_order)
SELECT n.id, p.code, p.display_name, p.data_size, p.validity, p.price_kobo, p.provider_sku, true, p.sort_order
FROM data_networks n
JOIN (VALUES
    ('MTN','MTN500MB','MTN 500MB','500MB','30 days',25000,'SIM-MTN-500MB',10),
    ('MTN','MTN1GB','MTN 1GB','1GB','30 days',50000,'SIM-MTN-1GB',20),
    ('MTN','MTN2GB','MTN 2GB','2GB','30 days',100000,'SIM-MTN-2GB',30),
    ('AIRTEL','AIRTEL500MB','Airtel 500MB','500MB','30 days',25000,'SIM-AIRTEL-500MB',10),
    ('AIRTEL','AIRTEL1GB','Airtel 1GB','1GB','30 days',50000,'SIM-AIRTEL-1GB',20),
    ('AIRTEL','AIRTEL2GB','Airtel 2GB','2GB','30 days',100000,'SIM-AIRTEL-2GB',30),
    ('GLO','GLO500MB','Glo 500MB','500MB','30 days',23000,'SIM-GLO-500MB',10),
    ('GLO','GLO1GB','Glo 1GB','1GB','30 days',46000,'SIM-GLO-1GB',20),
    ('GLO','GLO2GB','Glo 2GB','2GB','30 days',92000,'SIM-GLO-2GB',30),
    ('9MOBILE','9MOBILE500MB','9mobile 500MB','500MB','30 days',26000,'SIM-9MOBILE-500MB',10),
    ('9MOBILE','9MOBILE1GB','9mobile 1GB','1GB','30 days',52000,'SIM-9MOBILE-1GB',20),
    ('9MOBILE','9MOBILE2GB','9mobile 2GB','2GB','30 days',104000,'SIM-9MOBILE-2GB',30)
) AS p(network_code, code, display_name, data_size, validity, price_kobo, provider_sku, sort_order)
ON n.code = p.network_code
ON CONFLICT (code) DO UPDATE SET
    network_id = EXCLUDED.network_id,
    display_name = EXCLUDED.display_name,
    data_size = EXCLUDED.data_size,
    validity = EXCLUDED.validity,
    price_kobo = EXCLUDED.price_kobo,
    provider_sku = EXCLUDED.provider_sku,
    active = EXCLUDED.active,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();
