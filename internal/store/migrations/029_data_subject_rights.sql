-- C20: NDPR data-subject rights (access / erasure) and consent records.
--
-- consent_records         every grant/withdrawal of a processing purpose
--                          (processing, marketing, data_sharing) per user,
--                          with timestamps and source. NDPR 3.1 lawful basis.
-- data_subject_requests   access and erasure requests from data subjects.
--                          Erasure follows a 30-day cooling-off legal hold so
--                          MLPA record keeping and dispute handling survive.
CREATE TABLE IF NOT EXISTS consent_records (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose text NOT NULL CHECK (purpose IN ('processing','marketing','data_sharing')),
    status text NOT NULL DEFAULT 'granted' CHECK (status IN ('granted','revoked')),
    source text NOT NULL DEFAULT 'chat' CHECK (source IN ('chat','onboarding','admin')),
    version text NOT NULL DEFAULT '1.0',
    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    UNIQUE (user_id, purpose)
);

CREATE INDEX IF NOT EXISTS consent_records_user_idx ON consent_records(user_id, purpose);

CREATE TABLE IF NOT EXISTS data_subject_requests (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_type text NOT NULL CHECK (request_type IN ('access','erasure')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','in_review','completed','rejected')),
    reason text NOT NULL DEFAULT '',
    requested_by text NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    reviewer_email text,
    decision_note text,
    reviewed_at timestamptz,
    completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS data_subject_requests_status_idx
    ON data_subject_requests(status, requested_at DESC);
CREATE INDEX IF NOT EXISTS data_subject_requests_user_idx
    ON data_subject_requests(user_id, requested_at DESC);

-- Backfill consent: every existing customer has granted processing consent
-- from first use (demo default, documented in the privacy policy).
INSERT INTO consent_records (user_id, purpose, source, version, granted_at)
SELECT u.id, 'processing', 'chat', '1.0', u.created_at
FROM users u
ON CONFLICT (user_id, purpose) DO NOTHING;
