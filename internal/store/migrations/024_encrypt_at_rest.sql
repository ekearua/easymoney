-- Application-layer encryption at rest (C7). Chat payload columns move from
-- jsonb to text so crypto.Seal envelopes can be stored. Existing plaintext
-- rows keep their JSON text; the application re-encrypts them in batches via
-- EncryptLegacyAtRest on startup/migrate.
ALTER TABLE inbound_messages ALTER COLUMN payload TYPE text USING payload::text;
ALTER TABLE message_outbox ALTER COLUMN payload TYPE text USING payload::text;
