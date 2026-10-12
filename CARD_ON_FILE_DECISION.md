# Card on file — decision record

**Status:** **Open** — enquiry issued to Interswitch, answers pending (see §3 and Appendix A).
**Date:** 2026-10-12
**Last updated:** 2026-10-12 (initial skeleton + enquiry).
**Vendor facts last verified against published material:** 2026-10-12 (§4).
**Decision owner:** Platform + Compliance (joint — this changes PCI DSS scope, not just code).

**Scope of this decision:** whether Faya may store a card *credential* on behalf of a payer,
and if so under which posture. Plan and phases: `SAVED_CARD_METHODS_PLAN.md`.

**In scope:**

- Card **tokenization / card-on-file** on the Interswitch Payment Gateway.
- The resulting **PCI DSS scope** for the platform and its deployment.
- Enrollment, revocation, consent, data-subject-rights and retention handling for a stored token.

**Out of scope, deliberately:**

- **Recurring / mandated debit** (unattended scheduled charges). Separate product decision with
  its own mandate, pre-debit-notice and cancellation obligations; not settled here.
- Adding a second card gateway — the stack is Interswitch-only.
- Card-on-file for **payouts** (this record is collection-side only).
- Storing anything beyond a gateway token and `panLast4` — no PAN, CVV or PIN, ever.

---

## 1. Why this decision is being made now

Payer feedback and the checkout funnel both point the same way: a returning payer re-enters
card details on every purchase, because the platform deliberately stores nothing. The
attractive fix — "save my card" — is not a UI change. On this gateway it is a change of
integration mode, and the integration mode is what currently keeps card data out of our
systems.

Today's posture (verified in code, not assumed):

| Fact | Where |
|---|---|
| Card rail is Interswitch only | `PAYMENT_PROVIDER=interswitch`, `internal/config/config.go` |
| Default render mode is **Hosted Fields** — card number/expiry/CVV/PIN/OTP sit in Interswitch-hosted iframes; the platform page carries only the SDK config island and a scoped CSP | `INTERSWITCH_CHECKOUT_RENDER=hosted_fields`, `internal/app/checkout.go`, `internal/providers/interswitch/page.go` |
| Alternative mode is the legacy Web Checkout redirect form | `interswitch_checkout.html`, `NewPayPage` |
| Outcome authority is always server-side: signed webhook + `gettransaction.json` requery | `ValidateWebhook`, `Verify`, `VerifyAndApply` |
| The requery already returns a card field the platform **discards**; nothing card-derived is persisted | `requeryResponse.CardNumber` in `internal/providers/interswitch/client.go`; `payments` has no card columns (migration 001) |
| No tokenization, vault or instrument code exists anywhere in the tree | absent — the plan's Phase 1 creates the tables |

So the honest framing is: **the platform has no card data today by architecture, not by luck**,
and the feature request asks us to change that architecture (or to get one-tap without changing
it). This record exists to force that choice to be made explicitly and with the vendor's
written position in hand.

---

## 2. What we already own — and therefore must not buy or rebuild

| Capability | Where it lives | Consequence for this decision |
|---|---|---|
| AES-256-GCM application-level encryption at rest | `Store.SetDataKey` / `sealValue`, `internal/store/store.go` | a token column is written encrypted via the existing mechanism — no new crypto |
| Consent records | `consent_records` (migration 029), `store_dsr.go` | enrollment consent reuses this table; no new consent concept |
| Data-subject export and erasure | `store_dsr.go` (`ExportUserData`, `CompleteDataSubjectRequest`) | export must include tokens **masked**; erasure must destroy them |
| Retention floor | `store_retention.go` (spec G6, 5-year AML floor) | applies to transaction records, **not** to the credential — see §9 |
| "Refuse and open a review case" pattern | `store_identity_keys.go` (spec G11) | the model for any enrollment that must be refused rather than failed |
| Payment screening seam | `payment_screenings` + `ScreenPayout` (spec G2) | a card-on-file charge is customer-not-present and must pass this before dispatch |
| Device and risk signals | `store_devices.go`, Trust Fabric ring signals, `internal/kyc/network_risk.go` | a new instrument on a new device should move the risk band |
| Step-up authentication | `internal/app/mfa.go` (TOTP) | enrollment is gated on it |
| Audit chain and admin queues | `store_audit_chain.go`, `review_cases`, admin compliance views | revocation and enrollment are auditable acts |
| In-country data rule | `CBN_DATA_LOCALITY_GAP.md` G16 | tokens are Nigerian payment credentials: primary and backups stay in-country |

