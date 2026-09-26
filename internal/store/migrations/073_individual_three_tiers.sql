-- CBN three-tier framework (individuals): collapse the L0-L4 ladder onto the
-- three CBN rungs. L4 is retired. No profile may sit above CBN Tier 1 (L1)
-- without BOTH a verified BVN and NIN (both are now collected at the L2 gate),
-- so legacy L2/L3/L4 holders are demoted to the new L1. The obsolete evidence
-- keys (identity_on_file, nin_or_bvn_verified) are pruned from stored evidence.
--
-- The individual Tier-1 self-declared data set gains gender (collected with
-- name/dob/address at onboarding). customer_verifications.verification_type is
-- widened to the new CBN evidence keys, and kyc_tier_limits loses its L4 rows
-- and is aligned to the CBN ceilings in internal/kyc.

-- ---------------------------------------------------------------------------
-- 1. Individual profiles: gender for CBN Tier-1 data
-- ---------------------------------------------------------------------------
ALTER TABLE individual_profiles
    ADD COLUMN IF NOT EXISTS gender text NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- 2. Data-migrate the ladder before narrowing the CHECK constraints
-- ---------------------------------------------------------------------------
-- Legacy holders of the retired higher rungs fall back to the new L1, because
-- under the CBN framework no one may go past Tier 1 without both a verified
-- BVN and a verified NIN, and neither exists in legacy evidence.
UPDATE kyc_profiles
SET tier = 'L1',
    tier_updated_at = now(),
    updated_at = now()
WHERE tier IN ('L2','L3','L4');

-- Prune evidence keys that no longer exist on the CBN ladder.
UPDATE kyc_profiles
SET evidence = evidence - 'identity_on_file' - 'nin_or_bvn_verified',
    updated_at = now();

-- A review request for the retired L4 re-targets the EDD tier (L3).
UPDATE manual_review_cases
SET requested_tier = 'L3'
WHERE requested_tier = 'L4';

-- kyc_tier_limits: retire the L4 rows and realign the tier defaults with the
-- CBN ceilings (Tier 1: N50k/N200k/N500k; Tier 2: N200k/N500k/N3m; Tier 3
-- uncapped -> the platform maximum). Do this before the tier CHECK narrows.
DELETE FROM kyc_tier_limits
WHERE account_type = 'individual' AND tier = 'L4';

UPDATE kyc_tier_limits SET
    single_limit_kobo = 5000000,
    daily_limit_kobo = 20000000,
    monthly_limit_kobo = 50000000,
    updated_at = now()
WHERE account_type = 'individual' AND tier = 'L1';

UPDATE kyc_tier_limits SET
    single_limit_kobo = 20000000,
    daily_limit_kobo = 50000000,
    monthly_limit_kobo = 300000000,
    updated_at = now()
WHERE account_type = 'individual' AND tier = 'L2';

UPDATE kyc_tier_limits SET
    single_limit_kobo = 100000000,
    daily_limit_kobo = 100000000,
    monthly_limit_kobo = 1000000000,
    updated_at = now()
WHERE account_type = 'individual' AND tier = 'L3';

-- ---------------------------------------------------------------------------
-- 3. Narrow the CHECK constraints to the retiring ladder
-- ---------------------------------------------------------------------------
ALTER TABLE kyc_profiles
    DROP CONSTRAINT IF EXISTS kyc_profiles_tier_check;
ALTER TABLE kyc_profiles
    ADD CONSTRAINT kyc_profiles_tier_check
        CHECK (tier IN ('L0','L1','L2','L3'));

ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_requested_tier_check;
ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_requested_tier_check
        CHECK (requested_tier IS NULL OR requested_tier IN ('L0','L1','L2','L3'));

ALTER TABLE customer_verifications
    DROP CONSTRAINT IF EXISTS customer_verifications_verification_type_check;
ALTER TABLE customer_verifications
    ADD CONSTRAINT customer_verifications_verification_type_check
        CHECK (verification_type IN (
            'channel_confirmed','email_confirmed','tier1_basic_details',
            'government_photo_id','bvn_verified','nin_verified',
            'address_proof','edd_completed'));

-- The kyc_tier_limits tier check is a table-level CHECK in 053; PostgreSQL
-- auto-named it (kyc_tier_limits_check), so drop it by definition rather than
-- by a guessed name. Any surviving check that still mentions 'L4' would keep
-- L4 insertable.
DO $$
DECLARE c record;
BEGIN
    FOR c IN
        SELECT conname
        FROM pg_constraint
        WHERE conrelid = 'kyc_tier_limits'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%''L4''%'
    LOOP
        EXECUTE format('ALTER TABLE kyc_tier_limits DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;

ALTER TABLE kyc_tier_limits
    ADD CONSTRAINT kyc_tier_limits_tier_check
        CHECK (
            (account_type = 'individual' AND tier IN ('L0','L1','L2','L3'))
            OR (account_type = 'business' AND tier IN ('B0','B1','B2','B3'))
        );