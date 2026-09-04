-- W1: per-entity wallet accounts. A wallet is the customer/business-visible
-- balance of a dedicated double-entry ledger account (2301_user_wallet:<owner>
-- for individuals, 3101_business_wallet:<owner> for merchants). The wallet row
-- carries metadata and status; every movement is a balanced posting on the
-- append-only ledger (030), so a wallet can never diverge from the journal.
--
-- Wallets are deliberately NOT foreign-keyed to users/merchants: retention
-- and DSR purge those rows, and a wallet must outlive nothing — the account
-- code (not a FK) binds it to its owner.
CREATE TABLE IF NOT EXISTS wallet_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_type text NOT NULL CHECK (owner_type IN ('user','business')),
    owner_id uuid NOT NULL,
    account_code text NOT NULL UNIQUE,
    account_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','frozen')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_type, owner_id)
);

CREATE INDEX IF NOT EXISTS wallet_accounts_owner_idx ON wallet_accounts(owner_type, owner_id);

-- Backfill. Every existing user gets an individual wallet and every existing
-- merchant a business wallet, both grandfathered active (mirroring the KYB
-- B0 grandfathering): they are operational from the moment of the upgrade.
INSERT INTO wallet_accounts(owner_type, owner_id, account_code, account_name, status)
SELECT 'user', u.id, '2301_user_wallet:' || u.id::text,
       COALESCE(NULLIF(u.display_name,''), 'Individual wallet'), 'active'
FROM users u
ON CONFLICT (owner_type, owner_id) DO NOTHING;

INSERT INTO wallet_accounts(owner_type, owner_id, account_code, account_name, status)
SELECT 'business', m.id, '3101_business_wallet:' || m.id::text,
       COALESCE(NULLIF(m.name,''), 'Business wallet'), 'active'
FROM merchants m
ON CONFLICT (owner_type, owner_id) DO NOTHING;