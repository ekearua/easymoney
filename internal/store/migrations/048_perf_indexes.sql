-- P3-2: critical missing indexes for hot-path queries.

-- CutSettlement: payments filtered by (merchant_id, status).
CREATE INDEX IF NOT EXISTS payments_merchant_status_idx ON payments (merchant_id, status);

-- CutSettlement NOT EXISTS subquery: settlement_lines looked up by payment_id.
CREATE INDEX IF NOT EXISTS settlement_lines_payment_idx ON settlement_lines (payment_id);

-- ListQueuedPayouts: payouts filtered by status, ordered by created_at.
CREATE INDEX IF NOT EXISTS payouts_queued_idx ON payouts (status, created_at) WHERE status='queued';

-- ListPayouts per merchant: payouts filtered by merchant_id, ordered by created_at.
CREATE INDEX IF NOT EXISTS payouts_merchant_created_idx ON payouts (merchant_id, created_at DESC);

-- ListFailedWebhooks: webhook_deliveries filtered by processing_status, ordered by received_at.
CREATE INDEX IF NOT EXISTS webhook_deliveries_status_received_idx ON webhook_deliveries (processing_status, received_at DESC);

-- Reconciliation legs 2+4: ledger_entries filtered by account+entry_type+source_type.
CREATE INDEX IF NOT EXISTS ledger_entries_account_type_idx ON ledger_entries (account, entry_type, source_type, reversal_of);
