-- C18: chat content guard attempt log. Inbound WhatsApp/Telegram messages that
-- try to send card numbers, PINs, CVVs, or OTPs into the conversation are
-- blocked at the service layer; each attempt is recorded here (with the
-- credential redacted) so PCI / CBN consumer-protection incidents can be
-- reviewed without ever persisting payment-credential material.
CREATE TABLE IF NOT EXISTS chat_guard_events (
    id bigserial PRIMARY KEY,
    message_id text NOT NULL DEFAULT '',
    channel text NOT NULL DEFAULT '',
    sender text NOT NULL DEFAULT '',
    recipient text NOT NULL DEFAULT '',
    category text NOT NULL,
    redacted_text text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chat_guard_events_created_idx ON chat_guard_events(created_at DESC);
CREATE INDEX IF NOT EXISTS chat_guard_events_sender_idx ON chat_guard_events(sender);