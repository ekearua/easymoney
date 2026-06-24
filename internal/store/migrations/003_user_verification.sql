-- Add lightweight, audit-friendly verification markers without changing the
-- existing demo identity model. WhatsApp verification means the user messaged
-- us through Meta's signed webhook; number confirmation means they explicitly
-- accepted that number during onboarding.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS whatsapp_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS number_confirmed_at timestamptz,
    ADD COLUMN IF NOT EXISTS email_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS verification_level text NOT NULL DEFAULT 'unverified';

UPDATE users
SET whatsapp_verified_at = COALESCE(whatsapp_verified_at, last_inbound_at, created_at),
    verification_level = 'whatsapp_inbound'
WHERE whatsapp_verified_at IS NULL
  AND verification_level = 'unverified';

UPDATE users
SET verification_level = CASE
    WHEN number_confirmed_at IS NOT NULL THEN 'whatsapp_confirmed'
    WHEN whatsapp_verified_at IS NOT NULL THEN 'whatsapp_inbound'
    ELSE verification_level
END;
