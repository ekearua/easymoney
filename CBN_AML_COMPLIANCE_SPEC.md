# Faya AML/CFT/CPF compliance specification

**Status:** Draft for compliance review. This document is the regulatory
specification for the platform's AML obligations: what CBN and NFIU require,
where the codebase already satisfies each obligation (with the code that
proves it), what is a gap, and what the platform is explicitly not (which
determines scope). Where an obligation is only partially met, the gap is
named with the evidence check that will close it.

**Prepared against:** MLPPA 2022; CBN AML/CFT/CPF Regulations 2022; CBN
Customer Due Diligence Regulations 2023 (as applied to this platform's
ladder); NFIU STS/FTR/CTR/STR admin instructions (money-space caps and CTR
boundary); CBN Baseline Standards for Automated AML/CFT/CPF Solutions (final,
10 March 2026); TPPA 2022 sanctions and NSC designations. **Vendor selection
for screening intelligence is a separate decision** — see
`AML_VENDOR_DECISION_RECORD.md`; this spec constrains that purchase but does
not make it.

---

## 1. Regulatory instruments and applicability

| Instrument | What it is | Applicability to Faya |
|---|---|---|
| **Money Laundering (Prevention and Prohibition) Act 2022 (MLPPA)** | The principal Act: defines ML/FT/PF offences, CDD, record-keeping (5 years), reporting duties, PEP definition, the **Terrorism (Prevention and Prohibition) Act 2022 (TPPA)** cross-references sanctions obligations | Applies as a reporting entity under the designated non-financial business / OFI perimeter |
| **CBN AML/CFT/CPF Regulations 2022** (issued under s.63 MLPPA) | Operational regulations: CDD tiers, PEP/EDD, record-keeping, programme requirements, monitoring and reporting duties | Applies to all AML-scoped CBN institutions; governs the KYC ladder and programme |
| **CBN Customer Due Diligence Regulations 2023** | The CDD detail: identification, verification, beneficial ownership, EDD triggers | Governs the tier ladder, KYB/UBO collection, EDD triggers |
| **CBN Baseline Standards for Automated AML/CFT/CPF Solutions** (final, 10 March 2026) | The automation standard: watchlists, fuzzy matching, real-time updates, PEP DB, adverse media, automated interdiction, STR/CTR generation, annual independent validation | Applies to OFIs on a **24-month window**; roadmap was due shortly after issue |
| **NFIU admin instructions (STS/FTR/CTR/STR)** | Reporting thresholds and filing mechanics to NFIU | Applies from the first reportable event; CTR boundary configured as `REPORT_CTR_THRESHOLD_KOBO` |
| **Terrorism (Prevention and Prohibition) Act 2022 (TPPA)** | Sanctions/designations regime; **Nigeria Sanctions Committee (NSC)** list | Freezing obligation sits with the screening lane (`SanctionsScreener` + vendor, per the vendor record §9) |
| **SCUML registration** (under MLPPA) | DNFBP registration for in-scope businesses | Applies to the platform's own registration status — an operator duty, not a code path |
| **NDPA 2023** | Data protection: lawful basis, retention, cross-border terms | Binds retention, admin access and vendor DPAs; interfaces at §6.2 |

**Instrument precedence in this document:** the Regulations 2022 and the CDD
Regulations 2023 define the obligations; the Baseline Standards 2026 define
the automation bar for the obligations; NFIU instructions define the report
formats and thresholds. Where this spec says "the Regulations", it means the
2022 CBN AML/CFT/CPF Regulations.

---

## 2. What the platform is (and is not) — the scope cut

Compliance obligations follow the product, so the spec states the perimeter
before mapping controls. As built:

**The platform is:**

1. A **payment initiation and collection** platform: card checkout via
   Interswitch, bank transfer (one-time accounts / DVA), wallet with ledger.
2. A **limited stored-value wallet**: single-currency (NGN), spend-only
   out of collections — funds flow in from named rails and out to named
   rails under the controls below.
3. A **thrift/ajo savings scheme** with allowances, standing credits and
   payouts — an EMI-like stored-value activity.
