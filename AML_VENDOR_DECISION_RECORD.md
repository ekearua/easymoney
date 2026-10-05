# AML screening vendor decision record

**Status:** Proposed — pending a paid trial (see §7). Not yet an approved purchase.
**Date:** 2026-09-29
**Decision owner:** Compliance + Platform (joint — this touches both the regulatory
programme and the payment path).

**In scope:** the vendor supplying sanctions / PEP / watchlist / adverse-media *screening
intelligence and matching* for two capabilities:

- **Customer screening** — at onboarding and on the periodic rescreen worker.
- **Payment screening** — inline, at payment-instruction time, before a payout is dispatched.

**Out of scope, deliberately:**

- Registry and identity verification (CAC, TIN, NIN/BVN, address) — a separate lane with its
  own ports; see §2.
- Transaction monitoring (velocity, structuring, round amounts, counterparty categories) —
  owned in-house.
- Risk scoring, banding, case management, audit — owned in-house.

---

## 1. Why this decision is being made now

The trigger is the CBN **Baseline Standards for Automated AML/CFT/CPF Solutions** (final
issue 10 March 2026; the May 2025 text was the exposure draft). As an OFI we fall inside a
**24-month implementation window**, with a roadmap due shortly after the issue date. The
standard names a set of capabilities that map onto this codebase as follows:

| CBN capability | Where it lands here |
|---|---|
| Domestic + international watchlists, real/near-real-time updates | **Vendor** (this decision) |
| Fuzzy and AI-based name matching, alias handling | **Vendor** |
| PEP databases | **Vendor** |
| Internal watchlists | **Owned** (our data, no vendor dependency) |
| Adverse-media monitoring | **Vendor** |
| Automated blocking / interdiction on confirmed matches | **Owned decisioning**, vendor intelligence |
| Documented match-resolution procedures, traceable dispositions | **Owned** (case files) |
| FP/FN thresholds, documented tuning decisions, change governance | **Owned** (tuning loop exists) |
| STR/SAR/CTR automated report generation | **Owned** (export scaffolding) |
| CDD/KYC/KYB context inside monitoring | **Owned** |
| Annual independent validation; tamper-proof audit; NDPA compliance | **Owned** + vendor audit terms |

The load-bearing reading: the CBN names *screening* and *monitoring* as distinct
capabilities. We already own monitoring. What we do not own is the **list data and the
fuzzy matching engine** — and now also the **inline payment-screening path**, which does not
exist at all today.

---

## 2. What we already own — and therefore must not buy

| Capability | Where it lives | Buy or own |
|---|---|---|
| Transaction monitoring | `kyc.RunTransactionMonitor` / `RunTransactionMonitorForInput` on every settled movement (payments, top-ups, standing credits, refunds, payouts) + 15-minute sweep | **Own** |
| Risk scoring & banding | `kyc.ScoreRisk`, decay under `RISK_DECAY_HALF_LIFE` (90d default), false-positive cancellation, network signals | **Own** |
| Interdiction by risk band | `RISK_HIGH_SINGLE_LIMIT_KOBO` caps a high band's single ceiling inside `ReserveAllowance` | **Own** |
| Case management & audit | `manual_review_cases`, `audit_logs`, `/admin/kyc`, trust timeline | **Own** |
| Screening **decisions** (clear / possible / strong) | `SanctionsScreener` port; `screening_results` (`provider`, `decision`, `matched_names`, `screened_at`, `rescreen_due`); rescreen worker on `KYC_RESCREEN_PERIOD` (90d) | **Own the decision**, buy the intelligence |
| Registry / identity verification | `CorporateRegistryVerifier`, `TaxIDVerifier`, `LocationVerifier` ports; `cac` / `firs` / `geo` adapters | **Own lane, separate vendor** |
| Internal watchlist | `internal_watchlist` (migration 080), enforced in `RecordScreeningResult`, reviewer CRUD at `/admin/internal-watchlist` | **Own** — built, no vendor |
| Approval blocking on a strong match | `AdvanceKYCTier` refuses on `strong`/`blocked` | **Own** |

