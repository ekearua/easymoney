-- 050: Audit trail for AI intent classifications and assistant interactions.
-- Stores every AI call for debugging, compliance review, and tuning.

CREATE TABLE IF NOT EXISTS ai_log (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel text NOT NULL,
    request text NOT NULL,
    intent text NOT NULL DEFAULT '',
    entities jsonb NOT NULL DEFAULT '{}',
    response text NOT NULL DEFAULT '',
    confidence double precision NOT NULL DEFAULT 0,
    provider text NOT NULL DEFAULT '',
    latency_ms bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ai_log_user_id ON ai_log (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ai_log_created_at ON ai_log (created_at DESC);
