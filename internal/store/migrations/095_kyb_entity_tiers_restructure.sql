-- Restructure KYB to support entity-type aware requirements
-- Formal businesses (with CAC registration) use merchant KYB with entity-specific requirements
-- Informal businesses (no CAC registration) route to individual accounts

-- Add entity classification to business_kyb_profiles
ALTER TABLE business_kyb_profiles
    ADD COLUMN IF NOT EXISTS entity_type text NOT NULL DEFAULT 'business_name',
    ADD COLUMN IF NOT EXISTS registration_number text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS business_name text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS business_phone text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS business_email text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS date_of_incorporation date,
    ADD COLUMN IF NOT EXISTS registered_address text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS is_dnfbp boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS dnfbp_category text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tin text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS routing_path text NOT NULL DEFAULT 'business';

-- Add entity classification to merchants
ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS entity_type text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS has_cac_registration boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS routing_path text NOT NULL DEFAULT 'business';

-- Add entity classification to business_kyb_documents
ALTER TABLE business_kyb_documents
    ADD COLUMN IF NOT EXISTS entity_type text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS business_info jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN business_kyb_profiles.entity_type IS 'Entity type: sole_proprietor, business_name, partnership, limited_company_private, limited_company_public, ngo, other';
COMMENT ON COLUMN business_kyb_profiles.routing_path IS 'Routing path: business (uses merchant KYB) or individual (routes to individual account)';
COMMENT ON COLUMN merchants.has_cac_registration IS 'True if business has CAC registration - routes to business KYB path';