4. An **EMI-perimeter OFI** if and when licensed; the controls in this
   spec are written to the OFI bar so the perimeter question is commercial,
   not architectural.

**The platform is NOT (and the spec does not claim):**

- ❌ No self-custody of customer funds beyond the wallet's operational
  float; **no proprietary trading, no FX, no lending, no crypto rails**.
- ❌ **No cross-border payment lane.** All rails are domestic; therefore
  **no travel-rule duty arises** today. If a cross-border rail is added,
  the corresponding obligation activates and this spec must be re-opened
  (§8 roadmap gate).
- ❌ **No custodial ledger of customer balances beyond the wallet; no
  interest; no deposit-taking.**
- ❌ **No cash in or out.** The platform never handles physical cash; CTR
  obligations attach to the *reporting* boundary we monitor for structuring
  detection, not to teller operations.
- ❌ **No mass-payout or payroll product** where Faya is the originator of
  funds on behalf of a merchant's employees.

### 2.1 Perimeter-facing obligations this cut produces

Because money-in is always attributable to an onboarded customer, and
money-out is always a wallet debit, payout or refund to a *screened*
counterparty path, the spec's obligations are: **CDD on every customer
(ladder), screening on every customer and counterparty, monitoring on every
settled movement, reporting on the reportable subset, records on everything.**

### 2.2 Dependencies declared outside code

Three obligations live with people, not code, and are listed so the
compliance file is complete:

| Obligation | Owner | Where it lives |
|---|---|---|
| AML/CFT programme: MLRO appointment, training, independent audit | Operator | Outside this repo; §7 evidence check |
| SCUML registration | Operator | Outside this repo |
| licences/registration with CBN (EMI/OFI status) | Operator | Commercial track; §8 gate |

Obligations that fall outside this cut are collected in §3.7 (the G10–G15 set), so §3 can map controls to the declared perimeter without claiming more than it covers.

---

## 3. Obligation → control mapping (the core of this spec)

Each row: the obligation, its instrument source, the control that exists in
code (or the gap), and the evidence a reviewer can open. This is written so
a compliance reviewer can trace obligation → code → test without reading the
whole repo.

### 3.1 Customer due diligence

| Obligation (source) | Control | Evidence |
|---|---|---|
| **CDD required before establishing business relations** (Reg. 2022; CDD Reg 2023) | Tiered onboarding with evidence-gated advancement; no payment before identity exists | `AdvanceKYCTier` adjacency + evidence keys; onboarding FSM (aggregated one-message collection); `store_allowances.go` |
| **Tier 1 (simplified DD): identity basics, phone, address** | L1 from `tier1_basic_details` + email verification with lockout/resend | Onboarding sim tests; `VerifyEmailCode` path |
| **Tier 2 (standard DD): government photo ID, BVN + NIN** | Both BVN **and** NIN verified via the ninbvnportal adapter before L2 | `bvn_verified` + `nin_verified` evidence keys; adapter in `internal/providers` |
| **Tier 3 (EDD): address proof + source of wealth/occupation** | `address_proof` + `edd_completed` keys gate L3 | `AdvanceKYCTier` |
| **Corporate KYB: CAC, TIN, UBO, SCUML evidence** (CDD Reg 2023 Part on legal persons) | B0→B1 gated on **reviewer-verified documents** — chat aggregation (`kyb_request_details`) or browser upload with per-document review | `DeriveKYBEvidence`/`VerifiedKYBEvidence`; migration 078; `AdvanceKYBTier` refuses unverified B1 |
| **PEP status determination** (Reg. 2022 Part; CBN PEP Guidelines) | Screening produces the PEP list; PEP **report** exists (`reports.TypePEP`) | `SanctionsScreener` port; `screening_results`; `/admin/reports` PEP bundle |
| **PEP: EDD + senior-management approval** | A PEP determination auto-opens one `pep_edd` review case; while open, `AdvanceKYCTier` refuses every advancement; disposal is the SM's own audited status (`sm_approved`/`sm_rejected`) with the EDD evidence attached on approval | `store_pep_edd.go` (`EnsurePEPEDDCase`, `ReviewPEPEDDCase`); migration 081; `store_kyc.go` hook + gate; SM buttons on `/admin/kyc` |
| **EDD triggers: high-risk business, unusual activity, complex ownership** | Risk band drives caps; high-risk counterparty rule in monitor | `RISK_HIGH_SINGLE_LIMIT_KOBO`; monitor `high_risk_counterparty` |
| **Ongoing CDD / periodic review** | 90-day rescreen worker (`KYC_RESCREEN_PERIOD`) | Worker in `app.New` wiring |

