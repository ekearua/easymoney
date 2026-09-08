-- Add Instagram Messaging and TikTok Business Messaging as parallel customer
-- channels while preserving existing WhatsApp/Telegram users, queue rows, and
-- payment records. Instagram identity is the Instagram-scoped ID (IGSID);
-- TikTok identity is the open_id with the account-level union_id kept for
-- cross-app resolution.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS instagram_igsid text UNIQUE,
    ADD COLUMN IF NOT EXISTS instagram_username text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS instagram_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS instagram_confirmed_at timestamptz,
    ADD COLUMN IF NOT EXISTS tiktok_open_id text UNIQUE,
    ADD COLUMN IF NOT EXISTS tiktok_union_id text,
    ADD COLUMN IF NOT EXISTS tiktok_username text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tiktok_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS tiktok_confirmed_at timestamptz;
