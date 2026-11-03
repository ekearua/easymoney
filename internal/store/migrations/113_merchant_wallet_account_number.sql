-- Wallet account number (9330000000 pool) for merchant wallets.
ALTER TABLE merchants ADD COLUMN IF NOT EXISTS wallet_account_number text;
CREATE UNIQUE INDEX IF NOT EXISTS uq_merchants_wallet_account_number ON merchants(wallet_account_number) WHERE wallet_account_number IS NOT NULL;