-- Standing (reusable) virtual bank accounts for wallet credit.
--
-- A payer-facing account number is reserved deterministically at Level 1 as a
-- local 9029-* number so a customer can always be paid by bank transfer even
-- when no Interswitch sandbox credentials are present. When credentials are
-- configured, the reserved slot is upgraded to a provider standing account
-- number (standVA) and the local number is retired.
CREATE TABLE IF NOT EXISTS standing_accounts (
    user_id            uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    account_number     text NOT NULL,
    account_name       text NOT NULL DEFAULT '',
    bank_name          text NOT NULL DEFAULT '',
    provider           text NOT NULL DEFAULT '',
    provider_reference text NOT NULL DEFAULT '',
    reserved_at        timestamptz NOT NULL DEFAULT now(),
    provisioned_at     timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_standing_accounts_number
    ON standing_accounts(account_number);

-- Idempotent record of a bank-transfer credit into a standing account. The
-- app ingests the provider notification once and applies the wallet credit.
CREATE TABLE IF NOT EXISTS standing_credits (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_number     text NOT NULL,
    provider_reference text NOT NULL,
    amount_kobo        bigint NOT NULL CHECK (amount_kobo > 0),
    status             text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','skipped')),
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_standing_credits_reference
    ON standing_credits(provider_reference);

CREATE INDEX IF NOT EXISTS idx_standing_credits_user_status
    ON standing_credits(user_id, status);