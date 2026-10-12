-- Partial purse draws: a restricted program purse does not have to cover a
-- whole purchase. When it cannot, the purse funds as much as it holds and the
-- payer's ordinary wallet funds the remainder. purse_amount_kobo records the
-- restricted portion so the split debit, the allowance exemption, and the
-- refund reversal all agree on how much of the payment was program money.
--
-- Invariant: a payment either has no purse at all (purse_id NULL and
-- purse_amount_kobo 0), or it has a purse and a positive purse portion that
-- never exceeds the total. The portion is a subset of the total by definition,
-- so the ledger can never debit the purse for more than the payment charged.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS purse_amount_kobo bigint NOT NULL DEFAULT 0
    CHECK (purse_amount_kobo >= 0);

-- Backfill the rows created before splits existed: a pre-existing purse
-- payment was all-or-nothing, so its whole amount was the purse portion.
UPDATE payments SET purse_amount_kobo = amount_kobo
    WHERE purse_id IS NOT NULL AND purse_amount_kobo = 0;

-- NOT VALID so the migration can never fail on a stray pre-existing row; the
-- constraints still enforce every future insert/update.
ALTER TABLE payments ADD CONSTRAINT payments_purse_amount_chk
    CHECK ((purse_id IS NULL) = (purse_amount_kobo = 0)) NOT VALID;

ALTER TABLE payments ADD CONSTRAINT payments_purse_amount_le_total_chk
    CHECK (purse_amount_kobo <= amount_kobo) NOT VALID;
