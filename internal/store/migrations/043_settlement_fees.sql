-- S3: settlement fees. A percentage fee is levied on each batch at cut time
-- and recorded as a separate ledger entry (dr 3200 / cr 5200) that reduces
-- the merchant's scheduled payable. The payout amount is always total minus fee.

ALTER TABLE settlement_batches ADD COLUMN IF NOT EXISTS fee_kobo bigint NOT NULL DEFAULT 0;
ALTER TABLE settlement_batches ADD COLUMN IF NOT EXISTS fee_bps integer NOT NULL DEFAULT 0;
