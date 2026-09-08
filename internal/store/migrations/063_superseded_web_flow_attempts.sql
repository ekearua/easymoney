-- Auto-refund of superseded web-flow attempts. When a gateway attempt is
-- abandoned (the customer cancelled/declined on the hosted page and the flow
-- was reopened for retry via ReopenWebFlowForRetry), that attempt is marked
-- superseded here. If it later verifies as succeeded (a slow provider callback
-- or a success that lands after the retry already paid), the money never
-- reaches the customer twice: the app auto-refunds the superseded attempt the
-- moment its success is applied.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS superseded_at timestamptz;

CREATE INDEX IF NOT EXISTS payments_superseded_idx
    ON payments (superseded_at)
    WHERE superseded_at IS NOT NULL;