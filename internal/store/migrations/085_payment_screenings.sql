-- Spec G2 (CBN_AML_COMPLIANCE_SPEC.md §3.5): payment-time counterparty
-- screening. Every money-out movement is screened in the ReserveAllowance →
-- dispatch window; the money-out reservation stays in place so the movement
-- cannot be re-attempted past the screen. Three outcomes:
--
--   clear    — dispatch proceeds;
--   blocked  — the movement is interdicted (payout rejected / withdrawal
--              refused); the reservation remains so the amount stays
--              counted against the subject's ceiling;
--   possible/strong — the movement is held for review (payout status
--              'held', withdrawal reservation left in place pending
--              disposition).
--
-- The screening row is keyed by the movement's stable identity (rail +
-- movement reference), so a re-attempt of the same movement reuses its
-- recorded decision instead of re-screening. The payout ref embeds the
-- destination id (payout:<batch_no>:<destination_id>) so a payout re-created
-- for the same batch to a different account always re-screens.

CREATE TABLE IF NOT EXISTS payment_screenings (
    id BIGSERIAL PRIMARY KEY,
    rail TEXT NOT NULL,
    -- movement_ref: payout:<batch_no> or wallet-withdraw:<uuid>, matching
    -- the allowance reservation ref of the screened movement.
    movement_ref TEXT NOT NULL,
    subject_id UUID NOT NULL,
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    counterparty_name TEXT NOT NULL,
    account_number TEXT NOT NULL DEFAULT '',
    bank_code TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL,
    provider_ref TEXT NOT NULL DEFAULT '',
    -- manually_cleared is a reviewer disposition (ReleaseHeldPayout): an
    -- ambiguous hit released by the queue re-reads as cleared on re-attempt.
    decision TEXT NOT NULL CHECK (decision IN ('clear','possible','strong','blocked','manually_cleared')),
    matched_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (rail, movement_ref)
);

CREATE INDEX IF NOT EXISTS idx_payment_screenings_open
    ON payment_screenings (decision, created_at DESC)
    WHERE decision IN ('possible','strong');
