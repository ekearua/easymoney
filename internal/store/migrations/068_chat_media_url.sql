-- 068_chat_media_url.sql
-- Adds a dedicated media_url column to inbound_messages so chat-path media
-- downloads use the provider-supplied URL instead of nil bytes.  The column is
-- backfilled with an empty string to preserve existing rows.

ALTER TABLE inbound_messages ADD COLUMN IF NOT EXISTS media_url text NOT NULL DEFAULT '';