### 3.2 Screening

| Obligation (source) | Control | Evidence |
|---|---|---|
| **Screen customers against watchlists** (Reg. 2022; Baseline Std: domestic + international lists, PEP, adverse media) | Screening at onboarding + rescreen; decision vocabulary clear/possible/strong; blocking halts advancement and downgrades | `SanctionsScreener` port; `screening_results` (`provider`, `decision`, `matched_names`, `rescreen_due`) |
| **Screen payments for counterparty risk** (Baseline Std: automated interdiction on confirmed matches) | Payment-time counterparty screening in the `ReserveAllowance` → dispatch window: confirmed hits interdict, ambiguous hits hold with the allowance reserved, screener outages hold (never clear) | §5 |
| **Internal watchlist** (Baseline Std, named) | **Gap (G3)** — no owned list table or enforcement | §5 |
| **Fuzzy/AI matching, real-time list updates** (Baseline Std) | **Vendor lane** — the decision record's R3/R6/R8; the spec does not re-decide the vendor | Vendor record §6–§7 |

### 3.3 Monitoring

| Obligation (source) | Control | Evidence |
|---|---|---|
| **Monitor transactions against the risk profile** (Reg. 2022; Baseline Std) | Rail-aware monitor scores every settled movement — payments, top-ups, standing credits, refunds, payouts — with idempotent dedupe | `RunTransactionMonitorForInput`; 15-min sweep |
| **Rules at minimum: velocity, structuring, round amounts, high-risk counterparty** | All four implemented with config thresholds (`MONITOR_*`) | `internal/kyc/monitor.go` |
| **Structuring below the CTR boundary** | The structuring rule's boundary is the configured CTR threshold, so alerts name the boundary they straddle | `REPORT_CTR_THRESHOLD_KOBO` drives both |
| **Cross-identity patterns** (Reg. 2022 "unusual patterns"; doctrine Layer 4) | Shared-device and ring signals feed the risk score with decay/cancellation (network risk wave) | `RiskEventSharedDevice`/`RiskEventRingMember` in `internal/kyc/network_risk.go`; `/admin/network` |
| **Risk score decay + FP cancellation** (Baseline Std tuning governance) | Decay under `RISK_DECAY_HALF_LIFE` (90d); false-positive resolution cancels the exact weight, dated with the observation | `8e8341d`, `c547db0` |

### 3.3a PEP handling (consolidated)

- **Determination** — the screening lane + `TypePEP` report.
- **Closed (G1):** a PEP determination now opens the EDD workflow with a
  senior-management approval disposition that blocks tier advancement while
  open — see the §3.1 row and the gap register.
- Note: CBN PEP guidance expects a **database-driven** determination with
  source provenance — which is one more reason the vendor lane (fuzzy
  matching + PEP DB) is a purchase, not a table we maintain.

### 3.4 Reporting

