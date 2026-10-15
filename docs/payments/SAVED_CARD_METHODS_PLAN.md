# Saved Card Methods — Implementation Plan

**Status:** Phase 0 enquiry **drafted and not yet sent** (send it before deciding anything);
Phase 0.5 measurement **built and run — returned "insufficient data"**; Phase 1 preference layer
**shipped**. **Phase 2 is still gated** on the §4 vendor answers. Revision 2 — see §0.
**Owner:** platform
**Migrations:** Phase 1 → `102` (applied); Phase 2 (gated) → `103`
Related: `docs/payments/CARD_ON_FILE_DECISION.md` (the decision record — nine vendor questions, postures and
PCI scope), `docs/compliance/CBN_AML_COMPLIANCE_SPEC.md` (G2 payment screening, G6 retention, G11 identity keys),
`docs/compliance/TRUST_FABRIC_PLAN.md` (device/ring signals), `docs/compliance/CBN_DATA_LOCALITY_GAP.md` (G16 in-country data),
`docs/runbooks/GO_LIVE_RUNBOOK.md` (deploy + PCI posture), `docs/cost-models/EXTERNAL_SERVICE_COST_MODEL.md`

---

## 0. What this revision changes

Revision 1 proposed adding a `ProviderCardOnFile` rail constant. Reading the code showed that
would have shipped two defects, so the design is corrected here:

1. **A new rail would have silently charged zero platform fee.** `FeeChannelForProvider`
   (`internal/service/fees.go:53`) ends in `default: return provider`, and `XegoCollectionFee`
   returns `FeeResult{FeeKobo: 0}` for an unknown channel — a `"card_on_file"` channel misses the
   switch and bills nothing. The rail allow-lists at `internal/service/payments.go:241` and `:500`
   would have rejected the payment before it got that far.
2. **Therefore: an *authorisation dimension*, not a new rail** (§3). `provider` stays
   `interswitch`; the difference is recorded on `payments.authorised_via`.
3. **The token charge's confirmation path is an open question, not an assumption.** Revision 1
   asserted it "funnels through the same authority" as a hosted attempt. Nothing establishes
   that; it is now vendor question **Q8**.
4. **3-D Secure treatment is now a value gate.** If a stored-token charge challenges the payer
   every time, B does not deliver one-tap and the PCI scope change is unjustified — vendor
   question **Q9**.
5. **New Phase 0.5: measure before spending**, with "build nothing" as an explicit outcome (§5).
6. **Phase 1 no longer pre-creates an empty `payment_instruments` table.** Schema ships with the
   feature that populates it.
7. **Phase 2 gains** a Tier 1 enrollment gate, a decline-fallback decision and a log-hygiene
   requirement; **Phase 3 gains** chargeback evidence and a kill switch.
8. **`docs/payments/CARD_ON_FILE_DECISION.md` is edited to match** — nine questions, not seven.

---

## 1. What "saving a card" can mean here

Three different products hide behind one phrase, and they cost very different amounts of
compliance work. The plan must not blur them:

| # | What it is | What we store | Card data touches us? | PCI scope |
|---|---|---|---|---|
| **A** | **Remembered preference** — "I pay merchants by card, remember that" | the *choice* (rail + scope), never the card | No | unchanged (SAQ-A) |
| **B** | **Card on file (token)** — one-tap pay with a stored token | gateway `token`, `panLast4`, brand, expiry, consent | Only via the gateway's tokenization call | **changes** — see §4 |
| **C** | **Recurring / mandated debit** — unattended charges on a schedule | same as B plus a mandate and debit notices | as B | as B plus conduct rules |

This plan builds **A now**, **B behind vendor answers and a measurement** (§4 / Phase 0 + 0.5),
and treats **C as a separate, later product** with its own mandate, pre-notification and
cancellation requirements — it is explicitly *not* part of B.

And it keeps a fourth option on the table: **Option 0 — build neither**, and optimise the
existing hosted entry. Phase 0.5 exists to decide whether A and B earn their cost at all.

