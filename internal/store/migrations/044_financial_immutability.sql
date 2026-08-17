-- R5: Immutability triggers on financial tables.
-- Amounts are set once at creation and must never be modified.
-- Status transitions are allowed; amount mutations are blocked.

-- Block UPDATE on refund amounts
CREATE OR REPLACE FUNCTION reject_refund_amount_mutation()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.amount_kobo IS DISTINCT FROM OLD.amount_kobo THEN
    RAISE EXCEPTION 'refund amount_kobo is immutable; use a reversal journal instead';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_refund_amount_immutable
  BEFORE UPDATE ON refunds
  FOR EACH ROW EXECUTE FUNCTION reject_refund_amount_mutation();

-- Block UPDATE on payout amounts
CREATE OR REPLACE FUNCTION reject_payout_amount_mutation()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.amount_kobo IS DISTINCT FROM OLD.amount_kobo THEN
    RAISE EXCEPTION 'payout amount_kobo is immutable; use a reversal journal instead';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_payout_amount_immutable
  BEFORE UPDATE ON payouts
  FOR EACH ROW EXECUTE FUNCTION reject_payout_amount_mutation();

-- Block direct UPDATE on payment amount_kobo
CREATE OR REPLACE FUNCTION reject_payment_amount_mutation()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.amount_kobo IS DISTINCT FROM OLD.amount_kobo THEN
    RAISE EXCEPTION 'payment amount_kobo is immutable; create a refund or adjustment journal instead';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_payment_amount_immutable
  BEFORE UPDATE ON payments
  FOR EACH ROW EXECUTE FUNCTION reject_payment_amount_mutation();
