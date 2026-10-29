-- Issuer-independent physical card-request workflow. This stores request metadata
-- and opaque issuer references only: no PAN, PIN, CVV, or identity-document number.
CREATE TABLE IF NOT EXISTS card_requests (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel               text NOT NULL DEFAULT 'whatsapp',
    region_id             uuid NOT NULL,
    cardholder_name_enc   text NOT NULL,
    status                text NOT NULL DEFAULT 'requested'
                          CHECK (status IN ('requested','approved','rejected','ready','collected')),
    review_note           text NOT NULL DEFAULT '',
    reviewer_email        text NOT NULL DEFAULT '',
    reviewed_at           timestamptz,
    issuer_reference      text NOT NULL DEFAULT '',
    agent_merchant_id     uuid,
    ready_at              timestamptz,
    collection_id_type    text NOT NULL DEFAULT '',
    collected_by          uuid,
    collected_at          timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS card_requests_user_idx ON card_requests(user_id, created_at DESC);
-- At most one open request per customer, enforced by the database rather than a
-- read-then-insert check that two concurrent confirms could both pass.
CREATE UNIQUE INDEX IF NOT EXISTS card_requests_open_user_idx
    ON card_requests(user_id) WHERE status IN ('requested','approved','ready');
CREATE INDEX IF NOT EXISTS card_requests_status_idx ON card_requests(status, created_at);
CREATE INDEX IF NOT EXISTS card_requests_agent_idx ON card_requests(agent_merchant_id, status, created_at);

-- Readiness is explicitly default-closed; all gates must be attested before a
-- compliance operator can record externally ordered stock as ready for pickup.
CREATE TABLE IF NOT EXISTS card_issuer_readiness (
    gate_key       text PRIMARY KEY CHECK (gate_key IN
                   ('issuer_contract','pci_scope','funding_settlement','issuer_api','fulfillment')),
    ready          boolean NOT NULL DEFAULT false,
    evidence_ref   text NOT NULL DEFAULT '',
    note           text NOT NULL DEFAULT '',
    attested_by    text NOT NULL DEFAULT '',
    attested_at    timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Pickup codes are random, salted digests; plaintext is only ever sent through
-- the encrypted message outbox. A successful collection retires the code.
CREATE TABLE IF NOT EXISTS card_request_pickups (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    card_request_id    uuid NOT NULL REFERENCES card_requests(id) ON DELETE CASCADE,
    code_hash           text NOT NULL,
    code_salt           text NOT NULL,
    status              text NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','redeemed','expired','locked')),
    attempts            integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts        integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    expires_at          timestamptz NOT NULL,
    redeemed_at         timestamptz,
    redeemed_by         uuid,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS card_request_pickups_pending_idx
    ON card_request_pickups(card_request_id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS card_request_pickups_agent_code_idx
    ON card_request_pickups(status, expires_at);
