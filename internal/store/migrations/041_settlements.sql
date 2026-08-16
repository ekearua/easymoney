-- S1: settlement + payout plane. A settlement batch freezes a merchant's
-- accrued merchant_payable (3100) liability into a cut at a point in time;
-- the payout then moves funds from the operating bank to the merchant's
-- verified destination account. Money movement stays on the append-only
-- ledger (030): cutting posts dr 3100 / cr 3200, paying out posts
-- dr 3200 / cr 1100, and reversals restore the payable.

CREATE TABLE IF NOT EXISTS merchant_settlement_accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    bank_code       text NOT NULL,
    account_number  text NOT NULL,
    account_name    text NOT NULL,
    is_default      boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    approved_by     uuid REFERENCES admin_users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, bank_code, account_number)
);

CREATE TABLE IF NOT EXISTS settlement_batches (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_no        text NOT NULL UNIQUE,
    merchant_id     uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open','scheduled','processed','failed')),
    total_kobo      bigint NOT NULL DEFAULT 0,
    line_count      integer NOT NULL DEFAULT 0,
    cutoff_at       timestamptz NOT NULL,
    ledger_journal  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz
);

CREATE TABLE IF NOT EXISTS settlement_lines (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id        uuid NOT NULL REFERENCES settlement_batches(id) ON DELETE CASCADE,
    payment_id      uuid NOT NULL REFERENCES payments(id),
    amount_kobo     bigint NOT NULL CHECK (amount_kobo > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, payment_id)
);

CREATE TABLE IF NOT EXISTS payouts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id          uuid NOT NULL REFERENCES settlement_batches(id) ON DELETE CASCADE,
    merchant_id       uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    destination_id    uuid NOT NULL REFERENCES merchant_settlement_accounts(id),
    amount_kobo       bigint NOT NULL CHECK (amount_kobo > 0),
    status            text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','processing','completed','failed','reversed')),
    external_ref      text,
    provider          text NOT NULL DEFAULT 'simulated',
    attempts          integer NOT NULL DEFAULT 0,
    last_error        text NOT NULL DEFAULT '',
    ledger_journal    text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS payouts_external_ref_uniq_idx
    ON payouts (external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS payouts_batch_uniq_idx
    ON payouts (batch_id) WHERE status <> 'reversed';
CREATE INDEX IF NOT EXISTS payouts_status_idx ON payouts (status);
CREATE INDEX IF NOT EXISTS settlement_batches_merchant_idx
    ON settlement_batches (merchant_id, created_at DESC);

-- Generalize the outbound webhook delivery key: payment events carry the
-- payment id; settlement/payout events carry their own id. One delivery per
-- (merchant, source, event) so redelivered bus events stay no-ops.
ALTER TABLE merchant_webhook_deliveries ALTER COLUMN payment_id DROP NOT NULL;
ALTER TABLE merchant_webhook_deliveries
    ADD COLUMN IF NOT EXISTS source_id uuid NOT NULL DEFAULT gen_random_uuid();
UPDATE merchant_webhook_deliveries SET source_id = payment_id WHERE source_id IS NULL;
DROP INDEX IF EXISTS merchant_webhook_deliveries_uniq_idx;
CREATE UNIQUE INDEX IF NOT EXISTS merchant_webhook_deliveries_uniq_idx
    ON merchant_webhook_deliveries (merchant_id, source_id, event);
