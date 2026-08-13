-- Phase 3: transactional business-event outbox. Domain facts (payment
-- succeeded/failed, ...) are written atomically alongside the state change
-- that produces them, then a publisher drains the table onto the event bus
-- (in-memory Kafka-compatible bus or Kafka). At-least-once delivery means
-- consumers must be idempotent.
CREATE TABLE IF NOT EXISTS business_event_outbox (
    id bigserial PRIMARY KEY,
    topic text NOT NULL,
    event_key text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','publishing','published','failed')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS business_event_outbox_pending_idx
    ON business_event_outbox(status, available_at);
CREATE INDEX IF NOT EXISTS business_event_outbox_key_idx
    ON business_event_outbox(topic, event_key);

-- Idempotency marker for the Phase 3 notification consumer: a settled payment
-- gets one merchant notification, even if the event is redelivered.
ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS merchant_notified_at timestamptz;
