# Trust Fabric — phased implementation plan

Dojah's *The Trust Fabric* defines five layers with a strict dependency
chain — identity → signal → decision → network → intelligence — and argues
"the order is the architecture": each layer's output is the next layer's
input. This document maps that doctrine onto this codebase, records what
already exists per layer, and defines milestones M0–M5 with concrete
acceptance criteria. Status vocabulary: **exists** (in production code),
**partial** (some path wired), **missing**.

**Compliance mapping:** the regulatory reading of these layers — the CBN
AML/CFT/CPF obligations each layer satisfies and the gap register — lives in
`CBN_AML_COMPLIANCE_SPEC.md`; the screening-vendor decision lives in
`AML_VENDOR_DECISION_RECORD.md`. This file stays the architecture plan; the
spec stays the obligation map. Keep the two in sync when a wave moves both.

The doctrine's calendar cadence (0–3 / 3–6 / 6–12 / 12–24 months) maps to
M0–M1 / M2–M3 / M4 / M5 here — but the milestones are ordered by dependency,
not dates: a layer's milestone only starts when the layer below it exists.

---

## Layer 1 — Identity ("who is this entity?") — exists

The strongest layer in the repo; the CBN ladder is the anchor every other
layer joins on.

- CBN three-tier ladder L0–L3 (individuals) and B0–B3 (businesses) enforced
  in the store with adjacency + evidence checks: `AdvanceKYCTier`,
  `DowngradeKYCTier`, migration 073's realigned `kyc_tier_limits`.
- Evidence keys: `tier1_basic_details`, `government_photo_id`,
  `bvn_verified` + `nin_verified` (both required for L2, via the
  ninbvnportal adapter), `address_proof` + `edd_completed` for L3.
- Sanctions/PEP screening with a blocking decision halting advancement and
  downgrading the profile; KYB corporate gates (CAC, TIN, UBO, location,
  SCUML) for B0→B1; `approved_simulated` on the Tier-1 profile remains the
  known C9 remediation item.
- Cross-channel identity carry-over: a customer approved on one channel is
  never re-onboarded on another; account linking merges channel rows into
  one surviving primary (`store_link_accounts.go`).

## Layer 2 — Signal ("what is happening around this identity?") — exists

- **Transaction signals — exists.** The rail-aware monitor scores every
  settled money movement (payments, top-ups, standing credits, refunds,
  payouts) against velocity, structuring-below-the-CTR-boundary, round
  amounts, and high-risk counterparty categories, with cross-rail history
  and idempotent dedupe.
- **Screening signals — exists** at onboarding and on the 90-day rescreen
  worker (`KYC_RESCREEN_PERIOD`).
- **Device signals — exists** (was the biggest blind spot). Web sign-in and
  checkout capture request context; `trusted_devices` is touched on every
  sign-in/checkout, the KYC profile lists devices, and shared-device
  co-usage feeds the risk score symmetrically for every identity on the
  fingerprint (`RiskEventSharedDevice`, capped at 45).
- **Session/channel signals — partial.** Sessions are keyed per channel and
  channel linking is audited; "new device + high-value action" scoring is
  still not wired.

## Layer 3 — Decision ("what action should the platform take?") — exists

- **Decisions wired:** tier gates refuse payments above ceilings
  (`ReserveAllowance` validates single/daily/monthly against
  `kyc_tier_limits`); a blocking screening decision halts advancement and
  downgrades; medium/high monitoring alerts open a `monitoring` review case
  and feed the risk score.
- **The band bites (was the gap):** `kyc.ScoreRisk` computes the 0–100 score
  with **decay** (`RISK_DECAY_HALF_LIFE`, 90d default) and **false-positive
  cancellation** (a dismissed alert cancels exactly its dated weight), and a
  **high** band caps the single ceiling at `RISK_HIGH_SINGLE_LIMIT_KOBO`
  inside `ReserveAllowance` until a review clears. Alert outcomes are
  first-class data (false_positive/confirmed/undetermined) feeding the
  tuning loop (`TUNING_FP_PERCENT`, `TUNING_MIN_LABELLED`).
- **Still unwired:** step-up authentication as a band action; screening
  hold/interdiction at payment time (spec G2).

## Layer 4 — Network ("how is this entity connected to others?") — partial

- **Entity-link analysis — exists.** Network edges over shared trusted
  devices, shared payout/recipient bank accounts (`user_payout_destinations`,
  written by the individual-pay and payout rails), and channel identities;
  ring detection with **weighted edges** (sightings + recency) and a triage
  queue at `/admin/network` showing member risk bands and tiers.
