-- Partner API credentials (P1). Merchants present a single-part key in the
-- X-Xego-Key header and sign each request with the same key material. Only the
-- SHA-256 hash of the key is stored, so the plaintext is shown exactly once at
-- creation time and is unrecoverable afterwards.
CREATE TABLE IF NOT EXISTS merchant_api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name         text NOT NULL,
    key_hash     bytea NOT NULL,
    prefix       text NOT NULL,
    enabled      boolean NOT NULL DEFAULT true,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

CREATE UNIQUE INDEX merchant_api_keys_key_hash_idx ON merchant_api_keys(key_hash);
CREATE INDEX merchant_api_keys_merchant_idx ON merchant_api_keys(merchant_id);
