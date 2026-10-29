-- Wallet cash-out (user-initiated withdrawal to an external bank account).
--
-- A wallet cash-out is a real NIP transfer to an account the customer enters
-- for that transfer. The wallet is debited atomically when the request is
-- accepted: the payout principal leaves the wallet to the operating bank
-- (1100) and the NIP fee leaves the wallet to Xego's payable (2300_xego_payable).
-- The provider transfer happens afterwards; if it ultimately fails the two
-- debits are reversed back into the wallet.
--
-- reservation_ref is the allowance reservation key AND the idempotency key:
-- one cash-out request creates exactly one row, so a replayed webhook cannot
-- debit the wallet twice.
--
-- Deliberately NO foreign keys to users (mirroring wallet_accounts 058 and
-- program_purses 100): retention and DSR purge delete users, and a historical
-- payout record must never block a purge. Integrity is enforced in the store.
CREATE TABLE IF NOT EXISTS wallet_payouts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL,
    destination_id   uuid,
    bank_code        text NOT NULL,
    bank_name        text NOT NULL DEFAULT '',
    account_number   text NOT NULL,
    account_name     text NOT NULL DEFAULT '',
    amount_kobo      bigint NOT NULL CHECK (amount_kobo > 0),
    payout_kobo      bigint NOT NULL CHECK (payout_kobo > 0),
    fee_kobo         bigint NOT NULL DEFAULT 0 CHECK (fee_kobo >= 0),
    status           text NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued','processing','completed','failed','reversed')),
    provider         text NOT NULL DEFAULT 'interswitch',
    external_ref     text NOT NULL DEFAULT '',
    attempts         integer NOT NULL DEFAULT 0,
    last_error       text NOT NULL DEFAULT '',
    reservation_ref  text NOT NULL UNIQUE,
    ledger_journal   text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz
);

CREATE INDEX IF NOT EXISTS wallet_payouts_user_idx
    ON wallet_payouts (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS wallet_payouts_status_idx
    ON wallet_payouts (status, created_at);
