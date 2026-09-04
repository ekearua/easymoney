-- Web flows power the low-cost conversation model: after the first WhatsApp
-- message (the link), the customer completes the flow in the browser and the
-- only further WhatsApp message is the confirmation. Each row is one
-- capability token minted in response to a user-initiated chat message.
CREATE TABLE IF NOT EXISTS web_flows (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token        text NOT NULL UNIQUE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel      text NOT NULL DEFAULT 'whatsapp',
    flow_type    text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    step         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'open', -- open | complete | expired
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS web_flows_user_open_idx ON web_flows (user_id, status) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS web_flows_expires_idx ON web_flows (status, expires_at) WHERE status = 'open';

-- Outbound message ledger: one row per customer-facing message so the
-- messaging cost meter can report per-flow volumes and the average number of
-- messages per transaction. Meta rates stay in configuration, not code.
CREATE TABLE IF NOT EXISTS message_log (
    id           bigserial PRIMARY KEY,
    channel      text NOT NULL DEFAULT 'whatsapp',
    recipient    text NOT NULL DEFAULT '',
    flow         text NOT NULL DEFAULT '',
    message_type text NOT NULL DEFAULT 'text',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS message_log_created_idx ON message_log (created_at);
CREATE INDEX IF NOT EXISTS message_log_flow_idx ON message_log (flow, created_at);
