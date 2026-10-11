-- P1 (restricted-purpose program purses): a sponsor merchant (a ministry, an
-- NGO, an employer) funds a *program*; enrolled individuals hold a restricted
-- purse whose money can only be spent at that program's approved merchants.
--
-- The purse is a real double-entry ledger account
-- (2302_program_purse:<purse-id>), not a tag on the ordinary wallet. That is
-- deliberate: the wallet withdrawal rail only ever touches 2301_user_wallet,
-- so restricted money cannot be cashed out by construction rather than by a
-- check somebody could forget to write.
--
-- Deliberately NO foreign keys to users or merchants: retention and DSR purge
-- delete those rows, and a purse or enrollment must never block a purge
-- (mirroring wallet_accounts, 058). Integrity is enforced in the store.
--
-- merchant_type classifies the merchant for admin filters and search only. The
-- per-program allow-list below is what actually authorises a spend, so a
-- mistyped type can never widen access to program money.
ALTER TABLE merchants ADD COLUMN IF NOT EXISTS merchant_type text NOT NULL DEFAULT 'standard'
    CHECK (merchant_type IN ('standard', 'sponsor', 'program_acceptor'));
CREATE INDEX IF NOT EXISTS merchants_type_idx ON merchants (merchant_type);

-- A program: the sponsor, the label beneficiaries see, and the default
-- validity window a new purse inherits. default_validity_days = 0 means the
-- purse never expires on its own (still recallable by a sponsor action).
CREATE TABLE IF NOT EXISTS merchant_programs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    sponsor_merchant_id uuid NOT NULL,
    purpose_label text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    default_validity_days integer NOT NULL DEFAULT 0 CHECK (default_validity_days >= 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS merchant_programs_sponsor_idx ON merchant_programs (sponsor_merchant_id);
CREATE INDEX IF NOT EXISTS merchant_programs_status_idx ON merchant_programs (status);

-- The allow-list. 'blocked' is a distinct state from removal so an operator
-- can refuse one merchant without losing the record that it was once approved
-- (or vice versa), and removed_at keeps the audit trail of a removal.
CREATE TABLE IF NOT EXISTS merchant_program_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'approved' CHECK (status IN ('approved', 'blocked')),
    block_reason text NOT NULL DEFAULT '',
    added_by uuid,
    added_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz,
    UNIQUE (program_id, merchant_id)
);

CREATE INDEX IF NOT EXISTS merchant_program_members_merchant_idx
    ON merchant_program_members (merchant_id, status);

-- The link from a program to an individual account: "this farmer belongs to
-- the Ministry of Agriculture program". Enrollment is the precondition for a
-- purse; removing it does not claw money back on its own.
CREATE TABLE IF NOT EXISTS program_enrollments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id uuid NOT NULL,
    user_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'removed')),
    enrolled_by uuid,
    enrolled_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (program_id, user_id)
);

CREATE INDEX IF NOT EXISTS program_enrollments_user_idx ON program_enrollments (user_id, status);

-- The purse: one per (program, beneficiary), never co-mingled with the
-- ordinary wallet. account_code is the ledger account
-- (2302_program_purse:<id>); the balance is the sum of that account's journal
-- entries, so the purse can never diverge from the book.
CREATE TABLE IF NOT EXISTS program_purses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id uuid NOT NULL,
    user_id uuid NOT NULL,
    enrollment_id uuid NOT NULL,
    account_code text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    opened_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    closed_at timestamptz,
    returned_kobo bigint NOT NULL DEFAULT 0,
    UNIQUE (program_id, user_id)
);

CREATE INDEX IF NOT EXISTS program_purses_user_idx ON program_purses (user_id, status);
CREATE INDEX IF NOT EXISTS program_purses_expiry_idx ON program_purses (status, expires_at);

-- Which purse funded a payment (nil = the ordinary wallet or the gateway
-- rails). The money-movement check in transitionPayment reads this to decide
-- which ledger account to debit and to re-verify the payee is on the program's
-- allow-list inside the same transaction that moves the money.
ALTER TABLE payments ADD COLUMN IF NOT EXISTS purse_id uuid;
CREATE INDEX IF NOT EXISTS payments_purse_idx ON payments (purse_id) WHERE purse_id IS NOT NULL;

-- A bulk batch may be a program disbursement: its eligible recipients are paid
-- into their program purse (restricted) instead of their ordinary wallet
-- (withdrawable). Nil is an ordinary bulk payment and behaves exactly as
-- before, so the existing rail is unchanged for every existing batch.
ALTER TABLE bulk_batches ADD COLUMN IF NOT EXISTS program_id uuid;
CREATE INDEX IF NOT EXISTS bulk_batches_program_idx ON bulk_batches (program_id) WHERE program_id IS NOT NULL;
