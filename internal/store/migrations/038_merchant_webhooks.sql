-- Outbound merchant webhook delivery (P2). Merchants register a callback URL
-- and receive signed payment.succeeded / payment.failed notifications. The
-- webhook secret is the symmetric HMAC key the merchant uses to verify payloads
-- (Paystack-style) and is sealed at rest like chat payloads; deliveries use the
-- same claim/retry/backoff shape as message_outbox.
ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS webhook_url text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS webhook_secret text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS merchant_webhook_deliveries (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merchant_id  uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    payment_id   uuid NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    event        text NOT NULL,
    payload      text NOT NULL,
    status       text NOT NULL DEFAULT 'pending',
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text NOT NULL DEFAULT '',
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One delivery per (merchant, payment, event): redelivered bus events are no-ops.
CREATE UNIQUE INDEX IF NOT EXISTS merchant_webhook_deliveries_uniq_idx
    ON merchant_webhook_deliveries(merchant_id, payment_id, event);
CREATE INDEX IF NOT EXISTS merchant_webhook_deliveries_status_idx
    ON merchant_webhook_deliveries(status, available_at);
