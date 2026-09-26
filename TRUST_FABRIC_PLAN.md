# Trust Fabric — phased implementation plan

Dojah's *The Trust Fabric* defines five layers with a strict dependency
chain — identity → signal → decision → network → intelligence — and argues
"the order is the architecture": each layer's output is the next layer's
input. This document maps that doctrine onto this codebase, records what
already exists per layer, and defines milestones M0–M5 with concrete
acceptance criteria. Status vocabulary: **exists** (in production code),
**partial** (some path wired), **missing**.

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

## Layer 2 — Signal ("what is happening around this identity?") — partial

- **Transaction signals — exists.** The rail-aware monitor scores every
  settled money movement (payments, top-ups, standing credits, refunds,
  payouts) against velocity, structuring-below-the-CTR-boundary, round
  amounts, and high-risk counterparty categories, with cross-rail history
  and idempotent dedupe.
- **Screening signals — exists** at onboarding and on the 90-day rescreen
  worker (`KYC_RESCREEN_PERIOD`).
- **Device/IP signals — missing.** Migration 026 created `trusted_devices`
  but nothing writes to it. Web sign-in and checkout capture no IP or user
  agent. This is the biggest blind spot: velocity rules see the *account*,
  never the *device*, so one actor rotating ten accounts is invisible.
- **Session/channel signals — partial.** Sessions are keyed per channel and
  channel linking is audited, but nothing scores "new device + high-value
  action".

## Layer 3 — Decision ("what action should the platform take?") — partial

- **Decisions that exist:** tier gates refuse payments above ceilings
  (`ReserveAllowance` validates single/daily/monthly against
  `kyc_tier_limits`); a blocking screening decision halts advancement and
  downgrades; medium/high monitoring alerts open a `monitoring` review case
  and feed the risk score.
- **The gap:** `kyc.ScoreRisk` computes a 0–100 score and a low/medium/high
  band and persists it on the KYC profile (`risk_score`, `risk_band`) — but
  **nothing reads the band back to change behaviour**. The score is
  display-only. The doctrine's decision layer (approve / block / escalate /
  step-up / limit) has exactly one wired action (review case) and one
  unwired instrument (the band).

## Layer 4 — Network ("how is this entity connected to others?") — missing

No entity resolution, no graph. What partially compensates: the cross-rail
monitor correlates *movements* per user; channel linking correlates
*accounts* (but only when the customer declares them); the structuring rule
detects split patterns for one identity. Cross-identity patterns — many
accounts sharing a device, a bank account receiving from structuring
clusters — are undetectable until devices (M2) and recipient links (future)
exist.

## Layer 5 — Intelligence ("how does the system learn?") — missing

Every ingredient exists as data: alerts with reviewer dispositions
(`ResolveTransactionAlert`), risk events with timestamps and scores, a
kyc audit log, false-positive-prone rules with thresholds in config. What
is missing is the loop: a resolved alert records no outcome label, no
threshold was ever tuned from outcomes, and no dashboard answers "are the
rules getting better?". The doctrine's warning — *episodic verification
produces episodic intelligence* — applies: without outcome labels captured
now, any future model work has no training data.

---

## Milestones

### M0 — Foundations and truth (mostly done)

- **M0.1 (done, `932bf95`)** — provider env template truthful, pinned by a
  load test; retired-rail cases purged from docs and workbooks.
- **M0.2 (this wave)** — the plan itself versioned (this file); decision
  knobs inventoried in one place so M3 has a surface to act on:
  `kyc_tier_limits` (ceilings), `REPORT_CTR_THRESHOLD_KOBO` (structuring
  boundary), `MONITOR_*` (rule thresholds), `risk_band` (the unused
  instrument).
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

### M2 — Device signals: make `trusted_devices` real

Fingerprint web sessions (stable hash of UA + accepted-language class at
M1; upgraded to real device attestation later), record/touch rows on
sign-in and checkout, and surface the device list on the KYC profile.

- **Acceptance:** repeated visits upsert one row (`UNIQUE (user_id,
  device_fingerprint)` honoured); `last_seen_at` advances; profile view
  shows devices; integration test covers first-touch and repeat-touch.

### M3 — Decision: make the risk band bite

`risk_band` becomes an input: a **high** band caps the individual
money-in/money-out single ceiling (config knob
`RISK_HIGH_SINGLE_LIMIT_KOBO`, default the L1 single ceiling) until a
review case clears; **medium** keeps current limits but is surfaced on the
admin profile. The cap joins `ReserveAllowance`'s existing tier validation
— one more ceiling check in the same audited transaction.

- **Acceptance:** a high-band user is capped at the configured ceiling
  regardless of tier; clearing the review (recompute with clear evidence)
  restores the tier ceiling; store tests cover cap, restore, and
  replay-idempotency; the admin profile view shows the active ceiling.

### M4 — Feedback: outcomes become first-class data

Resolving a monitoring alert records the disposition as an outcome row
(`false_positive` / `confirmed` / `undetermined` via the existing resolve
statuses: acknowledged→undetermined, escalated→confirmed, resolved→
reviewer-chosen). Outcomes are visible as counts per rule on the
compliance queue, so threshold tuning has evidence.

- **Acceptance:** every terminal alert status writes exactly one outcome
  row; the queue shows per-rule outcome counts; integration test covers
  the three statuses.

### M5 — Intelligence: the doctrine's KPIs, computed

The admin metrics dashboard gains the doc's own KPI section, computed from
existing tables: false-positive rate by rule (M4 outcomes), median time
from alert to resolution, signal coverage (share of movements arriving
through the monitored rails), and risk-band distribution. No learning
system yet — the point is that when one is built, its labels and baseline
metrics already exist.

- **Acceptance:** the dashboard renders the four KPIs from seeded data;
  browser smoke proves the page; the numbers reconcile with the
  underlying tables in an integration test.

---

## Explicitly out of scope for now

- **Network layer (graph/entity resolution).** Gated on M2 device data
  accumulating and on a real multi-identity fraud pattern to justify the
  schema. The doctrine's own "consortium" layer is a governance project,
  not a feature.
- **Vendor signal breadth** (behavioural biometrics, phone/email
  reputation). The doctrine's "signal breadth beats model sophistication"
  is agreed, but each vendor adds a data-processing agreement; revisit
  after M3/M4 prove the decision loop.
- **ML scoring.** Blocked on M4 outcome volume; deterministic
  `ScoreRisk` weights stay until labels exist.
