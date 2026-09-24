-- Two-step login retry support. Verifying a code used to consume the pending
-- step before the code was checked, so a single mistyped digit produced an
-- "expired" login on every retry. The pending row now survives a wrong code
-- and carries an attempt counter that bounds guessing per step.
ALTER TABLE totp_pending_logins
    ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0;