- **Ring judgements — exists.** A confirmed alert raises one linked case
  across all ring members (`EnsureNetworkCases` fires on the alert-resolution
  path, not just on admin load); resolving as false positive cancels each
  member's ring weight exactly; confirmed keeps the weight and freezes
  allowances in one action. Ring membership feeds the risk score
  (`RiskEventRingMember`, capped at 70; zero without a confirmed hit).
- **Still missing:** cross-institution/consortium federation (governance
  project, out of scope below).## Layer 5 — Intelligence ("how does the system learn?") — partial

- **Outcome loop — exists (was missing).** Every terminal alert status
  records exactly one outcome row; per-rule false-positive counts feed the
  tuning suggestion; the 7-day FP rate drives tuning suggestions; risk
  events carry timestamps, decay, and cancellation provenance — so the
  labels a future model needs are already accumulating.
- **KPIs — partial:** signal coverage and risk-band distribution are
  computed on the admin dashboard; median alert-resolution time and the
  doc's full KPI section are the remaining M5 items.
- **ML scoring — still missing,** deliberately: blocked on M4 outcome
  volume; deterministic `ScoreRisk` weights stay until labels exist.

---

## Milestones

### M0 — Foundations and truth — done

- **M0.1 (done, `932bf95`)** — provider env template truthful, pinned by a
  load test; retired-rail cases purged from docs and workbooks.
- **M0.2 (done)** — the plan versioned; decision knobs inventoried
  (`kyc_tier_limits`, `REPORT_CTR_THRESHOLD_KOBO`, `MONITOR_*`, `risk_band`).
- **M0.3 (done)** — the CBN obligation→control mapping and gap register
  versioned as `CBN_AML_COMPLIANCE_SPEC.md`.
- **Acceptance:** a reviewer can read this file, open each named symbol,
  and find the code as described. CI keeps docs honest via the existing
  template-load and coverage drift guards.

### M1 — Capture the signals that already pass through the server

Trust-relevant boundaries (web sign-in, checkout pages, web-flow submits)
log the client IP (honouring `X-Forwarded-For` behind the tunnel) and user
agent into one audited place, so device intelligence later has history
instead of starting empty.

- **Acceptance:** a sign-in and a checkout submit persist request context;
  the admin can see it on the KYC profile view; no PII beyond IP/UA;
  unit + integration coverage.

### M2 — Device signals: make `trusted_devices` real — done

Fingerprinting, touch-on-sign-in/checkout, profile device list, and the
shared-device risk signal all shipped (see Layer 2); migration 079 added the
fingerprint index the lookups needed.

- **Acceptance (met):** repeated visits upsert one row; `last_seen_at`
  advances; profile view shows devices; integration tests cover
  first-touch, repeat-touch, and the symmetric co-user risk emission.

### M3 — Decision: make the risk band bite — done

The high band caps the single ceiling (`RISK_HIGH_SINGLE_LIMIT_KOBO`) inside
`ReserveAllowance` until a review clears; the decay/cancellation machinery
(`8e8341d`) and the network signals (`c547db0`) landed on top.

- **Acceptance (met):** cap, restore, and replay-idempotency covered by
  store tests; the admin profile shows the active ceiling; false-positive
  cancellation returns the score to baseline exactly.

### M4 — Feedback: outcomes become first-class data — done

Terminal alert statuses record outcome rows
(`false_positive` / `confirmed` / `undetermined`), the compliance queue
shows per-rule counts, and the 7-day FP-rate crossing a threshold surfaces
a tuning suggestion in the queue.

- **Acceptance (met):** exactly one outcome row per terminal status;
  per-rule counts on the queue; integration tests cover the statuses and
  the suggestion path.

### M5 — Intelligence: the doctrine's KPIs, computed — partial

False-positive rate by rule, signal coverage, and risk-band distribution
are on the admin dashboard; **median alert-resolution time and the full KPI
section remain**, plus the trust timeline already shipped on the KYC
profile (alerts + device sightings + audit entries in one feed).

- **Acceptance (remaining):** dashboard renders the full KPI set from
  seeded data; the numbers reconcile with the underlying tables in an
  integration test.

---

## Explicitly out of scope for now

- **Cross-institution/consortium federation.** The doctrine's own
  "consortium" layer is a governance project, not a feature; the intra-
  platform graph now exists (Layer 4).
- **Vendor signal breadth** (behavioural biometrics, phone/email
  reputation). Each vendor adds a data-processing agreement; revisit after
  the screening-vendor decision (`AML_VENDOR_DECISION_RECORD.md`) lands.
- **ML scoring.** Blocked on M4 outcome volume; deterministic
  `ScoreRisk` weights stay until labels exist.
- **Screening hold/interdiction at payment time** (spec G2) and the other
  spec gaps — tracked in `CBN_AML_COMPLIANCE_SPEC.md` §5, not duplicated
  here.
