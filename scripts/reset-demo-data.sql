-- scripts/reset-demo-data.sql
--
-- One-shot demo-data reset for the VPS database. Deletes every seeded
-- merchant and user, keeping ONLY:
--   * the survivor merchant (default: Tricoxsys, matched by slug OR name)
--   * the internal Xego system merchants used as payment rails
--     (xego-wallet-topup, xego-individual-pay, xego-thrift-contributions,
--      xego-data)
--   * admin console accounts (admin_users) and their sessions
--   * the `users` rows that own a kept merchant (merchant_owners)
-- Then re-clears the survivor merchant's KYB to the top business tier (B3)
-- with a clear screening decision, an approved review, the full evidence set,
-- and an active business wallet, so the merchant can take demo payments
-- immediately at the platform ceiling.
--
-- The demo seed user '+2348000000001 (Demo Merchant Admin)' is deliberately
-- removed as well: it owns every seeded merchant, and the merchant_owners
-- links to the deleted merchants go with them.
--
-- The ledger, split legs, refunds, disputes, settlements, invoices,
-- checkouts, webhook deliveries and wallet balances are reset together with
-- their owners, so the survivor starts from a clean financial slate.
--
-- SAFETY: the whole script runs inside ONE transaction. If any statement
-- fails, everything (including the temporary ledger-trigger drop) is rolled
-- back. Always take a backup before running:
--
--   # on the VPS, from the deploy directory:
--   ./backup.sh
--
--   # then reset (adjust the psql target to your DATABASE_URL):
--   psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f scripts/reset-demo-data.sql
--
-- The survivor merchant is identified by slug first, then by name. If it is
-- not found the script aborts and nothing is changed. To target a different
-- merchant, override the two variables below.

\set ON_ERROR_STOP on

\set survivor_slug 'tricoxsys'
\set survivor_name 'Tricoxsys'

BEGIN;

-- ---------------------------------------------------------------------------
-- 0. Resolve the kept sets (survivor + system merchants, their owners).
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE keep_merchants (id uuid PRIMARY KEY);
INSERT INTO keep_merchants (id)
SELECT m.id FROM merchants m
WHERE m.slug = :'survivor_slug'
   OR lower(m.name) = lower(:'survivor_name')
ON CONFLICT (id) DO NOTHING;

INSERT INTO keep_merchants (id)
SELECT id FROM merchants
WHERE slug IN ('xego-wallet-topup','xego-individual-pay','xego-thrift-contributions','xego-data')
ON CONFLICT (id) DO NOTHING;

CREATE TEMP TABLE keep_users (id uuid PRIMARY KEY);
INSERT INTO keep_users (id)
SELECT DISTINCT mo.user_id
FROM merchant_owners mo
JOIN keep_merchants km ON km.id = mo.merchant_id;

-- The demo seed admin is removed even though it owns every merchant.
DELETE FROM keep_users ku
USING users u
WHERE u.id = ku.id AND u.whatsapp_number = '+2348000000001';

-- psql interpolates :'var' only OUTSIDE dollar-quoted bodies, so the survivor
-- identifiers are materialised here and the DO block below reads them back.
CREATE TEMP TABLE survivor_ident (slug text, name text);
INSERT INTO survivor_ident (slug, name) VALUES (:'survivor_slug', :'survivor_name');

DO $$
DECLARE
    v_survivor_slug text;
    v_survivor_name text;
BEGIN
    SELECT slug, name INTO v_survivor_slug, v_survivor_name FROM survivor_ident LIMIT 1;
    IF NOT EXISTS (SELECT 1 FROM keep_merchants) THEN
        RAISE EXCEPTION 'survivor merchant %/% not found; aborting without changes',
            v_survivor_slug, v_survivor_name;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM keep_users) THEN
        RAISE WARNING 'no non-admin user is retained: the survivor merchant has no ownerless chat account after the reset';
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 1. Financial children with NO ON DELETE CASCADE (block payment deletion).
-- ---------------------------------------------------------------------------
DELETE FROM payment_splits;
DELETE FROM refunds;
DELETE FROM disputes;
DELETE FROM settlement_lines;
DELETE FROM payouts;
DELETE FROM settlement_batches;
DELETE FROM merchant_settlement_accounts;

-- ---------------------------------------------------------------------------
-- 2. Payments. Deleting merchants is blocked (merchants referenced by
--    payments without a cascade), so all payment rows go first.
-- ---------------------------------------------------------------------------
DELETE FROM payments;

-- ---------------------------------------------------------------------------
-- 3. Merchant-scoped rows that reference merchants WITHOUT a cascade.
-- ---------------------------------------------------------------------------
DELETE FROM invoices WHERE merchant_id NOT IN (SELECT id FROM keep_merchants);
DELETE FROM checkouts WHERE payee_merchant_id NOT IN (SELECT id FROM keep_merchants);
DELETE FROM registered_services
WHERE merchant_id IS NOT NULL
  AND merchant_id NOT IN (SELECT id FROM keep_merchants);

