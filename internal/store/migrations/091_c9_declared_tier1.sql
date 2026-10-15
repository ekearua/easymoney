-- C9 (docs/payments/Remediation_Backlog.md): retire the Tier-1 "approved_simulated"
-- residue.
--
-- individual_profiles.kyc_status was written as the constant
-- 'approved_simulated' by every Tier-1 upsert and read by nothing outside
-- the store: the real state machine is kyc_profiles.tier (migration 026's
-- ladder), and a column that only ever holds one value expresses neither.
-- The same stamp also went into customer_verifications as provider
-- 'simulated' with result.status 'approved_simulated' — evidence that reads
-- like a simulated *approval* of the customer, when what actually happened
-- is that the customer declared Tier-1 details and we recorded them.
--
-- So the evidence rows are rewritten to the truthful declaration shape
-- first (customer_verifications is DSR-exported, so customers see these
-- rows in their export), then the column is dropped. Nothing else changes:
-- no ladder, screening or risk state reads kyc_status, and migration 026's
-- historical queries against it have already run on every existing
-- database before this file is applied.

UPDATE customer_verifications
SET provider = 'customer_declaration',
    result = '{"status":"recorded"}'::jsonb
WHERE provider = 'simulated'
  AND result ->> 'status' = 'approved_simulated';

ALTER TABLE individual_profiles DROP COLUMN kyc_status;
