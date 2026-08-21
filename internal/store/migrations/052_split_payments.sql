-- Phase 2: split payments and individual payouts.
-- payment_splits records every ledger leg of a collection (Xego fee,
-- merchant receivable, etc.). user_payout_destinations stores bank
-- account details for sending money to individual WhatsApp contacts.

CREATE TABLE IF NOT EXISTS payment_splits (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id uuid NOT NULL REFERENCES payments(id),
    split_type text NOT NULL CHECK (split_type IN ('xego_fee','merchant_receivable','individual_payout')),
    account text NOT NULL,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency text NOT NULL DEFAULT 'NGN',
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS payment_splits_payment_id_idx ON payment_splits(payment_id);

CREATE TABLE IF NOT EXISTS user_payout_destinations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    bank_code text NOT NULL,
    bank_name text NOT NULL DEFAULT '',
    account_number text NOT NULL,
    account_name text NOT NULL DEFAULT '',
    is_default boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(user_id, bank_code, account_number)
);