---

## 3. Questions put to the vendor — and their answers

The enquiry at **Appendix A** was issued on ☐ *(date)*. Answers are recorded verbatim where
possible, with the source (email, ticket, doc link) and the date.

| # | Question | What it decides | Answer | Source / date |
|---|---|---|---|---|
| Q1 | Is card **tokenization** enabled on our merchant profile and pay item? | whether posture B is available at all | *pending* | |
| Q2 | Is there a **hosted / iframe tokenize** flow — card captured by Interswitch, only a token returned to us? | **the highest-value answer**: if yes, B costs ~nothing and stays in SAQ-A | *pending* | |
| Q3 | What exactly is `AuthData`, how is it produced, and in what environment does that code run? | whether a PAN ever reaches our page/JS (SAQ-A vs SAQ-A-EP/SAQ-D) | *pending* | |
| Q4 | Token **TTL / `tokenExpiryDate`** semantics; behaviour on card expiry, reissue or replacement | whether a stored token has a shelf life we must manage, and what "expired" means to us | *pending* | |
| Q5 | Does tokenization work in **TEST**, and with which test cards? | whether this is testable before go-live at all | *pending* | |
| Q6 | `panLast4Digits` format, and is the **card brand** returned? | what we can honestly show in the UI ("Card •••• 4321" vs "your saved card") | *pending* | |
| Q7 | Can a saved-card charge fund a payment that is **not** a straight card purchase (DVA-funded, wallet top-up)? | whether the rail composes with our other rails or is card-purchase-only | *pending* | |
| Q8 | **How is the outcome of a `purchases/recurrents` charge confirmed** — webhook, `gettransaction.json` requery, or a separate status endpoint? | whether the charge can be reconciled by the authority we already trust, or needs a new path | *pending* | |
| Q9 | Does a stored-token charge trigger a **3-D Secure challenge** for the payer? | **the value gate**: if every charge is challenged, B is not one-tap and the PCI scope change is unjustified | *pending* | |

**Support questions folded into the same enquiry** (not gates, but live friction):

- The documented QA token endpoint `qa.interswitchng.com/passport/oauth/token` returns 404 —
  what is the correct QA passport host?
- Is there a **working sandbox Hosted Fields SDK origin**? The documented QA SDK host neither
  resolves nor completes a TLS handshake, so both modes currently load the live SDK URL.

---

## 4. Options

### 4.1 Posture A — remembered preference (no card storage)

Store the payer's **choice** (rail, per channel, optionally scoped to a merchant). Nothing
card-derived is stored. Zero change to PCI scope. Delivers "stop asking me which rail" but not one-tap: the
payer still enters card details on the hosted page, and we cannot pre-fill them.

### 4.2 Posture B — Interswitch tokenization (card on file)

Vendor-published contract, as understood before the answers:

- Tokenize: `POST /api/v2/purchases/validations/recurrents` with `transactionRef` and
  `AuthData` (encrypted card details) → returns a **payment token** and its expiry.
- Charge: `POST /api/v2/purchases/recurrents` with `customerId`, `amount`, `currency=NGN`,
  `token`, `tokenExpiryDate`, `transferRef`.
- Or, in a transaction request, `token=1` in `authData` asks the gateway to save the card in
  its "PCI DSS and PCI PIN certified environment" and return a token plus `panLast4Digits`.
- Up to **5 saved cards** per customer.

Our understanding of the constraint, taken from the vendor's own text:

> "Recurring payments on Interswitch Payment Gateway are only supported on Merchant Hosted
> (API) integrations."

