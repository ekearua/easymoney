-- Web-flow payloads now carry OCR'd identity text from NIN/BVN slip uploads
-- plus profile details, so they move from jsonb to text and get sealed at
-- rest with the same crypto.Seal envelope used for chat payloads (migration
-- 024). The payment_id a flow carries is promoted to its own column first:
-- gateway callbacks resolve the flow by payment id, and that lookup must stay
-- indexable even though the payload blob is opaque.
ALTER TABLE web_flows ADD COLUMN IF NOT EXISTS payment_id uuid;

-- Backfill payment_id from existing open flows' payloads before the column
-- type changes (jsonb operators stop working once the payload is a sealed
-- text envelope).
UPDATE web_flows
SET payment_id = (payload->>'payment_id')::uuid
WHERE status = 'open'
  AND payment_id IS NULL
  AND payload ? 'payment_id'
  AND (payload->>'payment_id') ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$';

CREATE INDEX IF NOT EXISTS web_flows_payment_idx ON web_flows (payment_id) WHERE payment_id IS NOT NULL;

ALTER TABLE web_flows ALTER COLUMN payload TYPE text USING payload::text;
ALTER TABLE web_flows ALTER COLUMN payload SET DEFAULT '{}'::text;
