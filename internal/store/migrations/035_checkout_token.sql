-- Hosted checkout: a non-guessable public token that points at a branded
-- /checkout/{token} confirmation page. Unlike checkout_url (the Paystack
-- gateway URL, only present after the customer confirms), checkout_token is
-- available from the moment the payment is created so chat/SMS/links can send
-- customers to the hosted page.
ALTER TABLE payments ADD COLUMN checkout_token TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX payments_checkout_token_idx ON payments(checkout_token) WHERE checkout_token <> '';

-- Backfill existing rows so every payment can already be reached by hosted
-- checkout. receipt_token is unique, so no conflicts are possible.
UPDATE payments SET checkout_token = receipt_token WHERE checkout_token = '';
