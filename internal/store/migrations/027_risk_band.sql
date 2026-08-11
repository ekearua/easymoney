-- C13: ML/FT customer risk scoring. Adds the aggregate risk band computed by
-- internal/kyc.ScoreRisk to the KYC profile so CDD vs EDD decisions are
-- persisted alongside the tier ladder. risk_score and risk_band are recomputed
-- by the store's RecomputeRiskScore whenever risk events are recorded.
ALTER TABLE kyc_profiles
    ADD COLUMN IF NOT EXISTS risk_score numeric(5,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS risk_band text NOT NULL DEFAULT 'low',
    ADD COLUMN IF NOT EXISTS risk_updated_at timestamptz;

ALTER TABLE kyc_profiles
    DROP CONSTRAINT IF EXISTS kyc_profiles_risk_band_check;
ALTER TABLE kyc_profiles
    ADD CONSTRAINT kyc_profiles_risk_band_check
    CHECK (risk_band IN ('low','medium','high'));

CREATE INDEX IF NOT EXISTS kyc_profiles_risk_idx ON kyc_profiles(risk_band, risk_updated_at);
