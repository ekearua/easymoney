-- Spec G11 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.7): duplicate-identity detection.
-- One person -> one customer. BVN/NIN numbers are already persisted inside
-- customer_verifications.result as JSONB, but that is not a uniqueness key: a
-- second profile presenting the same number cannot be detected. This table
-- owns the identity instead, keyed by a non-reversible SHA-256 of
-- verification_type + ':' + the digits, so overlap and uniqueness are
-- enforceable without keeping a second queryable copy of the raw number.
--
--   identity_keys  one row per (verification_type, identity_key): the user
--                  that identity belongs to. A second user's claim violates
--                  the primary key, which is how RecordCustomerVerification
--                  refuses the duplicate.
--
-- The backfill is collision-safe: where pre-existing rows already share an
-- identity, only the earliest owner is recorded, so the table can be created
-- on a database that already holds duplicates. Those older duplicates are
-- surfaced by the overlap query (IdentityKeyOwner) rather than silently
-- rejected.

CREATE TABLE IF NOT EXISTS identity_keys (
    verification_type text NOT NULL,
    identity_key text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (verification_type, identity_key)
);

CREATE INDEX IF NOT EXISTS identity_keys_user_idx ON identity_keys(user_id);

INSERT INTO identity_keys(verification_type, identity_key, user_id, first_seen_at)
SELECT DISTINCT ON (verification_type, digits)
       verification_type,
       encode(sha256(convert_to(verification_type || ':' || digits, 'UTF8')), 'hex'),
       user_id,
       verified_at
FROM (
    SELECT verification_type, user_id, verified_at,
           regexp_replace(COALESCE(result->>'id_number', ''), '[^0-9]', '', 'g') AS digits
    FROM customer_verifications
    WHERE verification_type IN ('bvn_verified','nin_verified')
) existing
WHERE digits <> ''
ORDER BY verification_type, digits, verified_at ASC, user_id
ON CONFLICT DO NOTHING;

-- The review case a collision opens. Same queue, same approve/reject flow as
-- every other case type; the index serves the one-open-case-per-user guard.
ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_case_type_check;

ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_case_type_check
        CHECK (case_type IN ('tier_upgrade','screening','edd','document','monitoring','pep_edd','identity'));

CREATE INDEX IF NOT EXISTS manual_review_cases_identity_open_idx
    ON manual_review_cases(user_id)
    WHERE case_type = 'identity' AND status = 'pending';
