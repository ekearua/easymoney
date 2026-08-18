-- R10: Maker-checker approval for refunds.
-- approval_status gates the actual refund execution.
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS approval_status text NOT NULL DEFAULT 'approved';
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS approved_by uuid REFERENCES admin_users(id);
ALTER TABLE refunds ADD COLUMN IF NOT EXISTS approved_at timestamptz;

-- Existing refunds (created before maker-checker) are auto-approved.
UPDATE refunds SET approval_status = 'approved' WHERE approval_status = 'pending_approval';
