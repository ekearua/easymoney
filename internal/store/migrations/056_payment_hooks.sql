-- C51: post-success payment hooks (invoice allocation, thrift contribution,
-- service stock, event ticket sales, collection splits, receipt scan) run
-- after the payment transition commits. A hook that fails must be retried
-- rather than silently dropped: each payment's hooks are tracked here and
-- drained by a worker with exponential backoff, so a succeeded payment can
-- never be left with its purpose un-applied.
CREATE TABLE payment_hooks (
    payment_id    uuid        NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    hook          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'pending', -- pending | done | failed
    attempts      integer     NOT NULL DEFAULT 0,
    last_error    text,
    next_retry_at timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (payment_id, hook)
);

CREATE INDEX payment_hooks_due_idx ON payment_hooks (status, next_retry_at);

-- Idempotency guards for inventory-style hooks: a retried hook must not
-- decrement service stock or increment ticket sales a second time.
ALTER TABLE service_purchases ADD COLUMN applied boolean NOT NULL DEFAULT false;
ALTER TABLE event_ticket_purchases ADD COLUMN applied boolean NOT NULL DEFAULT false;