---

## 2. What exists today (grounded)

**The card rail.** Interswitch only — `PAYMENT_PROVIDER=interswitch`. Two render modes
(`INTERSWITCH_CHECKOUT_RENDER`):

- `hosted_fields` (default) — `InterswitchHostedFieldsPage` mounts the Hosted Fields SDK;
  card number/expiry/CVV/PIN/OTP live in Interswitch-hosted iframes. The platform page
  (`internal/app/checkout.go` → `hosted_fields_checkout.html`) only carries the SDK config
  island and a scoped CSP. **This is why the platform currently holds no card data at all.**
- `legacy` — a host-posted redirect form to `newwebpay`.

**Authority for an outcome.** For a *hosted* attempt: webhook (`ValidateWebhook`, HMAC-SHA512)
plus requery (`Verify` → `gettransaction.json`), applied by `VerifyAndApply`. The requery branch
returns `Channel: "card"` and a `CardNumber` field the platform **discards today** — nothing
card-derived is persisted. Whether the *same* two paths cover a stored-token charge is Q8, and
this plan does not assume it.

**Storage.** `payments` (migration 001) has no card columns: `provider`,
`provider_reference`, `checkout_url`, `receipt_token`, `status`, `paid_at`. No
`payment_instruments`, no `payment_preferences`, no tokenization code anywhere in the tree.

**Rails are plain constants** threaded through every surface: `ProviderInterswitch` (card),
`ProviderBankTransfer` (DVA), `ProviderWallet`. Every place a rail string is switched on — this
is the blast radius §3 exists to avoid:

| Site | What it does with the rail |
|---|---|
| `internal/service/fees.go` — `XegoCollectionFee` | picks fee parameters by *channel*; unknown channel → **zero fee** |
| `internal/service/fees.go` — `FeeChannelForProvider` | maps rail → fee channel; `default:` returns the rail unchanged |
| `internal/service/payments.go:241` | collection-draft rail allow-list (rejects unknown rails) |
| `internal/service/payments.go:500` | invoice/checkout rail allow-list |
| transaction monitoring | tags alerts with the rails they fired over (`Details["rails"]`) |
| admin transaction-alert queue | rail filter + reference column added for triage |

**Method-picker call sites** a "saved" option has to reach:

- chat: `conversation_payment.go` (~442/460), `conversation_invoice.go` (~705/722),
  `conversation_thrift.go` (~563/579), `conversation_wallet.go` (53/55), `conversation_data.go`
- web flow: `wfMethodButtons` (`internal/app/webflow_money.go`), invoice page (`invoice.html`),
  request-money link (`link.html`), hosted checkout (`checkout.html`)

**Reusable seams (do not rebuild these):**

- AES-256-GCM application-level encryption at rest — `Store.SetDataKey` / `sealValue`
  (`internal/store/store.go`)
- `store_dsr.go` — `consent_records` (029), `ExportUserData`, erasure
- `store_retention.go` — the AML 5-year floor (spec G6)
- `store_identity_keys.go` — the "refuse + open a review case" pattern (spec G11)
- `payment_screenings` + the G2 seam (`ScreenPayout`, `store_payment_screening.go:211`)
- `review_cases` + admin compliance queue, `store_audit_chain.go`, `internal/app/mfa.go`,
  `store_devices.go` (trusted devices), the outbox

---

## 3. Design decision: an authorisation dimension, not a new rail

**A saved-card payment is a card payment over Interswitch.** The rail is unchanged; only *how it
was authorised* differs. So:

- `payments.authorised_via` ∈ `hosted_entry | stored_token` (default `hosted_entry`)
- `payments.instrument_id` — nullable FK, set only on a stored-token charge
- `provider` stays `interswitch`; `channel` stays `"card"`

What this buys, concretely:

| Surface | Effect of this design |
|---|---|
| `XegoCollectionFee` / `FeeChannelForProvider` → `"card"` | unchanged — fee correct **by construction**; defect 1 is impossible |
| `payments.go:241` / `:500` allow-lists | unchanged |
| Monitoring rail tags, admin rail filter | unchanged — no new rail string to register |
| Settlement, screening, receipt, reconciliation, existing reports | unchanged |

**Rejected alternative:** a `ProviderCardOnFile` constant. It reads well and reports nicely, but
it means touching every site in §2's blast-radius table, and its failure mode is a *silent*
zero-fee transaction rather than a compile error. If we ever do add a rail, that table is the
checklist, and it must ship with a table-driven test asserting **every declared rail resolves to
a non-zero fee channel** — the guard that would have caught this.

**Rails are not authorisation methods.** Conflating them is what made defect 1 likely; keeping
them separate is cheaper *and* more honest.

---

## 4. The decision that gates Phase 2 (resolve before writing any token code)

Interswitch's own documentation:

- Saving a card and charging it later are described as **tokenization**: set `token=1` in the
  transaction request's `authData`, and the gateway "will securely save the customer's card
  number in [its] PCI DSS and PCI PIN certified environment and return a unique token";
  `token` + `panLast4Digits` come back. Up to **5 saved cards** per customer.
- The API route is explicit: tokenize with `POST /api/v2/purchases/validations/recurrents`
  (`transactionRef`, `AuthData` = *encrypted card details*, Bearer token), then charge with
  `POST /api/v2/purchases/recurrents` (`customerId`, `amount`, `currency=NGN`, `token`,
  `tokenExpiryDate`, `transferRef`).
- And: **"Recurring payments on Interswitch Payment Gateway are only supported on Merchant
  Hosted (API) integrations."**

That last line is the whole problem. Our integration is *not* merchant-hosted — it is hosted
fields, chosen so the PAN never reaches us. Tokenization through the published API path means
our own page or client-side JS handles the PAN before encryption (`AuthData` is generated
client-side), moving the platform from **SAQ-A to SAQ-A-EP/SAQ-D**: self-assessment plus ASV
scans, segmentation evidence, a pen test, and an honest statement in the runbook and evidence
pack. That is a real, named cost — not a footnote — and it must be a *chosen* posture.

**Postures:**

- **A — preference only.** Zero new PCI scope. Delivers "don't make me choose again"; does not
  deliver one-tap. Cheap, and the schema the rest stands on.
- **B — Interswitch tokenization.** Real one-tap and the on-ramp to recurring — *conditional on
  Q9*: if every stored-token charge challenges the payer through 3-D Secure, B is not one-tap
  and the scope change is not worth it.
- **B′ — B via a hosted tokenize flow** (Q2). If the PAN stays inside Interswitch's frame, the
  scope change largely disappears and B becomes clearly worth building.
- **C — external PCI-1 vault.** More vendors, same locality question, no benefit over B for a
  single-gateway stack. Fallback only.
- **Option 0 — build neither.** Optimise the hosted entry path. Viable whenever Phase 0.5 shows
  re-entry friction is not material.

**Recommendation:** run Phase 0 and Phase 0.5 before committing engineering. Build Phase 1 only
if the measurement justifies it; commit to B (or B′) only on written answers.

---

## 5. Phases and milestones

### Phase 0 — Vendor answers (2–3 days, parallel; nine questions)

The full enquiry is Appendix A of `docs/payments/CARD_ON_FILE_DECISION.md`; the answers are recorded in its §3.

1. Is **tokenization** enabled on **our** merchant profile and pay item?
2. Is there a **hosted / iframe tokenize** flow — PAN stays in Interswitch's frame? *(highest value)*
3. What exactly is `AuthData`, and where does that code run?
4. Token **TTL / `tokenExpiryDate`** semantics; behaviour on card expiry or reissue.
5. Does tokenization work in **TEST**, with which test cards?
6. `panLast4Digits` format, and is the **card brand** returned?
7. Can a saved-card charge fund a non-card payment (wallet top-up, DVA-funded)?
8. **How is a `purchases/recurrents` outcome confirmed** — webhook, `gettransaction.json`
   requery, or a separate status endpoint? *(validates the Phase 2 charge path)*
