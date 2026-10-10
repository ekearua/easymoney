-- Spec gap G9 (CBN_AML_COMPLIANCE_SPEC.md): adverse-media + domestic-list
-- screening. Domestic lists (NSC/NIGSAC) are already owned through the
-- internal watchlist (G3); this table records the adverse-media lane from the
-- distinct AdverseMediaScreener provider port. The decision vocabulary is the
-- shared onboarding one (clear/possible/strong/blocked), and a non-clear
-- decision opens an 'adverse_media' review case so the KYC queue dispositions
-- it. Rows are keyed by user + screened_at (screening is periodic, not keyed to
-- one movement), so re-screens append a new row rather than overwriting.

CREATE TABLE IF NOT EXISTS adverse_media_screenings (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('clear','possible','strong','blocked')),
    matched_hits JSONB NOT NULL DEFAULT '[]'::jsonb,
    matched_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    legal_name TEXT NOT NULL DEFAULT '',
    list_version TEXT NOT NULL DEFAULT '',
    provider_ref TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    screened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    rescreen_due TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS adverse_media_screenings_user_idx
    ON adverse_media_screenings(user_id, screened_at DESC);

COMMENT ON TABLE adverse_media_screenings IS
    'G9 adverse-media screening results from the AdverseMediaScreener port; non-clear decisions open an adverse_media manual review case.';

-- The adverse_media determination opens a review case on the shared queue.
ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_case_type_check;

ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_case_type_check
        CHECK (case_type IN ('tier_upgrade','screening','edd','document','monitoring','pep_edd','identity','fraud','adverse_media'));

CREATE INDEX IF NOT EXISTS manual_review_cases_adverse_media_open_idx
    ON manual_review_cases(user_id)
    WHERE case_type = 'adverse_media' AND status = 'pending';
