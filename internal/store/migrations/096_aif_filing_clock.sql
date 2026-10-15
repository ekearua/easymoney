-- Spec G15 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §3.4): the Account Information File
-- (AIF) is an NFIU goAML filing that reports the platform's
-- accounts-of-record register. It joins STR/CTR/PEP as a first-class report
-- type with its own filing clock: filed on request or on the same monthly
-- registry cadence as a CTR/PEP (the 7th of the following month, end of day),
-- keyed per account of record (reference "acct:<id>"). The original inline
-- CHECK only admitted STR/CTR/PEP, so re-state it to include AIF.

ALTER TABLE report_filing_clocks DROP CONSTRAINT IF EXISTS report_filing_clocks_report_type_check;
ALTER TABLE report_filing_clocks ADD CONSTRAINT report_filing_clocks_report_type_check CHECK (report_type IN ('STR','CTR','PEP','AIF'));