Our production integration is **not** merchant-hosted — it is Hosted Fields, chosen precisely so
the PAN never reaches us. So B, via the published API route, implies our own page or client-side
JS handling a PAN before encryption, which is a scope change, not a configuration change.

**Residual uncertainty, correctly stated:** whether a *hosted tokenize* variant exists that gets
us B while keeping the PAN inside Interswitch's frame. That is Q2, and it is the answer this
whole record turns on.

**A second condition, equally decisive — Q9.** If every stored-token charge triggers a 3-D Secure
challenge, B does not deliver one-tap at all, and the PCI scope change is not justified by the
convenience it buys. Q2 decides what B *costs*; Q9 decides whether B is *worth anything*.

### 4.3 Posture B′ — B via a hosted tokenize flow (if Q2 = yes)

If Interswitch captures the card in its own frame and returns only a token, B′ delivers the same
one-tap experience as B while leaving the PCI scope roughly where it is. In that case B′ is
unambiguously the posture to adopt, and the plan's Phases 2 and 3 proceed unchanged. B′ is not a
variant we build for: it is the *answer* to B, and Q2 decides which one we are in.

### 4.4 Posture C — external PCI-1 vault

A third-party vault returning a network token, gateway-agnostic. More vendors, same in-country
data question, and no advantage over B for a single-gateway stack. Held as a fallback only.

### 4.5 Option 0 — build neither

Keep today's hosted entry and make it faster instead. This is a genuine option, not a
strawman: the platform currently holds no card data at all, and that is worth something. It is
the right answer whenever **Phase 0.5** of the plan shows that returning-payer re-entry friction
is immaterial, in which case both A and B are spending money to solve a problem we have not
measured.

---

## 5. Comparison

| | A — preference | B — Interswitch token } (API route) | B′ — Interswitch token (hosted route, if Q2 = yes) | C — external vault |
|---|---|---|---|---|
| One-tap pay | No | Yes — **subject to Q9** | Yes — **subject to Q9** | Yes |
| 3-D Secure on each stored-token charge | n/a | unknown — **Q9** | unknown — **Q9** | unknown |
| Outcome confirmable without a new path | n/a | unknown — **Q8** | unknown — **Q8** | unknown |
| Card data reaches us | No | **Yes** (client-side, pre-encryption) | No | No |
| PCI DSS scope | **unchanged (SAQ-A)** | **SAQ-A-EP / SAQ-D** | unchanged, if confirmed in writing | SAQ-A + vendor dependency |
| ASV scan / pen test | not required for this feature | **required** | not required for this feature | vendor's burden |
| New vendors | none | none | none | one |
| In-country data | n/a | token stored in Nigeria | token stored in Nigeria | second jurisdiction to assess |
| Engineering (plan Phases) | 1, 3 | 1, 2, 3 | 1, 2, 3 | 1, 2, 3 + vault integration |
| Enables recurring later | No | Yes | Yes | Yes |

---

## 6. Recommendation

**Provisional — to be confirmed by §3.**

**Measure first (Phase 0.5 of the plan).** Posture A is cheap and delivers little; B costs a PCI
scope change. Both should follow the returning-payer re-entry numbers, not the assumption that
the feature earns its bill. If the measured friction is immaterial, **Option 0 — build neither,
and optimise the hosted entry** — is the honest answer, and this record should say so rather than
build for the sake of building.

If the measurement justifies it, build **Phase 1 (posture A)**: the preference layer is
posture-neutral and is the schema B needs. In parallel, obtain the §3 answers.

**Measured 2026-10-12 — insufficient data to justify B.** The development database holds three
lifetime payments from one payer, every one still `awaiting_confirmation` or `pending`: the card
rail has not yet completed a payment here, so there is no abandonment rate to read and no
returning payer to serve. Posture B's value is therefore unproven *and unprovable* at this volume,
and Option 0 is indistinguishable from it until the funnel carries traffic. **Posture B is not
justified today on the evidence**, independent of what the vendor answers say. Figures and the
query: `SAVED_CARD_METHODS_PLAN.md` Phase 0.5.

