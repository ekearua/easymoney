-- Add Facebook Messenger as a parallel customer channel while preserving
-- existing WhatsApp/Telegram/Instagram/TikTok users, queue rows, and payment
-- records. Messenger identity is the Page-scoped ID (PSID) of the customer
-- for the configured Facebook Page.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS facebook_psid text UNIQUE,
    ADD COLUMN IF NOT EXISTS facebook_username text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS facebook_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS facebook_confirmed_at timestamptz;
