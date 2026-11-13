-- Track the Interswitch-issued wallet returned during secure browser onboarding.
-- The PIN is deliberately never stored; only provider identifiers and the
-- linked receiving-account metadata are retained.
ALTER TABLE standing_accounts
    ADD COLUMN IF NOT EXISTS bank_code text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_wallet_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_wallet_record_id bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS account_payable_code text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_wallet_status text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS wallet_creation_status text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provisioning_status text NOT NULL DEFAULT 'not_started'
        CHECK (provisioning_status IN ('not_started','provisioning','provisioned','unknown')),
    ADD COLUMN IF NOT EXISTS provisioning_error text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_standing_accounts_provider_wallet_id
    ON standing_accounts(provider_wallet_id)
    WHERE provider_wallet_id <> '';
