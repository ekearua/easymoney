-- Self-service KYB upgrade requests. A merchant owner can record which
-- evidence they hold for the next tier on the ladder; the admin console
-- reviews the request and either approves the advancement (moving the tier)
-- or rejects/clears it. The request is advisory evidence only - the tier still
-- only moves through AdvanceKYBTier with its adjacency, evidence and screening
-- gates.

ALTER TABLE business_kyb_profiles
    ADD COLUMN IF NOT EXISTS advancement_request jsonb;

ALTER TABLE business_kyb_profiles
    ADD COLUMN IF NOT EXISTS advancement_requested_at timestamptz;
