-- Recipient records: the single list managed by the records role, shaped by
-- docs/user-scripts/Records Template.xlsx (S/N, Name, Phone Number, BVN, NIN,
-- VIN, Account Number, Bank Name, Account Name, State, LGA, Ward, Polling
-- Unit/Booth, Source File).
--
-- center_users already holds one row per (merchant, phone), so it becomes the
-- records table. center_id is now optional: a record may exist before it is
-- attached to a payment location, and location verification is switchable per
-- merchant.
ALTER TABLE center_users ALTER COLUMN center_id DROP NOT NULL;

-- Identity numbers are sealed at rest with the store's data key and indexed by
-- a deterministic blind hash so duplicates can be detected without decryptable
-- lookups (a BVN is an 11-digit space, so a bare digest would be enumerable).
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS bvn_sealed text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS nin_sealed text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS vin_sealed text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS bvn_index text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS nin_index text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS vin_index text NOT NULL DEFAULT '';

ALTER TABLE center_users ADD COLUMN IF NOT EXISTS account_number text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS bank_name text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS bank_code text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS account_name text NOT NULL DEFAULT '';

ALTER TABLE center_users ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS lga text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS ward text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS polling_unit text NOT NULL DEFAULT '';
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS source_file text NOT NULL DEFAULT '';

-- Manual attestation by the records role. Only verified rows may be paid.
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS verification_status text NOT NULL DEFAULT 'unverified'
    CHECK (verification_status IN ('unverified', 'verified', 'rejected'));
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS verified_by_member uuid REFERENCES merchant_members(id) ON DELETE SET NULL;
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS verified_at timestamptz;
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS verification_note text NOT NULL DEFAULT '';

-- Maintained on each successful payment so the list and detail pages never
-- need a per-row aggregate.
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS last_paid_at timestamptz;
ALTER TABLE center_users ADD COLUMN IF NOT EXISTS last_paid_amount_kobo bigint NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS center_users_geo_idx ON center_users (merchant_id, state, lga, ward);
CREATE INDEX IF NOT EXISTS center_users_verification_idx ON center_users (merchant_id, verification_status);
CREATE UNIQUE INDEX IF NOT EXISTS center_users_bvn_idx ON center_users (merchant_id, bvn_index) WHERE bvn_index <> '';
CREATE UNIQUE INDEX IF NOT EXISTS center_users_nin_idx ON center_users (merchant_id, nin_index) WHERE nin_index <> '';
CREATE UNIQUE INDEX IF NOT EXISTS center_users_vin_idx ON center_users (merchant_id, vin_index) WHERE vin_index <> '';
