-- Bulk payments: location-aware disbursements from a merchant's business
-- wallet to recipients currently within a radius of a saved center. A batch is
-- a two-stage one-off job: stage 1 asks each recipient to share their location
-- over WhatsApp/Telegram, stage 2 (after a short TTL) pays everyone who
-- responded and is eligible (Tier-2 KYC, within radius, acceptable accuracy).
-- Each successful item is its own balanced ledger pair (debit business wallet /
-- credit recipient wallet), so the merchant is only debited for payments that
-- actually land.

-- A merchant-owned point used as the origin for distance checks. Merchants may
-- keep several (branches, pick-up points); recipients belong to exactly one.
CREATE TABLE IF NOT EXISTS merchant_centers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    radius_m int NOT NULL DEFAULT 50 CHECK (radius_m > 0),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS merchant_centers_merchant_idx
    ON merchant_centers (merchant_id, created_at DESC);

-- A recipient the merchant uploaded (manual entry, CSV, or XLSX) and attached
-- to one center. phone_e164 is the match key to an existing Xego account;
-- user_id is resolved lazily so a not-yet-registered number can still be
-- targeted (stage 1 invites them to finish onboarding).
CREATE TABLE IF NOT EXISTS center_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    center_id uuid NOT NULL REFERENCES merchant_centers(id) ON DELETE CASCADE,
    phone_e164 text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    source text NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, phone_e164)
);
CREATE INDEX IF NOT EXISTS center_users_center_idx ON center_users (center_id);
CREATE INDEX IF NOT EXISTS center_users_phone_idx ON center_users (phone_e164);

-- One manual trigger. Counts are maintained incrementally so the merchant's
-- status page and audit report never need a full item scan.
CREATE TABLE IF NOT EXISTS bulk_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_no text NOT NULL UNIQUE,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    center_id uuid NOT NULL REFERENCES merchant_centers(id) ON DELETE RESTRICT,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency text NOT NULL DEFAULT 'NGN',
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','stage1','stage2','completed','partial','failed','cancelled')),
    ttl_seconds int NOT NULL DEFAULT 300 CHECK (ttl_seconds > 0),
    total_targets int NOT NULL DEFAULT 0,
    requested_count int NOT NULL DEFAULT 0,
    responded_count int NOT NULL DEFAULT 0,
    eligible_count int NOT NULL DEFAULT 0,
    paid_count int NOT NULL DEFAULT 0,
    failed_count int NOT NULL DEFAULT 0,
    expired_count int NOT NULL DEFAULT 0,
    total_debited_kobo bigint NOT NULL DEFAULT 0,
    compliance_flag boolean NOT NULL DEFAULT false,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    stage1_at timestamptz,
    stage2_due_at timestamptz,
    completed_at timestamptz,
    failure_reason text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS bulk_batches_merchant_idx
    ON bulk_batches (merchant_id, created_at DESC);

-- One row per targeted recipient in a batch. The captured location and the
-- computed distance are retained for the manual AML review trail.
CREATE TABLE IF NOT EXISTS bulk_batch_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id uuid NOT NULL REFERENCES bulk_batches(id) ON DELETE CASCADE,
    center_user_id uuid REFERENCES center_users(id) ON DELETE SET NULL,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    phone_e164 text NOT NULL,
    channel text NOT NULL DEFAULT 'whatsapp',
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','location_requested','location_received',
                          'out_of_range','poor_accuracy','kyc_ineligible',
                          'not_registered','expired','paid','failed')),
    latitude double precision,
    longitude double precision,
    accuracy_m double precision,
    distance_m double precision,
    location_captured_at timestamptz,
    request_sent_at timestamptz,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    credit_ref text NOT NULL DEFAULT '',
    failure_code text NOT NULL DEFAULT '',
    failure_reason text NOT NULL DEFAULT '',
    attempts int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, phone_e164)
);
CREATE INDEX IF NOT EXISTS bulk_batch_items_batch_idx
    ON bulk_batch_items (batch_id, status);
-- A location reply carries no batch id; it is correlated to the one open
-- request for that phone number.
CREATE INDEX IF NOT EXISTS bulk_batch_items_open_phone_idx
    ON bulk_batch_items (phone_e164) WHERE status = 'location_requested';

-- Delayed one-off job queue (no perpetually running job): stage 1 is enqueued
-- when the batch is created, stage 2 is enqueued by stage 1 with run_after set
-- to the TTL boundary. Claimed with FOR UPDATE SKIP LOCKED by the ticker.
CREATE TABLE IF NOT EXISTS bulk_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('bulk_stage1','bulk_stage2')),
    batch_id uuid NOT NULL REFERENCES bulk_batches(id) ON DELETE CASCADE,
    run_after timestamptz NOT NULL DEFAULT now(),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued','running','done','failed')),
    attempts int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 5,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, batch_id)
);
CREATE INDEX IF NOT EXISTS bulk_jobs_due_idx
    ON bulk_jobs (run_after) WHERE status = 'queued';