| Obligation (source) | Control | Evidence |
|---|---|---|
| **CTR over the threshold** (NFIU CTR instruction; boundary configured) | CTR rows from audited movement data at `REPORT_CTR_THRESHOLD_KOBO` | `internal/reports/reports.go` `TypeCTR`; store query |
| **STR on suspicious activity** (MLPPA; NFIU STR instruction) | STR rows derived from confirmed monitor alerts + case status | `TypeSTR` rows carry alert rule/severity/status + case status |
| **PEP reporting** | PEP report rows from screening data | `TypePEP` |
| **goAML XML filing format** (NFIU goAML portal) | `STRgoAML`/`CTRgoAML`/`PEPgoAML` encoders beside the CSV/JSON writers; element-count + required-field validation before the bytes leave | `internal/reports/reports_goaml.go` (`GoAMLSchemaVersion goaml/4.0-nfiu-ng`, `ValidateGoAML`); `/admin/reports` XML download links |
| **Timelines: STR within 24h of suspicion; CTR by the 7th...** (NFIU instructions) | One idempotent filing clock per report row, keyed by stable reference; STR due 24h from the alert, CTR due the 7th of the following month; completion stamped by alert resolution, overdue clocks surfaced on the reports page | `store_reports_clocks.go` (`FilingDeadline`, `SyncReportFilingClocks`, `DueReportFilingClocks`); migration 082; `internal/app/reports.go` sync + `/admin/reports` clocks panel |

**G4/G5 closed note:** the NFIU/CBN-shaped `reports.ReportBundle` rows now
have their filing mechanics: the goAML XML encoders (§3.4) and the
per-row filing clocks surfaced on `/admin/reports`.

### 3.5 Record-keeping and audit

| Obligation (source) | Control | Evidence |
|---|---|---|
| **Keep records ≥5 years** (MLPPA; Reg. 2022) | `AML_RECORD_RETENTION` (floor 5y, `0` dev escape) clamps every AML-scope purge class, including merchant registrations | `store_retention.go`; `config.go` validation |
| **Records sufficient to reconstruct transactions** (MLPPA) | Ledger, payments, alerts, cases, screening results, audit log — all persisted | Store schema |
| **Tamper-proof audit trail** (Baseline Std) | Hash-chained audit log (`prev_hash` + row hash) on every state change; a scheduled sweep re-verifies the chain every `AUDIT_CHAIN_VERIFY_INTERVAL` (default 1h), records each run, and raises one alert on a clean→broken transition with a reviewer disposition (acknowledged bug vs confirmed tampering) | `store_admin.go` (`RecordAudit`, `VerifyAuditChain`); `store_audit_chain.go` (`VerifyAuditChainScheduled`); migrations 023/025/030/084 |
| **Annual independent validation** (Baseline Std) | One attestation row per validation cycle (created on the clock at startup), recording validator scope, findings, remediation and the signed-evidence location; overdue cycles are highlighted until submitted, and every recording is audited | `store_compliance.go`; migration 083; `/admin/compliance` checklist |

### 3.6 Programme obligations (operator duties, listed for completeness)

MLRO appointment, staff training, internal reporting procedures, independent
audit — live outside the repo. The spec lists them so the compliance file is
whole; the only code-side hook is the **programme evidence check** in §7.

---

## 3.7 Wider-perimeter obligations (the G10–G15 set)

§2 draws the perimeter this spec's §3 mapping is written to. A review of the supplied *Xego KYC & AML Compliance Services Framework* names six further obligations that are real for a compliance programme but sit outside that perimeter's control map. They are registered here as G10–G15 so the gap list is complete without pretending §3 already covers them.

| Obligation (source) | Why §3 does not cover it | Gap | Evidence check that closes it |
|---|---|---|---|
| **Liveness / biometric face verification** (framework §2; CBN identity guidance) | The Tier-3 EDD step records a **self-attestation** ("I confirm I'm completing this verification myself in real time") and stamps `liveness: passed`; there is no provider call, so the control is a declaration, not a verification | **G10** | A `LivenessVerifier` port with a deterministic simulated default, wired into the individual EDD step so `EvEDDCompleted` is granted only on a provider pass, recorded with provider + reference like every other verification |
| **Duplicate-identity detection** (multiple-identity risk; Reg. 2022) | Nothing enforces one person → one customer: no BVN/NIN uniqueness or overlap check exists, and the Trust Fabric links *phones, devices and bank accounts*, which is a different edge | **G11** | Identity-key uniqueness/overlap query; a second profile on the same BVN/NIN is refused and opens a review case |
| **ID/document authenticity** (CDD Reg 2023) | Documents are uploaded, OCR'd for NIN/BVN, and per-document **human**-reviewed; no tamper/authenticity provider and no face-to-document comparison | **G12** | An authenticity verdict per document from a provider port, surfaced beside the human decision in the reviewer panel and required before that document's evidence key counts |
| **Fraud monitoring as a distinct engine** (framework §10; Baseline Std adjacency) | The monitor is AML-shaped — velocity, structuring, round amounts, counterparty risk. No account-takeover, authentication-anomaly or payment-fraud rules exist | **G13** | A fraud rule set with its own alert type and queue; device/network and authentication signals feed fraud, PEP/sanctions/adverse-media feed AML, and the two remain separable |
| **Geography and expected-activity risk inputs** (risk-based approach, Reg. 2022) | The risk engine has no country/jurisdiction field and no expected-volume or expected-geography capture, so two of the inputs a risk-based approach names cannot move a band | **G14** | Country-of-residence/operation and an expected-volume band captured on the profile, a geography weight in the score, and the values visible on the customer record |
| **AIF and other NFIU report types** (NFIU reporting instructions) | §3.4 generates STR, CTR and PEP only; no AIF or other NFIU form exists | **G15** | An AIF encoder beside the goAML writers, a bundle row, and a filing-clock class validated like STR/CTR |

