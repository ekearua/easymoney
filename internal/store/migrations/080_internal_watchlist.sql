-- Spec G3 (CBN_AML_COMPLIANCE_SPEC.md §5): the owned internal watchlist.
-- The CBN Baseline Standards for Automated AML/CFT/CPF Solutions name an
-- internal watchlist as a capability no screening vendor supplies: the
-- platform's own list of named identities that must never be auto-cleared.
-- Entries are reviewer-owned: a name (plus optional DOB, phone and bank
-- account reference) with the reason it was listed and who listed it.
-- Screening enforcement treats any match as a hard block: the decision is
-- rewritten to "blocked" and a screening review case is opened.
CREATE TABLE IF NOT EXISTS internal_watchlist (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    full_name text NOT NULL,
    date_of_birth date,
    phone_number text NOT NULL DEFAULT '',
    bank_account_ref text NOT NULL DEFAULT '',
    reason text NOT NULL,
    listed_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The enforcement lookup is by normalised name; the unique key makes the
-- list idempotent for repeated adds of the same identity.
CREATE UNIQUE INDEX IF NOT EXISTS internal_watchlist_identity_key
    ON internal_watchlist (lower(btrim(full_name)),
                           COALESCE(date_of_birth, DATE '0001-01-01'));

CREATE INDEX IF NOT EXISTS internal_watchlist_name_idx
    ON internal_watchlist (lower(btrim(full_name)));

-- screening_results carries the identity that was screened (G3) so the
-- internal-watchlist interdiction is auditable per screening event, and the
-- listing id that forced a blocked decision.
ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS legal_name text NOT NULL DEFAULT '';
ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS date_of_birth date;
ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS phone_number text NOT NULL DEFAULT '';
ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS bank_account_ref text NOT NULL DEFAULT '';
ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS internal_watchlist_id uuid;