9. Does a stored-token charge trigger a **3-D Secure challenge**, and can merchant-initiated
   transaction exemptions apply? *(decides whether B delivers one-tap at all)*

**Done when:** every gate answer (Q1, 2, 3, 5, 8, 9) is in writing, recorded in the decision
record with its source and date, and §11 of that record states the posture and the PCI scope
line.

---

### Phase 0.5 — Measure before spending (~2 days) — **new**

B costs a PCI scope change; A costs little and delivers little. Neither should be built on
assumption, so measure first:

- Returning-payer abandonment on the card page (how many re-entries are abandoned).
- Share of repeat purchases that are for the same merchant (the case a merchant-scoped
  preference, or a stored token, actually addresses).
- Support contacts attributable to re-entering card details.

**Done when:** the numbers are recorded in this plan (or the decision record) and the outcome is
one of: build A, build A+B, or **Option 0 — build neither**, with the reason written down.

**Measured 2026-10-12 — outcome: insufficient data to decide.** The query and its test are in
place (`Store.CardReentryMetricsSince`, `store_payment_preferences_integration_test.go`), and it
was run against the live development database:

| Figure | 30-day window | Lifetime |
|---|---|---|
| Payments | 0 | 3 (1 payer, 2026-09-11) |
| Card attempts | 0 | 3 |
| Card attempts that did not complete | 0 | 0 |
| Payments from returning payers | 0 | 0 |

Every lifetime payment is still `awaiting_confirmation` (2) or `pending` (1): the card rail has
never yet completed a payment here, so there is no abandonment to measure and no returning payer
to serve.

That is a real answer, not a non-answer. **It rules out spending the PCI scope change now**:
Phase 2's value is unproven and unprovable at this volume, and Option 0 is indistinguishable from
B until the funnel has traffic. Phase 1 costs nothing to carry — it touches no card data — so it
stays as the substrate, and the measurement should be re-run once the card rail has completed a
meaningful number of attempts.

**Both remaining figures are now built (2026-10-12) and surfaced on `/admin/metrics`** as the
"Saved-method evidence" panel (`buildReentryPanel`, `admin_reentry.go` + `metrics.html`):

- **Repeat-purchase share per merchant** — `Store.CardReentryMetricsByMerchant`, rendered as a
  breakdown ordered by repeat-payer activity, so the question becomes *which* merchant is losing
  payers to re-entry rather than only whether anyone is.
- **Support contacts** — `CardReentryMetrics.CardFollowUps`: the number of distinct payers who
  messaged us within 30 minutes of one of their own card attempts failing. This is a **proxy, and
  the panel says so**, because a follow-up may be about anything and a payer who gives up in
  silence is invisible to it.

The proxy is built the way it is on purpose, and the reasons are worth keeping. It reads
**metadata only** — `users.whatsapp_number` joined to `inbound_messages.sender`, with the
attempt's `updated_at` against `received_at`. It never reads `inbound_messages.payload`, which is
sealed at rest (`enc:v1:`), so a content scan would be unreliable *and* the wrong thing to build:
a metrics query has no business reading customer messages, and any classification of them would
be personal data needing its own retention and DSR handling. It therefore **undercounts by
construction**, which is the safe direction to err for a number that would be used to justify
spending money.

**The panel states its own reading rules rather than implying confidence.** It refuses to call a
rate below 30 card attempts, and treats ≥15% of attempts not completing as material — both are
stated conventions (`reentryMinCardAttempts`, `reentryMaterialAbandonment`), not measured facts,
and the panel names the numbers it used so the argument can be had about the threshold instead of
about the arithmetic. Every branch of the reading names the sample it rests on.

**Still a named gap:** a true support-contact count needs a channel that records *why* someone
made contact. No ticket system exists, so the panel proxies it. If support capture is ever built,
this figure should be replaced rather than kept alongside it.

