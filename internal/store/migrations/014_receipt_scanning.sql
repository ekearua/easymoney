-- QR-first receipt scanning for registered services. NFC can later encode the
-- same scan URLs into tags/cards or phone NFC flows.
CREATE TABLE IF NOT EXISTS registered_services (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    service_type text NOT NULL,
    merchant_id uuid REFERENCES merchants(id),
    accepted_receipt_types text NOT NULL DEFAULT 'merchant_payment',
    token_ttl_seconds integer NOT NULL DEFAULT 86400 CHECK (token_ttl_seconds > 0),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(service_type, merchant_id)
);

CREATE INDEX IF NOT EXISTS registered_services_merchant_idx
    ON registered_services(merchant_id, active);

CREATE TABLE IF NOT EXISTS service_readers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id uuid NOT NULL REFERENCES registered_services(id) ON DELETE CASCADE,
    name text NOT NULL,
    api_key_hash bytea NOT NULL UNIQUE,
    key_prefix text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS service_readers_service_idx
    ON service_readers(service_id, active);

CREATE TABLE IF NOT EXISTS receipt_scan_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id uuid NOT NULL UNIQUE REFERENCES payments(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES registered_services(id) ON DELETE CASCADE,
    token text NOT NULL UNIQUE,
    manual_code text NOT NULL UNIQUE,
    receipt_type text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS receipt_scan_tokens_service_idx
    ON receipt_scan_tokens(service_id, created_at DESC);

CREATE TABLE IF NOT EXISTS receipt_scan_attempts (
    id bigserial PRIMARY KEY,
    token_id uuid REFERENCES receipt_scan_tokens(id) ON DELETE SET NULL,
    service_id uuid REFERENCES registered_services(id) ON DELETE SET NULL,
    reader_id uuid REFERENCES service_readers(id) ON DELETE SET NULL,
    status text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    remote_addr text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS receipt_scan_attempts_created_idx
    ON receipt_scan_attempts(created_at DESC);

-- Xego-owned services validate Xego product receipts.
INSERT INTO registered_services(name,service_type,merchant_id,accepted_receipt_types,token_ttl_seconds,active)
SELECT 'Xego Data', 'xego_data', m.id, 'data', 86400, true
FROM merchants m
WHERE m.slug='xego-data'
ON CONFLICT(service_type, merchant_id) DO UPDATE
SET name=EXCLUDED.name,
    accepted_receipt_types=EXCLUDED.accepted_receipt_types,
    active=true,
    updated_at=now();

INSERT INTO registered_services(name,service_type,merchant_id,accepted_receipt_types,token_ttl_seconds,active)
SELECT 'Xego Thrift', 'xego_thrift', m.id, 'thrift', 86400, true
FROM merchants m
WHERE m.slug='xego-thrift-contributions'
ON CONFLICT(service_type, merchant_id) DO UPDATE
SET name=EXCLUDED.name,
    accepted_receipt_types=EXCLUDED.accepted_receipt_types,
    active=true,
    updated_at=now();

-- Existing active non-system merchants become registered merchant services.
INSERT INTO registered_services(name,service_type,merchant_id,accepted_receipt_types,token_ttl_seconds,active)
SELECT m.name, 'merchant', m.id, 'merchant_payment,invoice', 86400, true
FROM merchants m
WHERE m.active=true
  AND m.slug NOT IN ('xego-data','xego-thrift-contributions')
ON CONFLICT(service_type, merchant_id) DO UPDATE
SET name=EXCLUDED.name,
    accepted_receipt_types=EXCLUDED.accepted_receipt_types,
    active=true,
    updated_at=now();
