-- Durable references let retries reconcile provider wallet movements instead
-- of issuing a second debit or reversal after an ambiguous network outcome.
CREATE TABLE IF NOT EXISTS interswitch_wallet_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    wallet_id text NOT NULL,
    operation_type text NOT NULL CHECK (operation_type IN ('debit','reversal')),
    reference text NOT NULL UNIQUE,
    original_reference text NOT NULL DEFAULT '',
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency_code text NOT NULL DEFAULT '566' CHECK (currency_code = '566'),
    status text NOT NULL DEFAULT 'dispatching'
        CHECK (status IN ('dispatching','succeeded','failed','unknown')),
    response_code text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_interswitch_wallet_operations_unresolved
    ON interswitch_wallet_operations(updated_at, created_at)
    WHERE status IN ('dispatching','unknown');