**The buy/own boundary:** the vendor supplies *ranked hits with provenance*. We supply the
*threshold policy, the decision, the case file, and the tuning governance*. This is the whole
reason not to buy a vendor's monitoring or risk-assessment product — buying those would place
a second risk engine beside ours, disagreeing with it, and fork the source of truth we are
already obliged to document.

---

## 3. Requirements

| # | Requirement | Source |
|---|---|---|
| R1 | Customer screening at onboarding and on rescreen, mapped onto `clear`/`possible`/`strong` | Existing port; CBN watchlist + PEP |
| R2 | Payment screening, synchronous, at instruction time | CBN "automated interdiction on confirmed matches" |
| R3 | Hit-level detail: match score, entity type, source list, **list version** | CBN traceable dispositions |
| R4 | Configurable thresholds — our policy, not the vendor's verdict | CBN FP/FN governance |
| R5 | Adverse media | CBN, named explicitly |
| R6 | Nigerian domestic lists (see §9 open questions) | CBN "domestic watchlists" |
| R7 | Quality on Nigerian names — transliteration, name order, common surnames | Local operating reality |
| R8 | List/version provenance sufficient to answer "what did we know at 10:04" | CBN audit |
| R9 | Synchronous API with a predictable latency budget and defined degradation | Payment path is inline and irreversible on NIP |
| R10 | Commercial shape that scales with *our* volume, not an enterprise floor | Cost of goods on payment screening |

---

## 4. Options

### 4.1 Napier AI

Modern, API-first platform with three products matching our two needs plus the one we own:
**Client Screening**, **Transaction Screening** (real-time, built for payment-scale
throughput), and Transaction Monitoring. Fuzzy name matching across 20+ languages including
transliteration and name variants; client screening and transaction screening share one
platform, so one engine can serve both capabilities.

**Strength against our requirements:** R2 (its heritage — real-time transaction screening at
volume), R9. **Uncertain:** R6, R7 (Nigerian-specific depth), R10 (customer base skews to
banks and payment firms; likely a higher commercial floor than a self-serve API).

### 4.2 ComplyAdvantage

API-first AML data and screening platform (**Mesh**), covering **Customer Screening**,
**Adverse Media** (NLP-based), sanctions/PEP/watchlist across ~49 list types, and — since
**May 2026** — **Payment Screening** integrated into Mesh as a real-time capability.

**Strength against our requirements:** R1, R5 (the best adverse-media engine of the
API-first group), R10 (a self-serve entry tier exists, so a small-volume start is
commercially reachable), R3 (source metadata per hit). **Uncertain:** R2 — the payment
screening product is months old, so its inline behaviour under our latency budget is untested
rather than proven.

### 4.3 Firco / Actimize / FICO incumbents

The bank-grade inline filtering group: **LexisNexis Firco** (FircoSoft — the correspondent-
banking incumbent for real-time payment filtering plus formal list management),
**NICE Actimize** (Watch List Filtering, Actimize AML), **FICO Siron**, Oracle FCCM, SAS.

**Strength:** R2 at genuine scale, R3/R8 via dedicated list-management systems, regulator-
facing evidence. **Against us:** enterprise sales motion, annual licence at a
bank-scale floor, implementation measured in months. That cost is justified by throughput and
list-management requirements we do not have at our volume.

### 4.4 Adjacent, not primary

- **Data-only** — Dow Jones Risk & Compliance, LSEG World-Check. Best-in-class lists; viable
  *as the data layer behind our own engine*, but that means owning the matching engine, which
  is exactly the part we are choosing to buy.
- **Nigeria-local** — Smile ID, Youverify, Dojah, Prembly. Strong for the verification lane
  (§2) and possibly for domestic list depth, but **not a payment-filtering engine**. Do not
  serve R2 from here.
- **Challengers** — Hawk:AI, FinScan, Unit21. Worth a look if the two finalists fail a trial
  criterion; not worth a three-way bake-off now.

---

## 5. Comparison

Scored against §3. "Unknown" is an honest answer and is what the trial exists to resolve.