---

## 4. Configuration surface (the compliance knobs, inventoried)

So a reviewer can audit the control parameters without reading code:

| Knob | Controls | Compliance anchor |
|---|---|---|
| `REPORT_CTR_THRESHOLD_KOBO` | CTR boundary + structuring rule | NFIU CTR instruction |
| `MONITOR_*` (velocity, structuring window, round-amount, high-risk) | Monitor rule thresholds | Reg. 2022 monitoring |
| `RISK_DECAY_HALF_LIFE` | Risk-score decay (90d default) | Baseline Std tuning governance |
| `RISK_HIGH_SINGLE_LIMIT_KOBO` | High-band single-transaction cap | EDD/interdiction |
| `KYC_RESCREEN_PERIOD` | Ongoing CDD cadence | Reg. 2022 ongoing DD |
| `TUNING_FP_PERCENT`, `TUNING_MIN_LABELLED` | FP/FN tuning governance | Baseline Std FP/FN governance |
| `AI_TOKENS_PER_NGN` | AI extraction cost metering | NDPA-adjacent cost governance |
| Tier ceilings (`kyc_tier_limits`, migration 073) | Per-tier single/daily/monthly ceilings | CDD Reg 2023 ladder |

---

Candidate envelope knobs — `AML_WATCHLIST_INTERNAL`, payment-screening
hold/interdiction config, retention floors — are **deliberately deferred
until the §5 gaps are scheduled**: adding envelope entries before controls
exist would make the template lie, which is exactly what M0.1 was done to
stop.

---

## 5. Gap register

Numbered, each with the evidence check that closes it — this is the spec's
action list.