-- ---------------------------------------------------------------------------
-- 4. Append-only ledger. ledger_entries.merchant_id references merchants and
--    the table rejects UPDATE/DELETE via a trigger, so the trigger is dropped,
--    the journal cleared, and recreated inside the transaction.
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS ledger_entries_no_mutation ON ledger_entries;
DELETE FROM ledger_entries;
CREATE TRIGGER ledger_entries_no_mutation
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_entry_mutation();

-- ---------------------------------------------------------------------------
-- 5. Owner-scoped rows without a users FK.
-- ---------------------------------------------------------------------------
DELETE FROM allowance_usage;
DELETE FROM user_payout_destinations WHERE user_id NOT IN (SELECT id FROM keep_users);
DELETE FROM wallet_accounts
WHERE (owner_type = 'user'     AND owner_id NOT IN (SELECT id FROM keep_users))
   OR (owner_type = 'business' AND owner_id NOT IN (SELECT id FROM keep_merchants));

-- Merchant-owned TOTP secrets (admin scope and the global admin secret are kept).
DELETE FROM totp_secrets
WHERE scope = 'merchant'
  AND subject_id NOT IN (SELECT id FROM keep_merchants);

-- ---------------------------------------------------------------------------
-- 6. Users. Everything user-scoped cascades (sessions, KYC, thrift, data
--    orders, registrations, web flows, messages, ...).
-- ---------------------------------------------------------------------------
DELETE FROM users WHERE id NOT IN (SELECT id FROM keep_users);

-- ---------------------------------------------------------------------------
-- 7. Merchants. Cascade removes owners, KYB profiles, sessions, API keys,
--    webhooks, events, merchant services, remaining settlements, etc.
-- ---------------------------------------------------------------------------
DELETE FROM merchants WHERE id NOT IN (SELECT id FROM keep_merchants);

-- ---------------------------------------------------------------------------
-- 8. Re-assert the survivor.
-- ---------------------------------------------------------------------------
UPDATE merchants
SET active = true,
    sort_order = 10,
    updated_at = now()
WHERE id IN (SELECT id FROM keep_merchants)
  AND (slug = :'survivor_slug' OR lower(name) = lower(:'survivor_name'));

-- Business KYB fully cleared: B3 ceiling, all three evidence items, clear
-- screening, approved review, next rescreen in 90 days.
INSERT INTO business_kyb_profiles (
    merchant_id, tier, evidence, last_screening_decision, last_screen_at,
    rescreen_due, review_status, reviewed_at, updated_at
)
SELECT m.id, 'B3',
       '["business_docs_verified","business_bank_verified","business_edd_completed"]'::jsonb,
       'clear', now(), now() + interval '90 days', 'approved', now(), now()
FROM merchants m
WHERE m.slug = :'survivor_slug' OR lower(m.name) = lower(:'survivor_name')
ON CONFLICT (merchant_id) DO UPDATE SET
    tier = EXCLUDED.tier,
    evidence = EXCLUDED.evidence,
    last_screening_decision = EXCLUDED.last_screening_decision,
    last_screen_at = now(),
    rescreen_due = now() + interval '90 days',
    review_status = EXCLUDED.review_status,
    reviewed_at = now(),
    updated_at = now();

-- Re-create the business wallet as active (mirrors W1/AdvanceKYBTier).
INSERT INTO wallet_accounts (owner_type, owner_id, account_code, account_name, status)
SELECT 'business', m.id, '3101_business_wallet:' || m.id::text,
       COALESCE(NULLIF(m.name, ''), 'Business wallet'), 'active'
FROM merchants m
WHERE m.slug = :'survivor_slug' OR lower(m.name) = lower(:'survivor_name')
ON CONFLICT (owner_type, owner_id) DO UPDATE SET
    status = 'active',
    updated_at = now();

-- Merchant receipt-scanning service row (mirrors migration 014).
INSERT INTO registered_services (name, service_type, merchant_id, accepted_receipt_types, token_ttl_seconds, active)
SELECT m.name, 'merchant', m.id, 'merchant_payment,invoice', 86400, true
FROM merchants m
WHERE m.slug = :'survivor_slug' OR lower(m.name) = lower(:'survivor_name')
ON CONFLICT (service_type, merchant_id) DO UPDATE SET
    name = EXCLUDED.name,
    accepted_receipt_types = EXCLUDED.accepted_receipt_types,
    active = true,
    updated_at = now();

-- System merchants keep their B0 baseline (mirrors the Seed grandfathering).
INSERT INTO business_kyb_profiles (merchant_id, tier, evidence, last_screening_decision, last_screen_at, rescreen_due, review_status)
SELECT m.id, 'B0', '["registration_approved"]'::jsonb, 'clear', now(),
       now() + interval '90 days', 'approved'
FROM merchants m
WHERE m.slug IN ('xego-wallet-topup','xego-individual-pay','xego-thrift-contributions','xego-data')
ON CONFLICT (merchant_id) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 9. Summary.
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    v_merchants bigint;
    v_users     bigint;
    v_payments  bigint;
BEGIN
    SELECT count(*) INTO v_merchants FROM merchants;
    SELECT count(*) INTO v_users     FROM users;
    SELECT count(*) INTO v_payments  FROM payments;
    RAISE NOTICE 'reset complete: %, merchants, % users, % payments remaining', v_merchants, v_users, v_payments;
END
$$;

COMMIT;