| Criterion | Napier AI | ComplyAdvantage | Firco / Actimize |
|---|---|---|---|
| R1 Customer screening | Strong | Strong | Strong |
| R2 Payment screening | **Strong** (heritage) | Promising, **new (May 2026)** | Strongest (at scale) |
| R3 Hit detail + list version | Strong | Strong | Strong + dedicated LSM |
| R4 Our thresholds own the decision | Yes | Yes | Yes — but often a whole configuration programme |
| R5 Adverse media | Adequate | **Strongest** | Adequate–strong |
| R6 Nigerian domestic lists | Unknown — trial | Unknown — trial | Likely via LSM uploads |
| R7 Nigerian name matching | Likely strong (20+ languages, transliteration) | Unknown — trial | Unknown — trial |
| R8 Audit provenance | Strong | Strong | Strongest |
| R9 Latency / inline behaviour | **Strong** | Unknown — trial | Strongest |
| R10 Commercial shape at our volume | Likely higher floor | **Most reachable** | Bank-scale floor |
| Integration effort | Weeks | Weeks | Months |

---

## 6. Recommendation

**Primary: ComplyAdvantage, contingent on its Payment Screening passing the §7 trial.**
**Pre-qualified alternative: Napier AI**, to be adopted if the payment-screening trial
fails.

The reasoning turns on what actually binds us. Payment screening is a hard requirement, but
**throughput is not our binding constraint** — we are a low-volume platform, so Napier's
real-time-at-scale advantage, real as it is, does not change the bill or the outcome here. At
this volume the deciding factors become match quality, adverse-media depth (a capability the
CBN names explicitly and where ComplyAdvantage is the strongest API-first option), domestic
list coverage, and whether we can start commercially without signing a bank-grade licence.
ComplyAdvantage wins on the last two by a wide margin and ties or leads on match quality for
customer screening.

**Service both capabilities from this one vendor.** A single engine means one score per name,
one threshold policy, one disposition vocabulary, and one case file — and it is strictly
better than running customer screening on one vendor and payment screening on another, which
would produce two different scores for the same beneficiary and an indefensible case record.

**Do not buy** the vendor's transaction monitoring or risk-assessment products, from any of
these vendors. Those are the parts of "owning the AML service" already built here.

**Keep the local vendor in the verification lane.** CAC/TIN/NIN/address verification stays on
the existing `CorporateRegistryVerifier` / `TaxIDVerifier` / `LocationVerifier` ports. That is
a different capability, which is why a second service there is legitimate rather than
redundant.

---

## 7. Trial criteria that would overturn the recommendation

The recommendation is falsifiable. Any one of these flips the decision to Napier AI; two or
more means stopping and re-opening the field (Hawk:AI, FinScan).

1. **Payment-screening latency.** If ComplyAdvantage cannot hold a p99 inside the payment
   budget (target ≤500 ms; absolute deadline ≤1 s) on a warm connection at our peak, it fails
   R9. Test with a load profile, not a single call.
2. **Degradation behaviour.** If a timeout or 5xx cannot be given a defined, configurable
   outcome (fail-closed outbound / flagged-open inbound), it fails R9 — an undefined failure
   mode on an irreversible rail is disqualifying regardless of the happy path.
3. **Hit-level detail.** If the payment product returns a verdict rather than hits with score,
   entity type, source list and list version, it fails R3/R4 and the architecture in §8 does
   not hold.
4. **Nigerian name matching.** A prepared set of realistic Nigerian names — Yoruba/Igbo/Hausa
   orderings, transliterations, single-token names, common surnames — must not produce
   unacceptable false positives at our threshold. Test the *false-positive* rate, because that
   is where both cost and operational burden live, not the true-positive rate.
5. **Domestic list coverage.** If it cannot carry the Nigerian domestic lists we ask about in
   §9, it fails R6 — and that alone is fatal for a CBN-scoped decision.
6. **Commercial floor.** If the entry contract cannot start at our volume without a
   bank-scale annual commitment, it fails R10, and Napier's commercial profile should then be
   compared on the same basis before either is signed.