| # | Gap | Why it matters | Evidence check that closes it |
|---|---|---|---|
| **G1** | ~~**PEP EDD + senior-management approval**~~ — **closed.** A PEP determination auto-opens one `pep_edd` review case (migration 081 extends the case-type check; unique partial index enforces one open case per user); the ordinary approve/reject path refuses `pep_edd` cases so only the SM disposition lands; `ReviewPEPEDDCase` stamps `sm_approved`/`sm_rejected` (audited, its own status) and an approval attaches the `edd_completed` evidence key; `AdvanceKYCTier` refuses every tier move while a `pep_edd` case is open | Reg. 2022 PEP obligations; CBN PEP guidance expects documented approval | `store_pep_edd.go`; `store_kyc.go` hook + gate + refuse-guard; SM buttons on `/admin/kyc`; `store_pep_edd_integration_test.go` |
| **G2** | ~~**Payment-time counterparty screening + hold/interdiction**~~ — **closed against the simulated vendor.** `PaymentScreener` port with a deterministic simulated adapter (`PAYMENT_SCREENING_PROVIDER`, default `simulated`; the live vendor adapter is the G9 screening purchase); `ScreenPayout` gates the settlement `ReserveAllowance` → dispatch window — `blocked` fails the payout outright, `possible`/`strong` move it to the new `held` status (migration 086) and return `ErrMovementHeld`, an outage holds rather than clears; the money-out reservation is never touched by the gate, so a held or interdicted movement keeps counting against the ceiling. `ScreenUserWithdrawal` applies the same semantics to wallet withdrawals (refusal keeps the reservation; an outage releases it because withdrawals have no reviewer release path). `payment_screenings` (migration 085) records every decision keyed by rail + movement ref — payouts are destination-keyed so a re-created payout to a different account re-screens; replays reuse the recorded decision without a vendor call. Dispositions at `/admin/payment-screenings`: release re-queues the payout and marks the screening `manually_cleared`; reject reverses it, releases the reservation, and reopens the batch for re-settlement to a different destination | Baseline Std automated interdiction; NIP is irreversible | `ports.go` (`PaymentScreener`); `providers/paymentscreening` (+ test); `store_payment_screening.go`; `settlement.go` seam; `cli.go` withdrawal seam; `admin_payment_screening.go` + template; migrations 085/086; `settlement_screening_test.go`; `store_payment_screening_integration_test.go` |
| **G3** | ~~**Internal watchlist**~~ — **closed.** `internal_watchlist` table (migration 080) with normalised-identity uniqueness; enforcement in `RecordScreeningResult` rewrites any screening of a listed identity to `blocked` with provider `internal_watchlist` and the listing id stamped on the result, so onboarding gates, the rescreen worker and reporting all see the interdiction; admin CRUD at `/admin/internal-watchlist` with audited add/remove | Baseline Std names it; vendor record §8.2 scoped the build | `store_internal_watchlist.go`; `store_kyc.go` enforcement; `admin_internal_watchlist.go` + template; `store_internal_watchlist_integration_test.go` |
| **G4** | ~~**goAML XML filing format**~~ — **closed.** `STRgoAML`/`CTRgoAML`/`PEPgoAML` emit the goAML XML envelope (`goaml/4.0-nfiu-ng`) beside the CSV/JSON writers, with `ValidateGoAML` checking element counts and required fields before download; `/admin/reports` serves the XML beside the existing formats | NFIU files via goAML; CSV rows do not file | `reports_goaml.go`; `reports_goaml_test.go`; `/admin/reports` links |
| **G5** | ~~**Filing-clock tracking**~~ — **closed.** `report_filing_clocks` (migration 082) holds one idempotent clock per report reference: STR due 24h from the alert, CTR due the 7th of the following month (`FilingDeadline`); bundle generation syncs and resolution completes them; `/admin/reports` shows due clocks with overdue counts | NFIU timelines; missed clocks are reportable failures | `store_reports_clocks.go`; `internal/app/reports.go` sync; clocks panel in `admin_reports.html`; `store_reports_clocks_integration_test.go` |
| **G6** | ~~**AML 5-year retention floor**~~ — **closed.** `AML_RECORD_RETENTION` (default and minimum 5y, `0` = dev escape) clamps every AML-scope purge class — payments, invoices, thrift, audit, and merchant registrations (previously hard-deleted unarchived after the 90-day operational period); account records of never-transacted users are clamped to the same floor; the archive writer is re-run-safe (`appendArchiveGuarded`); config rejects sub-5y values and a `RETENTION_PERIOD` above the floor | MLPPA/Reg. 2022 | `store_retention.go` (`amlCutoff`, `archiveAndPurgeMerchantRegistrations`); `config.go` validation; `store_amlretention_integration_test.go` |
| **G7** | ~~**Audit tamper-evidence**~~ — **closed.** `audit_logs` carries a hash chain (`prev_hash` + row hash, migration 023) written on every `RecordAudit`; `VerifyAuditChain` recomputes and checks linkage, and `/admin` renders chain status. The same pattern covers the retention archive (025) and the ledger (030). Residual nuance, **closed** with the G8 wave: verification no longer waits for a page load — `VerifyAuditChainScheduled` re-verifies every `AUDIT_CHAIN_VERIFY_INTERVAL` (default 1h), records each run in `audit_chain_checks` (migration 084), and raises one alert in `audit_chain_alerts` on a clean→broken transition for reviewer disposition (acknowledged bug vs confirmed tampering) | Baseline Std "tamper-proof audit" | `store_admin.go` (`RecordAudit`, `VerifyAuditChain`); migrations 023/025/030 |
| **G8** | ~~**Annual independent validation evidence**~~ — **closed.** `compliance_attestations` (migration 083) holds one row per validation cycle with scope, summary, findings, remediation, evidence URL, status (scheduled → in_progress → submitted) and completion date; the current cycle is ensured at startup; `/admin/compliance` renders the checklist with overdue highlighting and per-cycle record forms, every recording audited. The operator duty to *commission* the validation remains a people obligation (§3.6) — the code now stores and surfaces the evidence | Baseline Std; operator duty | `store_compliance.go`; `admin_compliance.go` + template; `store_compliance_integration_test.go` |
| **G9** | **Adverse-media + domestic-list screening** | Baseline Std names both; vendor lane | Closes with the vendor purchase: the pre-signature trial and the signature-day checklist live in `AML_VENDOR_EVIDENCE_PACK.md` (vendor record §7—§8) |
| **G10** | **Liveness / face verification** — the Tier-3 EDD step is a self-attestation stamped `liveness: passed`; no provider call exists, so the control is a declaration rather than a verification | CBN identity guidance; framework §2 | See §3.7 — a `LivenessVerifier` port (simulated default) that `EvEDDCompleted` depends on |
| **G11** | **Duplicate-identity detection** — nothing enforces one person → one customer across BVN/NIN | Multiple-identity risk; Reg. 2022 | See §3.7 — identity-key uniqueness/overlap check; a second profile is refused and opens a case |
| **G12** | **ID/document authenticity** — documents are OCR'd and human-reviewed, but never authenticity-checked | CDD Reg 2023 | See §3.7 — provider authenticity verdict per document, beside the human decision |
| **G13** | **Fraud engine, distinct from the AML monitor** — no account-takeover, authentication-anomaly or payment-fraud rules | Baseline Std adjacency; framework §10 | See §3.7 — fraud rules with their own alert type and queue |
| **G14** | **Geography + expected-activity risk inputs** — no jurisdiction field, no expected-volume capture, no score weight for either | Risk-based approach; Reg. 2022 | See §3.7 — jurisdiction field + expected-volume band, with score weights |
| **G15** | **AIF and other NFIU report types** — only STR, CTR and PEP are generated | NFIU reporting instructions | See §3.7 — encoder, bundle row, filing-clock class |

