-- Partner API payment metadata (P1). merchant_reference is the merchant's own
-- idempotency key for POST /api/v1/payments; the partial unique index makes a
-- replay return the existing payment instead of creating a duplicate. metadata
-- carries idempotency_key / redirect_url / merchant payload from the request.
ALTER TABLE payments ADD COLUMN merchant_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE payments ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX payments_merchant_ref_idx ON payments(merchant_id, merchant_reference) WHERE merchant_reference <> '';