7. **List freshness.** If list deltas do not arrive with a version identifier we can persist
   and reference, it fails R8.

---

## 8. Integration against the existing seams

The vendor slots in behind interfaces that already exist, so no caller changes:

1. **Customer screening** — a new adapter beside `internal/providers/screening/http.go`,
   mapping the vendor's ranked hits through our threshold policy into the port's
   `ScreeningDecision` (`Decision`, `MatchedNames`, `ProviderRef`). `screening_results` gains
   the list version alongside `provider`/`decision` so a disposition is defensible later.
2. **Internal watchlist** — owned, no vendor. Build the shell regardless of vendor choice; it
   is valuable on its own and is a CBN requirement neither vendor supplies.
3. **Payment screening** — *implemented (spec G2).* The counterparty screen runs in the same
   pre-flight window as `ReserveAllowance`, before `CreatePayout` dispatches to the rail:
   `payment_screenings` (migration 085) records every decision, a confirmed match **blocks**
   the payout and an ambiguous one **holds** it (new `held` status, migration 086 — allowance
   stays reserved) for disposition at `/admin/payment-screenings`; a vendor outage holds
   rather than clears. The vendor adapter from §8(1) replaces the simulated screener behind
   `ports.PaymentScreener` without touching callers.
4. **Cost lever** — screen beneficiaries when they are saved (`user_payout_destinations` now
   records them) and re-screen on list delta, so the at-payment call is largely a cache
   lookup keyed on (beneficiary, list version) rather than a paid call per payment.
5. **Rescreen** — the existing worker on `KYC_RESCREEN_PERIOD` is where customer screening
   becomes ongoing monitoring; it needs to re-evaluate open holds when a list version changes.
6. **Decisions stay ours** — `AdvanceKYCTier` continues to refuse on a strong match; the
   tuning loop (`TUNING_FP_PERCENT`, `TUNING_MIN_LABELLED`) extends to screening thresholds.

---

## 9. Consequences, risks, open questions

- **ComplyAdvantage's payment product is new.** This is the single largest risk in the
  recommendation and the reason it is contingent, not settled. The trial in §7 exists
  specifically to falsify it.
- **Domestic list coverage is unverified for both finalists.** The specific question to put to
  each vendor: the **Nigeria Sanctions Committee (NSC) list** under the Terrorism
  (Prevention and Prohibition) Act 2022, and any CBN/NFIU domestic designations, alongside
  OFAC/UN/EU/UK. Confirm coverage in writing before signing — this is R6 and it is fatal.
- **Vendor lock-in on the data.** Neither vendor sells its lists as a downloadable dataset;
  switching later means re-screening the whole book. The port keeps the code cost low; the
  data cost is not removable, so the contract should permit a bulk re-screen export.
- **Single-engine concentration.** One vendor serving both screening capabilities is a
  deliberate simplification, but it is also a single point of failure on the payment path.
  The §7(2) degradation policy is the mitigation and must be implemented, not assumed.
- **Cost shape is a volume question we cannot answer yet.** Per-search vs per-transaction
  pricing decides the steady-state bill once payment screening is live. Ask for both curves.
- **Open:** whether an OFI of our scale should carry the ~49-list-type breadth, or a tighter
  configured subset, and therefore what we are actually paying per month for.
- **Open:** whether the NSC list is better served by a local provider alongside the global
  vendor — a *data* decision, not a second screening engine, and it would not violate §6.

## 10. Sources and caveats

- CBN **Baseline Standards for Automated AML/CFT/CPF Solutions**, final issue 10 March 2026
  (May 2025 exposure draft preceded it); 24-month window for OFIs. Capability list in §1
  reflects this standard.
- Vendor capabilities above are drawn from published vendor material and third-party
  comparisons, **not live trials or executed contracts**. Latency, list coverage, match quality
  and pricing are exactly the things §7 tests.
- ComplyAdvantage's real-time Payment Screening on Mesh is dated from its May 2026
  announcement; treat its maturity as unproven until the trial.