Both figures that were previously missing are now instrumented and surfaced on `/admin/metrics`:
repeat-purchase share per merchant, and a metadata-only proxy for support follow-ups after a
failed card attempt (no message content is read — inbound payloads are sealed at rest). The call
can therefore be re-made from live data once the card rail carries traffic, without building
anything further.

Then:

- **If Q2 = yes** (a hosted tokenize flow exists): adopt **B′**. One-tap at effectively no new
  PCI scope is unambiguously worth building, and the plan's Phase 2/3 proceed unchanged.
- **If Q2 = no**: adopt **A only** for now, and treat the PCI scope change of B as a *separate,
  costed decision* with the Compliance owner — not as a hidden consequence of a UX request. The
  honest alternative to paying for that is to accept that a returning payer re-enters card
  details, and to make that path as fast as possible.

Even where Q2 = yes, **Q9 still gates the value**: a hosted tokenize flow that nevertheless
challenges the payer on every charge gives us SAQ-A and no one-tap — the worst of both.

This record does **not** recommend posture C.

---

## 7. Trial criteria that would overturn the recommendation

1. **Q9 = every stored-token charge challenges the payer.** Then B is not one-tap, and the PCI
   scope change buys nothing a faster hosted entry would not. This is the value gate.
2. **Q8 = no confirmable outcome path** for `purchases/recurrents` — no webhook, no requery, no
   status endpoint. A charge whose outcome cannot be reconciled by the authority we already trust
   as a hosted attempt cannot ship.
3. Q2 answered **no**, or answered yes without a written statement of the frame origin.
4. Token TTL so short (Q4) that a stored token is useless for the repeat-purchase window.
5. Tokenization unavailable in TEST (Q5) — an untestable rail cannot ship.
6. No brand or last-4 returned (Q6) — a saved card we cannot label honestly is a support burden
   with no UX gain.
7. Q7 = card-purchase-only, and our funnel shows the repeat-purchase demand is on the
   wallet/DVA rails — in which case B buys little and A carries the value.
8. The scope change in §5 is costed and rejected by Compliance in writing.
9. Phase 0.5 measurement shows re-entry friction is immaterial — in which case **Option 0** (build
   neither) is the honest answer.

---

## 8. Integration against the existing seams

Recorded here so the decision lands on real code rather than a greenfield design:

- **Port:** `CardTokenizer` in `internal/ports/ports.go`, in the established verifier style
  (`IdentityVerifier`, `LivenessVerifier`, `DocumentAuthenticityVerifier`).
- **Provider:** `internal/providers/interswitch/cardtoken.go`, beside the existing `token.go`
  (OAuth) to avoid confusing the two token concepts; plus a **simulated default** in the style of
  `internal/providers/paymentscreening/simulated.go` so tests and demos run hermetically.
- **Rail vs authorisation — no new rail constant.** A saved-card payment *is* a card payment over
  Interswitch, so `provider` stays `interswitch` and the difference is recorded as
  `payments.authorised_via` (`hosted_entry | stored_token`) plus a nullable
  `payments.instrument_id`. A `card_on_file` rail would map through `FeeChannelForProvider`'s
  `default:` branch to a **zero-fee** channel — a silent revenue leak, not a compile error — and
  would be rejected by the allow-lists at `internal/service/payments.go:241` and `:500`. The full
  rail-switch inventory is `SAVED_CARD_METHODS_PLAN.md` §3.
- **Charge path:** `InitializeCheckout` performs the token charge instead of returning a hosted
  URL, and the outcome reaches the status machine, ledger, receipt, settlement and screening
  paths the same way a hosted attempt does — **subject to Q8**, which decides whether that is via
  webhook, requery, or a new status call. That is a requirement to confirm, not an observation.
- **Screening:** the charge passes the **G2 seam** and allowance/velocity checks *before*
  dispatch — customer-not-present movement, treated like a payout.
- **Storage:** `payment_instruments` (migration **103**, gated on this decision), `token_enc`
  written via `sealValue`; masked accessors only — no handler or template ever sees the token.
  Migration 102 carries the posture-neutral preference table only.
