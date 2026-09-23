-- 069_chat_media_stats.sql
-- Observability for the channel media pipeline. On inbound_messages,
-- media_ok records the OCR/STT outcome for chat media: TRUE = extraction
-- returned text, FALSE = provider error or empty result, NULL = never
-- extracted (AI disabled, unsupported type). The extracted text itself stays
-- only in the encrypted payload — no plaintext duplication. media_ai_tokens
-- carries the AI provider's reported usage for the extraction so AI spend is
-- visible per channel per day.

ALTER TABLE inbound_messages
    ADD COLUMN IF NOT EXISTS media_ok boolean,
    ADD COLUMN IF NOT EXISTS media_ai_tokens integer NOT NULL DEFAULT 0;

-- Web-flow media extractions (browser uploads/voice notes) land here; chat
-- extractions are recorded on inbound_messages itself. The report unions the
-- two sources per channel per day.
CREATE TABLE IF NOT EXISTS media_usage_log (
    id          bigserial PRIMARY KEY,
    channel     text NOT NULL,
    media_kind  text NOT NULL, -- image | voice
    success     boolean NOT NULL,
    ai_tokens   bigint NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS media_usage_log_created_idx ON media_usage_log (created_at);
