-- Onboarding reminders (chat-side nudge for incomplete onboarding). One row
-- per actual reminder attempt, written BEFORE the message_outbox row so the
-- unique index on (user_id, stage, attempt_no) arbitrates concurrent sweeps:
-- whoever loses the insert skips the send, so a reminder can never be sent
-- twice. Stage buckets mirror the FSM: 'start' (never replied), 'name',
-- 'email', 'email_code', 'details' (aggregated Tier-1 form), and 'kyb'
-- (merchant_register_* chain). Outcomes: queued (claimed, not yet delivered),
-- sent, failed (provider error), pending_template (out of the WhatsApp
-- 24-hour service window with no approved template configured — suppressed,
-- never a raw text).
CREATE TABLE IF NOT EXISTS onboarding_reminders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    stage text NOT NULL,
    attempt_no int NOT NULL,
    stage_state text NOT NULL DEFAULT '',
    channel text NOT NULL,
    recipient_sealed text,
    template text NOT NULL DEFAULT '',
    locale text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT 'queued'
        CHECK (outcome IN ('queued','sent','failed','pending_template')),
    sent_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, stage, attempt_no)
);

CREATE INDEX IF NOT EXISTS onboarding_reminders_user_idx
    ON onboarding_reminders (user_id, sent_at);
