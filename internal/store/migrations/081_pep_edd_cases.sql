-- Spec G1 (CBN_AML_COMPLIANCE_SPEC.md §3.3a): PEP EDD with senior-management
-- approval. A politically-exposed-person determination must open an enhanced
-- due diligence case, and no tier advancement may proceed while it is open.
-- The determination rides the existing review-queue table as a new case type;
-- the senior-management approval is a distinct status beyond the ordinary
-- approve/reject so an SM decision is auditable as its own act (Reg. 2022 PEP
-- obligations; CBN PEP guidance expects documented senior-management
-- approval). The requested_tier column stays NULL for pep_edd: the gate is
-- "no advancement anywhere", not "hold one tier".

ALTER TABLE manual_review_cases
    DROP CONSTRAINT IF EXISTS manual_review_cases_case_type_check;

ALTER TABLE manual_review_cases
    ALTER COLUMN status TYPE text,
    DROP CONSTRAINT IF EXISTS manual_review_cases_status_check;

ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_case_type_check
        CHECK (case_type IN ('tier_upgrade','screening','edd','document','monitoring','pep_edd'));

ALTER TABLE manual_review_cases
    ADD CONSTRAINT manual_review_cases_status_check
        CHECK (status IN ('pending','approved','rejected','sm_approved','sm_rejected'));

-- The advancement gate is a count of open pep_edd cases per user: one index
-- serves it directly, and the queue's pending-pep_edd filter can share it.
CREATE INDEX IF NOT EXISTS manual_review_cases_pep_open_idx
    ON manual_review_cases(user_id)
    WHERE case_type = 'pep_edd' AND status = 'pending';
