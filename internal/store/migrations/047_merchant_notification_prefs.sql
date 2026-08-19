-- O10: merchant notification preferences. Lets merchants opt out of
-- specific notification types. All default to true (opted-in).
ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS notify_payment boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS notify_refund boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS notify_settlement boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS notify_approval boolean NOT NULL DEFAULT true;
