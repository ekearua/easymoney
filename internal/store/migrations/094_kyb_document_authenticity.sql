-- Spec G12 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.7): document authenticity.
--
-- A corporate document was uploaded, OCR'd and human-reviewed, but never
-- checked for tampering — and no selfie was ever compared against the photo on
-- the ID. `authenticity` holds one provider verdict per evidence key
-- (genuine | tampered | inconclusive) from a DocumentAuthenticityVerifier, kept
-- beside the reviewer's disposition so the console can show both.
--
-- The verdict is load-bearing: the corporate hard gate counts a
-- reviewer-verified key only when its authenticity verdict is "genuine", so a
-- tampered or unjudged document cannot carry a merchant past B0 however it was
-- reviewed. It lives in its own column rather than the advisory
-- `verifications` map because it is a precondition, not a proposal for a human
-- to weigh.
ALTER TABLE business_kyb_documents
    ADD COLUMN IF NOT EXISTS authenticity jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN business_kyb_documents.authenticity IS
    'evidence key -> authenticity verdict {status, provider, provider_ref, confidence, face_matched, message, checked_at}; a reviewer-verified key counts as evidence only when status is genuine';