**Scheduling note:** G9 resolves in the vendor-record lane (§8 of the
vendor record); G2's seam ships against the simulated vendor and its live
adapter drops in behind the same port when the screening contract is signed.
Every gap this spec's §2 cut produces is closed: G1–G8. The G7
residual (scheduled chain-verification alerting) shipped with the G8 wave —
`audit_chain_checks`/`audit_chain_alerts` (migration 084) plus the
`AUDIT_CHAIN_VERIFY_INTERVAL` worker. G9 is the vendor lane.
**Open owned builds: G10** liveness · **G11** duplicate identity · **G12** document
authenticity · **G13** fraud engine · **G14** geography/expected activity ·
**G15** AIF — the wider-perimeter set in §3.7,
unscheduled. Nothing in the register blocks another item; all are additive.

---

## 6. Two hard problems the mapping does not trivially solve

### 6.1 Data minimisation vs. reporting completeness (NDPA interface)

NDPA 2023 (and the General Application and Implementation Document) expects
purpose limitation and minimisation; MLPPA expects the platform to hold
enough identity, transaction and alert data to reconstruct transactions and
defend a filing. The platform's cut: **AML record-keeping is the lawful
basis, purpose-limitation is enforced by access design** — regulator-report
rows are generated from audited data on demand, admin surfaces are
role-gated, risk events never name another customer (the network-signal
privacy rule), and the customer data export excludes other customers'
identifiers. The one unresolved interface: **cross-border transfer of AML
records** never arises today because nothing leaves the jurisdiction — if a
vendor contract (screening, OCR, STT) routes data abroad, the DPA must
carry the NDPA cross-border terms, which is a procurement gate in the
vendor record, not a code path.

