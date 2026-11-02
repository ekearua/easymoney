-- Approval workflow, filter snapshots and payout modes for bulk payments.
--
-- A batch is now built from a filtered recipient list rather than from one
-- location, so center_id becomes optional and each item keeps a snapshot of the
-- location and geography it was paid under (records can be reassigned later).
ALTER TABLE bulk_batches ALTER COLUMN center_id DROP NOT NULL;

ALTER TABLE bulk_batches DROP CONSTRAINT IF EXISTS bulk_batches_status_check;
ALTER TABLE bulk_batches ADD CONSTRAINT bulk_batches_status_check
    CHECK (status IN ('draft','submitted','approved','rejected','pending','stage1','stage2',
                      'completed','partial','failed','cancelled'));

ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS created_by_member uuid REFERENCES merchant_members(id) ON DELETE SET NULL;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS submitted_at timestamptz;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS approved_by_member uuid REFERENCES merchant_members(id) ON DELETE SET NULL;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS approved_at timestamptz;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS rejected_by_member uuid REFERENCES merchant_members(id) ON DELETE SET NULL;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS rejected_reason text NOT NULL DEFAULT '';
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS filter_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS payout_mode text NOT NULL DEFAULT 'wallet'
    CHECK (payout_mode IN ('wallet','bank'));
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS require_location boolean NOT NULL DEFAULT true;

ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS center_id uuid REFERENCES merchant_centers(id) ON DELETE SET NULL;
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT '';
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS lga text NOT NULL DEFAULT '';
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS ward text NOT NULL DEFAULT '';
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS polling_unit text NOT NULL DEFAULT '';
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS payout_mode text NOT NULL DEFAULT 'wallet';
ALTER TABLE bulk_batch_items ADD COLUMN IF NOT EXISTS transfer_ref text NOT NULL DEFAULT '';

-- Direct payment (location verification switched off) is its own job kind.
ALTER TABLE bulk_jobs DROP CONSTRAINT IF EXISTS bulk_jobs_kind_check;
ALTER TABLE bulk_jobs ADD CONSTRAINT bulk_jobs_kind_check
    CHECK (kind IN ('bulk_stage1','bulk_stage2','bulk_pay'));

-- Keep each recipient's last-paid snapshot current without an extra write in
-- the payment path: flipping an item to paid updates its record once.
CREATE OR REPLACE FUNCTION center_users_touch_last_paid() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'paid' AND (OLD.status IS DISTINCT FROM 'paid') AND NEW.center_user_id IS NOT NULL THEN
        UPDATE center_users
        SET last_paid_at = now(), last_paid_amount_kobo = NEW.amount_kobo
        WHERE id = NEW.center_user_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS bulk_batch_items_touch_last_paid ON bulk_batch_items;
CREATE TRIGGER bulk_batch_items_touch_last_paid
    AFTER UPDATE OF status ON bulk_batch_items
    FOR EACH ROW EXECUTE FUNCTION center_users_touch_last_paid();

-- Admin-controlled per-merchant bulk settings.
ALTER TABLE merchants ADD COLUMN IF NOT EXISTS bulk_location_required boolean NOT NULL DEFAULT true;
ALTER TABLE merchants ADD COLUMN IF NOT EXISTS bulk_payout_mode text NOT NULL DEFAULT 'wallet'
    CHECK (bulk_payout_mode IN ('wallet','bank'));
