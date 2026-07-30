-- Custom fields per service that merchants can define to collect extra data
-- from customers at purchase time (e.g. seat number, preferred date).
CREATE TABLE IF NOT EXISTS service_custom_fields (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id uuid NOT NULL REFERENCES merchant_services(id) ON DELETE CASCADE,
    field_name text NOT NULL,
    field_type text NOT NULL DEFAULT 'text',
    field_options text NOT NULL DEFAULT '',
    is_required boolean NOT NULL DEFAULT false,
    sort_order int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS service_custom_fields_service_idx
    ON service_custom_fields(service_id, sort_order);

CREATE TABLE IF NOT EXISTS service_purchase_custom_data (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_id uuid NOT NULL REFERENCES service_purchases(id) ON DELETE CASCADE,
    field_id uuid NOT NULL REFERENCES service_custom_fields(id) ON DELETE CASCADE,
    field_name text NOT NULL,
    field_value text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS service_purchase_custom_data_purchase_idx
    ON service_purchase_custom_data(purchase_id);