---

### Phase 1 — Preference layer (posture-neutral, ~1 week) — **implemented**

Ships value with **no card storage at all**, and is what B builds on.

**Shipped:** migration `102_payment_preferences.sql`; `Store.RememberPaymentRail` /
`PaymentPreferenceFor` / `UpsertPaymentPreference` / `ClearPaymentPreference` /
`PaymentPreferencesForUser` (`store_payment_preferences.go`); the service helpers
`SavedRail`, `RememberRail`, `OrderRailsByPreference`, `SavedRailNotice`,
`PaymentPreferenceChannel` and the declared-rail set (`payment_preferences.go`); the post-success
remember hook in `VerifyAndApply`; the chat picker orders its buttons and names the habit in the
body; the one-page web pickers order their buttons.

**One deviation, recorded deliberately:** the web surface reorders and does **not** relabel.
`webflow-onepage.js` captures each fee-bearing button's label at load and rewrites `textContent`
from it on every amount keystroke, so a server-side marker would be silently replaced the moment
the customer touched the amount — a label that vanishes is worse than no label. Ordering survives
that rewrite, and the chat surface, where we own the message body, carries the written
acknowledgement. Revisit if the JS gains a marker-aware path.

**Migration `102_payment_preferences.sql`** — preferences only. No instrument table: a table the
plan forbids populating is dead weight and a migration we would have to alter later.

- `payment_preferences` — `id`, `user_id`, `channel` (`whatsapp | web`), `scope`
  (`default | merchant`), `scope_ref` (merchant uuid, empty for `default`), `rail`,
  `created_at`, `updated_at`; unique on `(user_id, channel, scope, scope_ref)`; `user_id`
  `ON DELETE CASCADE`.
- Holds **no card data**, and no `invoice` scope — nobody repeat-pays an invoice.

**Store** (`internal/store/store_payment_preferences.go`)

- `UpsertPaymentPreference`, `PaymentPreferenceFor(userID, channel, scopes…)`, `ClearPaymentPreference`
- Rail values validated against the known set at the boundary, so a typo cannot become an
  unknown-channel zero-fee payment later.

**Service + surfaces**

- A single `savedMethods` helper returns the picker block so chat, web flow, invoice, link and
  transfer pages cannot drift in wording.
- Chat + web method picker pre-selects the remembered rail. Wording must not over-promise: this
  does **not** pre-fill card details.
- On a successful card payment, remember the choice for that merchant and as the default.

**Done when:** a returning payer's chosen rail is remembered across chat and the browser for the
same merchant; an explicit re-choice overrides it; `gofmt`/`build`/`vet` clean; the new table
holds nothing card-shaped; existing rail tests pass unchanged.

**Verified:** `store_payment_preferences_integration_test.go` pins the lifecycle on a real database
(remember → resolve → merchant beats default → per-surface isolation → upsert not append → clear
one scope → erasure cascades) and the Phase 0.5 figures; `payment_preferences_test.go` pins
ordering, the surface mapping, the notice copy (it must not imply a stored card) and rail
classification. Existing rail tests are unchanged.

---

### Phase 2 — Card on file (gated on Phase 0 + 0.5, ~1–2 weeks)

**Migration `103_payment_instruments.sql`**

- `payment_instruments` — `id`, `user_id` (CASCADE), `provider`, `kind`, `token_enc`,
  `pan_last4`, `brand`, `exp_month`, `exp_year`, `status` (`active|expired|revoked`),
  `consent_id`, `enrolled_channel`, `created_at`, `last_used_at`, `revoked_at`; unique on
  `(user_id, provider, token_enc)`. `token_enc` is the only secret column, written via
  `sealValue`. **Never** a `card_number` / `pan` / `cvv` / `pin` column (§6 guard).
- `payments.authorised_via` (`default 'hosted_entry'`) and `payments.instrument_id`
  (nullable FK) per §3.

**Port** — `internal/ports/ports.go`, in the existing verifier style:

```go
type CardTokenizer interface {
    Enroll(ctx context.Context, req EnrollRequest) (Instrument, error)
    Charge(ctx context.Context, req TokenCharge) (TokenChargeResult, error)
    Revoke(ctx context.Context, token string) error
}
```

**Providers** — `internal/providers/interswitch/cardtoken.go` (beside `token.go`, not inside it,
so the two token concepts stay distinct), plus a simulated default mirroring
`providers/paymentscreening/simulated.go`, so tests and local demos run hermetically. New knobs
go in `internal/config/config.go` + `.env.example` (truthful template, per the M0.1 rule),
including `CARD_ON_FILE_ENABLED`.

**Charge path** — `InitializeCheckout` branches on `authorised_via`: instead of returning a
hosted-page URL it performs the token charge. The outcome then reaches the **existing** status
machine, ledger, receipt and settlement paths — subject to Q8, which decides whether that is via
webhook, requery, or a new status call. A token charge is not a new way to be paid, only a new
way to be *attempted*.

**Eligibility gate** — enrollment requires an **active wallet / Tier 1**, mirroring the existing
wallet rule, so an unverified user cannot park a chargeable instrument.

**Enrollment UX** — after a successful card payment, a consent-first "Save this card for next
time?" step. Under B′ the PAN never enters our page; under B-via-API it requires the encrypted
`AuthData` flow and the PCI scope change. Enrollment is never automatic.

**Decline fallback** — a decision to state and implement, not discover: if a stored-token charge
declines, does the payer fall back to entering a card (better conversion, more paths), or fail
cleanly with a re-try option (simpler, fewer states)? Recommend: clean failure plus an explicit
"pay another way" action, so the token path can never mask a decline.

**Log hygiene** — `truncate()` logs raw gateway bodies. That habit becomes unsafe the moment a
PAN or `AuthData` could pass through. Redaction is a requirement with a test (§6), not a
convention.

**Screening and limits** — a card-on-file charge is customer-not-present money movement. It must
pass the **G2 payment-screening seam** and the allowance/velocity checks *before* dispatch.
Not optional.

**Done when:** a second payment for the same merchant completes from the stored token end to
end; declines and timeouts land in the same status machine as a hosted attempt; a
screened/blocked counterparty still stops the charge; a replayed reference is a no-op; the fee
charged equals the card fee exactly.

---

### Phase 3 — Security, DSR and admin controls (~1 week)

- **Enrollment is step-up protected:** MFA (`internal/app/mfa.go`) and a trusted-device check; a
  new instrument enrolled from an unknown device emits a Trust Fabric risk event so the band
  moves, the same way shared devices and ring membership already do.
- **Consent record:** reuse `consent_records` (029) with a `card_on_file` purpose, storing the
  exact wording shown at enrollment — chargeback defence and NDPA both need it.
- **Revocation is first-class:** self-service revoke in chat ("remove my saved card") plus an
  admin revoke; both audited on the hash chain.
- **Kill switch (new):** `CARD_ON_FILE_ENABLED` disables stored-token charging without a deploy,
  and an admin "revoke all instruments" incident action exists for a credential compromise. A
  stored chargeable instrument needs an operational off-ramp, not just a UI toggle. Both belong
  in `docs/runbooks/GO_LIVE_RUNBOOK.md`.
- **Chargeback/dispute evidence (moved up from Phase 4):** disputes begin the day B ships. The
  evidence pack — receipt, device/trusted-device sighting, screening decision, consent wording,
  enrolment channel — is a shipping requirement, not a later enhancement.
- **DSR:** `ExportUserData` must include instruments **masked** (never the token); erasure must
  destroy them. Erasure deletes the `users` row, so the FK decides it — make it
  `ON DELETE CASCADE` deliberately, and prove it in a test.
- **Retention split, stated explicitly:** the *transaction record* keeps the AML 5-year floor
  (G6); the *credential* does not. A revoked token is destroyed, not archived.