### 6.2 Sanctions freezing without a sanctions lane of our own

TPPA/NSC designations create a **freeze** obligation: a designated entity
must be blocked, funds reported. The platform's screening lane produces
decisions on customers; the vendor record's R6 question (domestic list
coverage, NSC list) is what makes freezing real. Until the vendor is
signed, the platform's honest position — recorded here so no one mistakes
it for an implementation claim — is: **screening exists with a simulated
default provider and the decision vocabulary and blocking behaviour are
real; the list breadth and match quality are the vendor's to prove.**

---

## 7. Evidence checks (how a reviewer verifies this spec)

1. **Code traceability** — every "Evidence" cell names a symbol or migration;
   `go build ./...` and the test suites are the executable part of the claim.
   The doc-drift guards (template-load, coverage, one-field-per-message)
   demonstrate the pattern: claims pinned by tests.
2. **Programme evidence check** — the operator duties (§2.2, §3.6) get an
   annual attestation: MLRO named, training done, SCUML status current,
   independent validation scheduled. The code-side hook is the
   `/admin/compliance` attestation checklist (G8).
3. **Regulator-facing rehearsal** — `/admin/reports` must produce, on demand,
   an STR/CTR/PEP bundle a compliance officer would hand over — now with the
   goAML encoding (G4) and filing clocks (G5) beside it.

---

## 8. Roadmap alignment (what changes, where)

The Baseline Standards clock (24 months for OFIs) and the Trust Fabric
milestones align as follows:

| Wave | Items | Lane |
|---|---|---|
| **Shipped** | G1 PEP EDD + SM approval · G2 payment screening + hold (simulated vendor) · G3 internal watchlist · G4 goAML XML · G5 filing clocks · G6 retention floor · G7 tamper-evidence (chain + scheduled verification) · G8 validation attestations | Owned builds — small commits |
| **Vendor track** | G2 live screening adapter (port + seam already in place) · G9 adverse media/domestic lists | Vendor record §7 trial → §8 integration |
| **Open (owned, unscheduled)** | **G10** liveness/face verification · **G11** duplicate-identity detection · **G12** document authenticity · **G13** fraud engine · **G14** geography + expected-activity risk inputs · **G15** AIF and other NFIU report types — the wider-perimeter set in §3.7 | Needs a wave; §3.7 names the evidence check that closes each |

**Re-open triggers for this spec:** cross-border rail (travel rule
activates), perimeter change (licence status), vendor signed (G2/G9
evidence moves), Baseline Standards amendments, or any new rail (monitor
coverage must extend within the same wave as the rail ships — the M2.1
rule).

---

## 9. Sources

- Money Laundering (Prevention and Prohibition) Act 2022; Terrorism
  (Prevention and Prohibition) Act 2022.
- CBN AML/CFT/CPF Regulations 2022; CBN Customer Due Diligence Regulations
  2023; CBN PEP Guidelines (Guidance Notes).
- CBN Baseline Standards for Automated AML/CFT/CPF Solutions — final issue
  10 March 2026 (May 2025 exposure draft preceded); 24-month OFI window.
- NFIU administration of STS/FTR/CTR/STR reporting; goAML portal
  requirements; CTR thresholds (₦5m individual / ₦10m corporate cash — the
  platform's non-cash lane anchors its own boundary in
  `REPORT_CTR_THRESHOLD_KOBO` rather than the cash thresholds).
- NDPA 2023 and GAID; SCUML (EFCC) registration regime.
- Wider-perimeter obligations (G10–G15): the *Xego KYC & AML Compliance
  Services Framework* requirements document supplied for review (October 2026).
  Where it and this spec disagree, this spec governs.
- Vendor capability claims: `AML_VENDOR_DECISION_RECORD.md` §10 caveats
  apply unchanged here.

*Caveat: instrument summaries reflect the published texts and secondary
analyses available at drafting time; the final texts govern, and the
compliance owner should verify against the gazetted versions before filing
anything with a regulator.*
