-- Bank-transfer simulation fixtures. These are real Nigerian bank names, but
-- the account details are demo-only and must not be used for live transfers.
CREATE TABLE IF NOT EXISTS bank_transfer_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bank_name text NOT NULL UNIQUE,
    account_name text NOT NULL,
    account_number text NOT NULL UNIQUE,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS bank_transfer_simulations (
    payment_id uuid PRIMARY KEY REFERENCES payments(id) ON DELETE CASCADE,
    bank_account_id uuid NOT NULL REFERENCES bank_transfer_accounts(id),
    simulated_reference text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'awaiting_transfer',
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO bank_transfer_accounts (bank_name, account_name, account_number, active) VALUES
    ('Access Bank', 'Xego Collections', '9000000001', true),
    ('Guaranty Trust Bank', 'Xego Collections', '9000000002', true),
    ('Zenith Bank', 'Xego Collections', '9000000003', true),
    ('First Bank of Nigeria', 'Xego Collections', '9000000004', true),
    ('United Bank for Africa', 'Xego Collections', '9000000005', true)
ON CONFLICT (bank_name) DO UPDATE SET
    account_name = EXCLUDED.account_name,
    account_number = EXCLUDED.account_number,
    active = EXCLUDED.active,
    updated_at = now();