- **Admin surface:** per-user instrument panel (masked, status, last used, enrolled device,
  revoke) alongside the KYC/compliance views, in the style of the internal-watchlist and
  KYB-authenticity panels.
- **Locality:** tokens are Nigerian payment credentials, in scope for
  `docs/compliance/CBN_DATA_LOCALITY_GAP.md` G16 — in-country primary and in-country backups, no exception.

---

### Phase 4 — Only if there is real demand: recurring / mandates

Auto-contributions to thrift cycles, subscription invoices, savings plans. Strictly gated on an
explicit mandate record, pre-debit notification, a cooling-off window, and one-tap cancellation.
**Do not** fold this into Phase 2 — unattended debits are a conduct-risk product, not a storage
feature.

---

## 6. Test strategy

- **Fee and rail guard — implemented** (`fees_rail_guard_test.go`). It reads the `Provider*`
  string constants out of the service package's own source rather than a hand-written list, so a
  rail added later is checked without anyone updating the guard. It immediately earned its keep:
  it flagged `ProviderAuto` and `ProviderSimulatedData`, forcing each to be recorded as a
  deliberate exemption instead of an oversight. `TestRailGuardHasTeeth` proves the guard's premise
  still holds, and `TestCardShapeClassifierHasTeeth` does the same for the others below.
- **Negative schema guard — implemented** (`payment_card_data_guard_test.go`). Scans migration SQL
  for PAN/CVV/PIN-shaped columns, templates for such form-field names, and Go for such form
  lookups; refuses to run below 100 scanned files so a broken walk cannot pass vacuously. It
  permits `pan_last4` and ignores the Interswitch iframe mount points, which are not form fields.
- **Redaction:** no handler, log line or error path can emit a full token, `AuthData`, or PAN —
  assert on the log/error surface, not just the response.
- **Unit:** masked instrument formatting; picker wording per posture; preference selection and
  override.
- **Store integration:** preference round-trip and uniqueness; enroll → mask → charge → revoke;
  `sealValue` round-trip plus ciphertext-at-rest assertion; unique-token constraint;
  `InstrumentForCharge` refuses revoked/expired and stamps `last_used_at`; DSR export masks; DSR
  erasure destroys.
- **Service:** token charge success / decline / timeout map into the *existing* status machine;
  screening block and allowance limit stop the charge; replay of the same reference is a no-op
  (the idempotency property the standing-credit path already relies on); the fee equals the card
  fee; `authorised_via` and `instrument_id` are set exactly once.
- **App/hermetic — implemented:** the picker renders in both postures and the enrollment consent
  step is not skippable (Phase 2). For the Phase 0.5 evidence panel, `admin_reentry_test.go` pins
  every branch of the reading — including that an empty funnel and a too-small sample are refused
  rather than called — the displayed figures, the merchant breakdown, the window labels, and that
  `metrics.html` renders both with and without the panel and escapes a merchant name.
- **Docs guard:** the runbook's PCI scope line and the decision record agree, in the style of the
  existing G7 drift guard.

---

## 7. Decisions this plan is asking for

1. **Posture** (§4): Option 0, A, B, B′, or C — and if B, acceptance of the PCI scope change.
2. **Q9 outcome** — if a stored-token charge always challenges, does B still make sense?
3. Who owns the **ASV scan and pen test** if B lands, and where in the pre-deploy schedule.
4. The **decline-fallback** policy (§5 Phase 2).
5. Whether **recurring (Phase 4)** is in product scope at all — it changes the enrollment
   wording, so answering early avoids rewording later.

## 8. Explicitly out of scope

- Storing anything more card-shaped than a token and `panLast4` — no PAN, no CVV, no PIN.
- A second card gateway: the stack is Interswitch-only and stays that way.
- Card-on-file for *payouts* (collection-side only).
- **Auto-enrollment** of any card without explicit consent at the moment of saving.
- Storing a mandate in Phase 2 — recurring is Phase 4 or nothing.
