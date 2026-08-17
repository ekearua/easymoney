-- R4.1: Promote idempotency_key from JSONB metadata to a proper column with
-- a unique index so the Partner API can enforce at-most-once creation.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS idempotency_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS payments_idempotency_key_idx
    ON payments(merchant_id, idempotency_key)
    WHERE idempotency_key <> '';

-- Backfill from existing metadata for any payments created through the API.
UPDATE payments
SET idempotency_key = COALESCE(metadata->>'idempotency_key', '')
WHERE idempotency_key = ''
  AND metadata->>'idempotency_key' IS NOT NULL
  AND metadata->>'idempotency_key' <> '';

-- R4.2: Prevent duplicate business events at the DB level.
-- The state machine is monotonic so duplicates are rare, but this is a safety net.
ALTER TABLE business_event_outbox ADD COLUMN IF NOT EXISTS dedup_key text;
UPDATE business_event_outbox SET dedup_key = topic || ':' || event_key WHERE dedup_key IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS business_event_outbox_dedup_idx
    ON business_event_outbox(dedup_key)
    WHERE dedup_key IS NOT NULL;
