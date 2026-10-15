-- Spec G5 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.4): filing-clock tracking. NFIU
-- instructions set real deadlines — an STR within 24 hours of the
-- determination of suspicion, a CTR by the 7th of the following month — and a
-- missed clock is itself a reportable failure. Report rows are regenerated on
-- demand from audited data, so the clock lives in its own table keyed by the
-- generated row's stable identity (report type + report reference): the sync
-- is idempotent, the due date is computed from the row's own event date, and
-- completion is stamped when the row's underlying alert/case reaches a
-- terminal status (or, for CTR, never — a CTR is only ever due).

CREATE TABLE IF NOT EXISTS report_filing_clocks (
    id bigserial PRIMARY KEY,
    report_type text NOT NULL CHECK (report_type IN ('STR','CTR','PEP')),
    reference text NOT NULL,
    event_date timestamptz NOT NULL,
    due_at timestamptz NOT NULL,
    filed_at timestamptz,
    overdue_notified boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (report_type, reference)
);

CREATE INDEX IF NOT EXISTS report_filing_clocks_due_idx
    ON report_filing_clocks(report_type, due_at)
    WHERE filed_at IS NULL;
