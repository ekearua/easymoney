-- Add Telegram as a parallel customer channel while preserving existing
-- WhatsApp users, queue rows, and payment records.
ALTER TABLE users
    ALTER COLUMN whatsapp_number DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS telegram_chat_id text UNIQUE,
    ADD COLUMN IF NOT EXISTS telegram_user_id text,
    ADD COLUMN IF NOT EXISTS telegram_username text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS telegram_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS telegram_confirmed_at timestamptz;

ALTER TABLE inbound_messages
    ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT 'whatsapp',
    ADD COLUMN IF NOT EXISTS recipient text NOT NULL DEFAULT '';

UPDATE inbound_messages
SET channel = COALESCE(NULLIF(channel, ''), 'whatsapp'),
    recipient = CASE WHEN recipient = '' THEN sender ELSE recipient END;

ALTER TABLE message_outbox
    ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT 'whatsapp';

ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT 'whatsapp',
    ADD COLUMN IF NOT EXISTS recipient text NOT NULL DEFAULT '';

UPDATE payments p
SET channel = COALESCE(NULLIF(p.channel, ''), 'whatsapp'),
    recipient = CASE WHEN p.recipient = '' THEN u.whatsapp_number ELSE p.recipient END
FROM users u
WHERE p.user_id = u.id;
