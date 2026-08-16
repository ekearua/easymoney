-- General "request money" payment links for individuals: a checkout is minted
-- without binding a customer; the payer is resolved on the hosted link page and
-- a payment is created against the payee merchant at that point.
CREATE TABLE IF NOT EXISTS checkouts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payee_merchant_id uuid NOT NULL REFERENCES merchants(id),
    token text NOT NULL UNIQUE,
    reference text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    currency text NOT NULL DEFAULT 'NGN',
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','expired','cancelled','paid')),
    payment_id uuid REFERENCES payments(id) ON DELETE SET NULL,
    redirect_url text NOT NULL DEFAULT '',
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS checkouts_payee_reference_idx
    ON checkouts(payee_merchant_id, reference) WHERE reference <> '';

CREATE INDEX IF NOT EXISTS checkouts_payee_created_idx
    ON checkouts(payee_merchant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS checkouts_status_idx
    ON checkouts(status, expires_at);
