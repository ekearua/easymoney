-- KYB self-service evidence, made verifiable. Three additions to the
-- corporate documents table (074):
--
--   * the document itself, as extracted text: the web flow OCRs an uploaded
--     CAC certificate / TIN certificate / utility bill and keeps only the
--     reading, never the raw file (the same "no raw media at rest" rule the
--     media uploader follows);
--   * the captured current location, so an operating address can be compared
--     against where the merchant actually is rather than taken on trust;
--   * the reviewer's per-document disposition and the third-party check
--     results that inform it.
--
-- The point of `reviews` is that "verified" always names a person and a time.
-- A document on file is a claim; only a row here makes it evidence.

ALTER TABLE business_kyb_documents
    ADD COLUMN IF NOT EXISTS cac_document         text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tin_document         text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS address_document     text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS location_lat         double precision,
    ADD COLUMN IF NOT EXISTS location_lng         double precision,
    ADD COLUMN IF NOT EXISTS location_accuracy_m  double precision,
    ADD COLUMN IF NOT EXISTS location_captured_at timestamptz,
    ADD COLUMN IF NOT EXISTS location_status      text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS location_provider    text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reviews              jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS verifications        jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN business_kyb_documents.reviews IS
    'evidence key -> {status, reviewer, note, reviewed_at}; a key is verified only when a reviewer disposition is recorded';
COMMENT ON COLUMN business_kyb_documents.verifications IS
    'evidence key -> registry/geocoding proposal {status, provider, provider_ref, match_name, message, checked_at}; advisory only, never advances a tier';
COMMENT ON COLUMN business_kyb_documents.location_status IS
    'last location-verification outcome: matched | review | no_location (empty = not checked)';