- **Enrollment:** step-up (MFA) + trusted-device check; new instrument on a new device emits a
  Trust Fabric risk event.
- **Discovery:** self-service revoke in chat plus an admin revoke, both on the audit chain.

---

## 9. Consequences, risks, open questions

**Consequences if B lands:**

- The deployment's compliance posture changes: self-assessment questionnaire, ASV scans,
  segmentation evidence and a pen test enter the pre-deploy schedule, and
  `GO_LIVE_RUNBOOK.md` §3.7 must state the new scope rather than leaving it implied.
- A stolen token becomes a chargeable instrument: revocation latency, per-instrument limits and
  velocity caps become security controls, not UX niceties.

**Positions taken in advance (to be confirmed, not re-litigated):**

- **The transaction record keeps the AML 5-year floor; the credential does not.** A revoked token
  is destroyed, not archived. Retaining a chargeable credential for years of "compliance" is an
  unnecessary liability with no regulatory basis.
- **No PAN, CVV or PIN column may exist**, in any environment, under any posture. Enforced by a
  build-failing guard, not by review.

**Open questions:**

- Who owns the ASV scan and pen test if B lands, and where in the pre-deploy schedule (Compliance
  vs Platform).
- Whether recurring (Phase 4 of `SAVED_CARD_METHODS_PLAN.md`) is in product scope — it changes the
  enrollment wording, so answering early avoids rewording later.

---

## 10. Sources and caveats

- Interswitch, *Card Payments* (developer documentation) — saving cards, up to 5 per customer,
  `token=1` in `authData`, token + `panLast4Digits` returned, PCI DSS/PCI PIN certified
  environment, test card details.
- Interswitch, *Recurring Payments* — the two endpoints, their request fields, `currency` NGN
  only, Verve/Visa/Mastercard schemes, and the merchant-hosted-only restriction.
- Vendor facts last verified **2026-10-12** from published material. **Vendor documentation is not
  a contract**: every claim above that gates implementation (Q1, Q2, Q3, Q5, Q8, Q9) requires a
  written answer from our TAM, recorded in §3 with its source and date. Until then, posture B′
  remains a hypothesis.
- No credentials, client secrets or merchant identifiers appear in this record by design.

---

## 11. Settled decision

**Status: not yet settled.** To be completed when §3 is answered:

- **Posture adopted:** ☐ A ☐ B ☐ B′ ☐ C
- **PCI DSS scope statement:** ☐ unchanged (SAQ-A) ☐ SAQ-A-EP ☐ SAQ-D — and the runbook line that
  records it.
- **Effective date / decided by:**
- **Conditions attached:**

---

## Appendix A — Enquiry sent to Interswitch (send-ready)

> **To:** `<TAM name>` — Interswitch, `<relationship manager email>`
> **CC:** `<our CTO / Compliance lead>`
> **Subject:** Faya — card tokenization / card-on-file availability on our merchant profile
> `<MERCHANT CODE>`

Hello `<TAM name>`,

We run Faya, a Nigerian merchant-payments platform integrating the Interswitch Payment Gateway for
card checkout. Our card acceptance is live in the Quickteller Business harness under merchant code
`<MERCHANT CODE>` (pay item `<PAY ITEM ID>`), in TEST today and moving to LIVE.

**How we integrate today, so you can route this correctly:**

- **Hosted Fields SDK** (default) — `isw-hosted-fields` `create()` with `paymentParameters`,
  `cardinal.containerSelector` and fields `cardNumber` / `expirationDate` / `cvv` / `pin` / `otp`.
  We chose this deliberately so card data never touches our systems.
- **Web Checkout redirect** as a legacy fallback, posting to `newwebpay`.
- Server-side outcome confirmation via signed webhook (`X-Interswitch-Signature`, HMAC-SHA512) and
  `gettransaction.json` requery.
- Also live on the same merchant profile: dynamic virtual accounts (DVA) for bank transfer, and
  Quickteller Send Money v5 for NIP payouts.

