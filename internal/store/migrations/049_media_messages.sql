-- 049: Add media columns to inbound_messages for image/voice/video support.
-- The media data is stored in the encrypted JSON payload blob (media_type,
-- media_id, media_mime, caption). This migration adds dedicated columns for
-- indexing and query convenience without changing the existing payload contract.

ALTER TABLE inbound_messages
    ADD COLUMN IF NOT EXISTS media_type text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_mime text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS caption text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_inbound_messages_media_id ON inbound_messages (media_id) WHERE media_id != '';
