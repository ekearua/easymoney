-- Payment preferences (SAVED_CARD_METHODS_PLAN.md Phase 1): the rail a payer
-- chose last time, remembered per surface so the method picker can offer it
-- first instead of asking the same question on every purchase.
--
-- This table deliberately holds NO card data. Posture A of the plan is
-- "remember the choice, not the instrument": there is no card number, CVV or
-- PIN column here, and there never may be (a schema guard test fails the build
-- if one appears). The saved-token instrument table ships separately, behind
-- the tokenizer decision recorded in CARD_ON_FILE_DECISION.md — a table is not
-- created before the code that populates it exists.
--
-- Two scopes:
--   'default'  — the payer's standing choice on that surface, set by their
--                most recent successful payment;
--   'merchant' — the habit at one payee, so a payer who always pays one
--                merchant by bank transfer and everyone else by card gets both.
-- Resolution is merchant-first, then default.
--
-- A merchant scope carries a scope_ref (the merchant id); a default scope never
-- does. The CHECK below pins that, so a future writer cannot create a row whose
-- scope and reference disagree.
CREATE TABLE IF NOT EXISTS payment_preferences (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('whatsapp', 'web')),
    scope text NOT NULL CHECK (scope IN ('default', 'merchant')),
    scope_ref text NOT NULL DEFAULT '',
    rail text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One preference per payer, surface and scope. The upsert path relies on this
-- key, so a repeat payment updates the remembered rail rather than piling up
-- rows.
CREATE UNIQUE INDEX IF NOT EXISTS payment_preferences_key_idx
    ON payment_preferences(user_id, channel, scope, scope_ref);

ALTER TABLE payment_preferences ADD CONSTRAINT payment_preferences_scope_ref_chk
    CHECK ((scope = 'default') = (scope_ref = ''));
