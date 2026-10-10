-- Spec G14 (CBN_AML_COMPLIANCE_SPEC.md §3.7): geography + expected-activity
-- risk inputs. The risk engine previously scored observations, tier and
-- screening without ever asking where a customer lives or how much activity
-- they expect, so two inputs a risk-based approach names could never move a
-- band. These columns capture them on the profile; the score carries a
-- jurisdiction weight and an expected-volume lift (internal/kyc.ScoreRisk),
-- and both values render on the customer record and the data-subject export.

ALTER TABLE individual_profiles
    ADD COLUMN IF NOT EXISTS country_iso text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expected_volume_band text NOT NULL DEFAULT '';

COMMENT ON COLUMN individual_profiles.country_iso IS
    'customer country of residence (ISO 3166-1 alpha-2, uppercase); a high-risk jurisdiction adds a geography weight to the risk score';
COMMENT ON COLUMN individual_profiles.expected_volume_band IS
    'declared expected activity band (low|medium|high|very_high); high/very_high call for enhanced attention in the risk score';

ALTER TABLE business_kyb_profiles
    ADD COLUMN IF NOT EXISTS country_iso text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expected_volume_band text NOT NULL DEFAULT '';

COMMENT ON COLUMN business_kyb_profiles.country_iso IS
    'merchant country of operation (ISO 3166-1 alpha-2, uppercase); a high-risk jurisdiction adds a geography weight to the risk score';
COMMENT ON COLUMN business_kyb_profiles.expected_volume_band IS
    'declared expected activity band (low|medium|high|very_high); high/very_high call for enhanced attention in the risk score';