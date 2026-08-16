-- C16/P3: merchant dimension on the append-only journal so balances are
-- computable per merchant (Partner API GET /api/v1/balance). Merchant-scoped
-- postings (money-in and merchant-payable accruals) carry the merchant;
-- platform-level postings (thrift, data orders) stay NULL. Reversal rows
-- inherit the merchant of the entries they unwind.
ALTER TABLE ledger_entries ADD COLUMN merchant_id uuid REFERENCES merchants(id);
CREATE INDEX IF NOT EXISTS ledger_merchant_account_idx ON ledger_entries(merchant_id, account, created_at DESC);
