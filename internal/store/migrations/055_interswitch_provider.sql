-- C50: Interswitch Web Checkout is the sole card gateway.
-- Existing rows keep their historical labels (paystack/flutterwave). Only the
-- column default changes so that any future insert without an explicit provider
-- defaults to interswitch.
ALTER TABLE payments ALTER COLUMN provider SET DEFAULT 'interswitch';
