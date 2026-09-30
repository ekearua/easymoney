# Xego AML/CFT/CPF compliance specification

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

| Instrument | What it is | Applicability to Xego |
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
- ❌ **No mass-payout or payroll product** where Xego is the originator of
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
| **PEP: EDD + senior-management approval** | **Gap (G1)** — screening decision exists but no EDD workflow/SM approval step on a PEP hit | §5 |
| **EDD triggers: high-risk business, unusual activity, complex ownership** | Risk band drives caps; high-risk counterparty rule in monitor | `RISK_HIGH_SINGLE_LIMIT_KOBO`; monitor `high_risk_counterparty` |
| **Ongoing CDD / periodic review** | 90-day rescreen worker (`KYC_RESCREEN_PERIOD`) | Worker in `app.New` wiring |

### 3.2 Screening

| Obligation (source) | Control | Evidence |
|---|---|---|
| **Screen customers against watchlists** (Reg. 2022; Baseline Std: domestic + international lists, PEP, adverse media) | Screening at onboarding + rescreen; decision vocabulary clear/possible/strong; blocking halts advancement and downgrades | `SanctionsScreener` port; `screening_results` (`provider`, `decision`, `matched_names`, `rescreen_due`) |
| **Screen payments for counterparty risk** (Baseline Std: automated interdiction on confirmed matches) | **Gap (G2)** — payment-time counterparty screening does not exist; the pre-flight window (`ReserveAllowance` → `CreatePayout`) is the seam named in the vendor record §8 | §5 |
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
- **Gap (G1, restated as its own item because CBN treats PEP handling as its
  own obligation family):** no EDD workflow, no senior-management approval
  step, and no heightened-ongoing-monitoring flag on a PEP determination.
- Note: CBN PEP guidance expects a **database-driven** determination with
  source provenance — which is one more reason the vendor lane (fuzzy
  matching + PEP DB) is a purchase, not a table we maintain.

### 3.4 Reporting

| Obligation (source) | Control | Evidence |
|---|---|---|
| **CTR over the threshold** (NFIU CTR instruction; boundary configured) | CTR rows from audited movement data at `REPORT_CTR_THRESHOLD_KOBO` | `internal/reports/reports.go` `TypeCTR`; store query |
| **STR on suspicious activity** (MLPPA; NFIU STR instruction) | STR rows derived from confirmed monitor alerts + case status | `TypeSTR` rows carry alert rule/severity/status + case status |
| **PEP reporting** | PEP report rows from screening data | `TypePEP` |
| **goAML XML filing format** (NFIU goAML portal) | **Gap (G4)** — CSV/JSON rows, not the goAML XML schema | §5 |
| **Timelines: STR within 24h of suspicion; CTR by the 7th...** (NFIU instructions) | **Gap (G5)** — no filing-clock tracking or due-date surfacing | §5 |

**G4/G5 gap note:** `reports.ReportBundle` writes NFIU/CBN-shaped rows
(customer identity, reference, amount, counterparty, flag reason) — the
data contract is right; the *filing* mechanics (goAML XML, filing clock)
are what is missing.

### 3.5 Record-keeping and audit

| Obligation (source) | Control | Evidence |
|---|---|---|
| **Keep records ≥5 years** (MLPPA; Reg. 2022) | **Gap (G6)** — audit/transaction retention is configured via `store_retention.go` but the AML 5-year floor is not asserted anywhere | §5 |
| **Records sufficient to reconstruct transactions** (MLPPA) | Ledger, payments, alerts, cases, screening results, audit log — all persisted | Store schema |
| **Tamper-proof audit trail** (Baseline Std) | Audit log on every state change; tamper-evidence **Gap (G7)** — no hash-chaining/WORM store | §5 |
| **Annual independent validation** (Baseline Std) | **Gap (G8)** — not a code obligation; an operator duty to schedule | §5 |

### 3.6 Programme obligations (operator duties, listed for completeness)

MLRO appointment, staff training, internal reporting procedures, independent
audit — live outside the repo. The spec lists them so the compliance file is
whole; the only code-side hook is the **programme evidence check** in §7.

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
| **G1** | **PEP EDD + senior-management approval workflow** — screening produces the PEP list but no EDD workflow or SM approval step on a PEP determination | Reg. 2022 PEP obligations; CBN PEP guidance expects documented approval | A PEP hit opens a `pep_edd` case with an SM-approval disposition; `AdvanceKYCTier` refuses advancement while one is open |
| **G2** | **Payment-time counterparty screening + hold/interdiction** | Baseline Std automated interdiction; NIP is irreversible | Pre-flight screen in the `ReserveAllowance` window; confirmed → block, ambiguous → hold (allowance reserved, dispatch deferred); cache-on-beneficiary for cost |
| **G3** | **Internal watchlist** (owned list, no vendor) | Baseline Std names it; vendor record §8.2 already scoped the build | `internal_watchlist` table + enforcement in the screening lane + admin CRUD + audit |
| **G4** | **goAML XML filing format** | NFIU files via goAML; CSV rows do not file | `internal/reports` gains the goAML XML encoder beside CSV/JSON; schema-validated output |
| **G5** | **Filing-clock tracking** (STR 24h, CTR cadence) | NFIU timelines; missed clocks are reportable failures | Report rows gain due/completed timestamps surfaced in `/admin/reports` |
| **G6** | **AML 5-year retention floor** | MLPPA/Reg. 2022; current retention deletes sooner if misconfigured | `store_retention.go` refuses to delete within the AML floor; config validated ≥ 5y |
| **G7** | **Audit tamper-evidence** | Baseline Std "tamper-proof audit" | Hash-chain (`prev_hash` + row hash) or WORM storage on `audit_logs`; verification job |
| **G8** | **Annual independent validation evidence** | Baseline Std; operator duty | Checklist + evidence store in the admin console (document + date + scope) |
| **G9** | **Adverse-media + domestic-list screening** | Baseline Std names both; vendor lane | Closes with the vendor purchase — tracked in the vendor record §7, not re-specified here |

**Scheduling note:** G2, G3, G9 resolve in the vendor-record lane (§8 of the
vendor record). G1, G4–G8 are **owned builds** — small, severable, and each
converts directly into a repo-style commit. Nothing in the register blocks
another item; all are additive.

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
   independent validation scheduled. **Gap (G8)** is the code-side hook.
3. **Regulator-facing rehearsal** — `/admin/reports` must produce, on demand,
   an STR/CTR/PEP bundle a compliance officer would hand over. G4/G5 are
   what stand between the current bundle and a filing.

---

## 8. Roadmap alignment (what changes, where)

The Baseline Standards clock (24 months for OFIs) and the Trust Fabric
milestones align as follows:

| Wave | Items | Lane |
|---|---|---|
| **Now** | G3 internal watchlist (owned, vendor-independent) · G6 retention floor · G5 filing-clock fields | Owned builds — small commits |
| **Vendor track** | G2 payment screening + hold · G9 adverse media/domestic lists | Vendor record §7 trial → §8 integration |
| **Later** | G1 PEP EDD workflow (needs the vendor's PEP DB first to be meaningful) · G4 goAML XML · G7 tamper-evidence · G8 attestation hook | Owned builds after the vendor track |

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
- Vendor capability claims: `AML_VENDOR_DECISION_RECORD.md` §10 caveats
  apply unchanged here.

*Caveat: instrument summaries reflect the published texts and secondary
analyses available at drafting time; the final texts govern, and the
compliance owner should verify against the gazetted versions before filing
anything with a regulator.*
