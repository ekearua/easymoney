-- Post-deploy synthetic checks (release/CD). Each row is one *stage* of one run
-- of `demo synthetic`, not one row per run: the useful post-incident question is
-- "which stage failed", and a run that aborts halfway still has to leave
-- evidence of how far it got. A run is identified by run_id, so a run counts as
-- healthy only when none of its stages failed.
--
-- The checks are deliberately read-only against the payments domain — see
-- internal/app/synthetic.go for why a synthetic payment is not fabricated in a
-- live ledger.
CREATE TABLE IF NOT EXISTS synthetic_checks (
    id          uuid PRIMARY KEY,
    run_id      uuid NOT NULL,
    ran_at      timestamptz NOT NULL DEFAULT now(),
    stage       text NOT NULL,
    ok          boolean NOT NULL,
    skipped     boolean NOT NULL DEFAULT false,
    detail      text NOT NULL DEFAULT '',
    duration_ms integer NOT NULL DEFAULT 0,
    source      text NOT NULL DEFAULT 'manual',
    environment text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS synthetic_checks_ran_at_idx ON synthetic_checks (ran_at DESC);
CREATE INDEX IF NOT EXISTS synthetic_checks_run_idx ON synthetic_checks (run_id);