**What we want to add:** let a returning payer save a card and pay again without re-entering
details. Our reading of your Card Payments and Recurring Payments documentation is that this is
enabled through tokenization, and that tokenization and recurring are supported on **Merchant
Hosted (API) integrations**. Because our current integration is hosted fields, we need your
written guidance before we change anything — the answer determines our PCI DSS scope, and we will
not move that scope on an assumption.

We would be grateful for answers to the following nine questions, in writing:

1. **Is card tokenization enabled on our merchant profile and pay item?** If not currently
   provisioned, what is required to enable it, and what is the expected turnaround?
2. **Is there a hosted or iframe tokenization flow** in which Interswitch's own frame captures the
   card and returns only a token to us — i.e. can we offer saved cards **without** bringing card
   data into our page or client-side JavaScript? This is the most important question for us: if
   such a flow exists, our compliance assessment changes completely. If the answer is that our own
   JavaScript must handle the PAN in any tokenize flow, please confirm that explicitly in writing.
3. **What is `AuthData`?** Specifically: what encryption does it use, what tooling or SDK produces
   it, and in what environment does that code execute — Interswitch's frame, or our page?
4. **Token lifetime and lifecycle:** what is the `tokenExpiryDate` and TTL semantics for a token,
   and what happens when the underlying card expires, is reissued, or is replaced by the issuer?
   Should we expect a token to become invalid without a transaction being attempted?
5. **Sandbox support:** does tokenization work in TEST, and which test cards can we use to exercise
   both tokenization and a subsequent token charge end to end?
6. **Response fields:** what exactly does `panLast4Digits` contain, and is the **card brand**
   returned as well? We intend to display a saved instrument as "Card •••• 1234" and will not
   display anything we cannot source reliably.
7. **Rail composition:** can a saved-card charge be used for payments that are not straight card
   purchases — specifically a wallet top-up, or a payment otherwise funded through the rails above?
8. **Outcome confirmation:** how is the result of a `purchases/recurrents` charge reported to us?
   Specifically, does it appear in the `gettransaction.json` requery we already use, does it emit
   an outbound webhook, or is there a separate status endpoint we must call? We need to confirm a
   charge server-side through the same authority we use today, and we would rather not assume it.
9. **3-D Secure:** does a charge made with a stored token trigger a 3-D Secure challenge for the
   payer? If the customer must be challenged on every saved-card charge, the saved card does not
   deliver the one-tap experience we are building for, and we need to know that before we change
   our card-data handling. If exemptions for merchant-initiated transactions are available, please
   tell us how they are requested.

Two smaller items in the same area, if they can be routed to the right team:

- Our provisioning notes say the QA OAuth token endpoint is
  `qa.interswitchng.com/passport/oauth/token`, but it currently returns 404. **What is the correct
  QA passport endpoint**, and does it require different credentials from the sandbox pair?
- We have been unable to use a sandbox Hosted Fields SDK origin: the documented QA SDK host neither
  resolves in DNS nor completes a TLS handshake from our networks, so we currently load
  `https://hostedfields.interswitchng.com/sdk.js` in both modes. **Is there a supported sandbox SDK
  origin**, and if so, may we test card charges against the sandbox webpay gateway with it?

If it is easier, we are happy to take this as a call — but we do need the answers to questions 1,
2, 3, 5, 8 and 9 in writing, as they are recorded in our compliance decision record and will be
referenced in our PCI DSS self-assessment.

Thank you,

`<Name>`
`<Role>`, Faya
`<Phone>` · `<Email>`

---

### Appendix A.1 — Notes before sending

- **Do not email the client secret, webhook secret or data-encryption key.** This enquiry needs no
  credentials; the merchant code and pay item are safe to include.
- Replace every `<PLACEHOLDER>` before sending. Copy the **sent** version (with the real merchant
  code) into the reply trail, not into this file.
- On reply: record each answer verbatim in **§3** with its source and date, then complete **§11**.
  Anything that gates implementation (Q1, 2, 3, 5, 8, 9) must be written, not verbal — if the answer
  arrives on a call, send a short follow-up email summarising what we understood and ask for
  confirmation.
