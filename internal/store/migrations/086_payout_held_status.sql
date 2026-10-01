-- Spec G2 (CBN_AML_COMPLIANCE_SPEC.md §3.5): payment-time counterparty
-- screening. The payouts.status CHECK predates the G2 hold: an ambiguous
-- screen parks a payout in 'held' for reviewer disposition while its
-- money-out reservation stays in place. Extend the constraint in place.

ALTER TABLE payouts DROP CONSTRAINT payouts_status_check;
ALTER TABLE payouts ADD CONSTRAINT payouts_status_check
    CHECK (status IN ('queued','processing','completed','failed','reversed','held'));
