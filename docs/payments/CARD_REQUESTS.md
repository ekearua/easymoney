# Physical Card Requests — operating runbook

**Status:** built and **closed by default** (`CARD_REQUESTS_ENABLED=false`)
**Migration:** `106_card_requests.sql`
**Store:** `internal/store/store_card_requests.go`
**Console:** `/admin/cards` (compliance), `/agent/cards` (agent)
Related: `docs/payments/CARD_ON_FILE_DECISION.md`, `docs/payments/SAVED_CARD_METHODS_PLAN.md`,
`docs/compliance/CBN_AML_COMPLIANCE_SPEC.md`, `docs/runbooks/GO_LIVE_RUNBOOK.md`

---

## 1. What this is, and what it is not

A customer asks for a **physical card** to be handed over at a local agent in
their area. Compliance reviews the request, records that the programme's
issuer/PCI/funding/API/fulfilment gates are evidenced, and only then may an
operator record externally manufactured stock and assign it to an agent. The
customer receives a one-time pickup code and collects in person with a
government photo ID.

It is **not** a card-issuing integration. Nothing in this workflow calls an
issuer card-manufacturing API, and the platform never becomes a card-data
environment:

- no PAN, CVV, PIN, track data or expiry is collected, stored, logged or
  transmitted by this flow;
- no identity-document number is captured — the handover records only the
  **type** of document the agent inspected;
- the card itself is ordered outside the platform, and the operator records the
  issuer reference as the evidence that the plastic exists.

That boundary is deliberate. It keeps the platform inside its existing PCI
posture while the commercial and issuer-contract work is still open (§9).

## 2. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CARD_REQUESTS_ENABLED` | `false` | Gates **new requests only**. Compliance review and the handover of stock already issued keep working when this is off, so a customer holding a pickup code is never stranded. |
| `CARD_REQUEST_PICKUP_TTL` | `720h` (30 days) | How long a one-time handover code stays valid. Capped at 180 days; must be positive. |

`CARD_REQUESTS_ENABLED=true` requires `AGENT_NETWORK_ENABLED=true`: a card is
handed over at an agent, and a configuration that offers cards with nowhere to
collect them is an operator error, not a degraded mode.

## 3. The five readiness gates

All five must be attested ready before any stock can be marked ready. A gate
with no row, or a row with `ready=false`, blocks the handover. Attesting a gate
ready requires an **evidence reference** — "we believe it is fine" cannot be
recorded as readiness. Withdrawing a gate is always allowed and immediately
blocks the next stock handover; cards already issued can still be collected.

| Gate | What it proves | Evidence that satisfies it |
|---|---|---|
| `issuer_contract` | A signed card-issuing agreement names Xego as programme owner and defines who issues, funds and supports the plastic. | Executed MSA / programme agreement reference |
| `pci_scope` | A signed data-flow review confirms the platform never handles PAN/CVV/PIN and the issuer or processor owns card-data scope. | PCI scoping document or QSA sign-off |
| `funding_settlement` | The prefunding/settlement account, cardholder liability and fee flows are agreed and reconciled to the ledger. | Treasury memo or settlement-account instruction |
| `issuer_api` | The **documented** issuance and status APIs are contracted for this programme. | API product schedule / sandbox-to-production approval |
| `fulfillment` | Personalisation, delivery to the agent, custody and lost-card handling have an owner and a written procedure. | Courier contract, custody procedure, incident runbook |

Gates are recorded by a compliance or admin operator at `/admin/cards`; every
attestation is written to the audit chain (`admin.card_gate_attested`).

## 4. Customer flow (chat)

Reachable by command rather than a menu row, because the main menu is already at
WhatsApp's 10-row ceiling:

- `request card` (also `physical card`, `debit card`, `get card`, `new card`)
- `my card` / `card status` to check progress

Steps: cardholder name → collection area → review → confirm. Requesting needs an
approved individual profile (the CBN Tier 2 identity step); an unverified
customer is routed into that upgrade instead of a dead end. Only areas that
currently have an **active agent** are offered, so a request is never accepted
for an area nobody can serve. One open request per customer: a second attempt
shows the existing request's status instead of opening a duplicate.

The customer's status view is generated from their own rows only, and never
contains a pickup code — the code is delivered once, when the card is ready.

## 5. Compliance review and stock handover

1. **Review** (`requested` → `approved` | `rejected`). A rejection requires a
   reason, which is shown to the customer. Approving does not hand out a card.
2. **Mark ready** (`approved` → `ready`). The operator records the issuer
   reference, picks an active agent **in the customer's area**, and the platform
   mints a six-digit pickup code. The plaintext code exists only long enough to
   be encrypted into the outbox; the database stores a salted HMAC-SHA256
   digest. The customer is messaged on the channel they requested from (falling
   back to WhatsApp).
3. **Resend code** for a ready, uncollected card mints a new code and retires
   the previous one in the same transaction — for a customer who lost the code
   or never received the message.

The store re-checks all of this inside the transaction that makes the card
collectable: approved status, all five gates, a non-empty issuer reference, and
an active agent in the request's region. A UI state can therefore never be the
authority for stock handover.

## 6. Agent handover

At `/agent/cards` the agent sees the cards assigned to their merchant, enters the
customer's code, selects the government ID type inspected, and records the
handover. `CollectCardRequest` re-checks, in one transaction:

- the agent exists, is active, and its region matches the request's area;
- the code matches one of that agent's pending card pickups (constant time);
- the code is unexpired and not locked;
- the request is still `ready`.

A successful handover retires the code, stamps `collected_at`, `collected_by`
and `collection_id_type`, and writes `agent.card_collected` to the audit chain.
A wrong code is not attributable to one customer's row — an agent may hold
several cards — so it never increments someone else's counter and can never lock
an unrelated card. Brute-force resistance here is the console's per-terminal
failure throttle on top of the code's short life and the visual ID check.

## 7. Refusals an operator may see

| Refusal | Meaning |
|---|---|
| `ErrCardRequestOpen` | That customer already has a request that is requested, approved or ready. |
| `ErrCardRequestRegionInvalid` | The collection area is missing or inactive. |
| `ErrCardRequestInvalidState` | The action does not apply to the request's current status (e.g. marking an unreviewed card ready). |
| `ErrCardIssuerNotReady` | At least one readiness gate is not evidenced. |
| `ErrCardIssuerReferenceAbsent` | No issuer reference was recorded for the manufactured card. |
| `ErrCardAgentUnavailable` / `ErrCardRequestRegionMismatch` | The chosen agent is not active, or is not in the customer's area. |
| `ErrCardPickupInvalid` | The code did not match a pending pickup at this agent. |
| `ErrCardPickupExpired` / `ErrCardPickupLocked` | The code has expired or reached its attempt limit; compliance must reissue. |
| `ErrCardIDTypeRequired` | The agent did not record the ID type inspected. |

## 8. Data handling

| Data | Where it lives | Notes |
|---|---|---|
| Cardholder name | `card_requests.cardholder_name_enc` | Sealed with the platform data key (`Store.sealValue`), opened only for the console. |
| Collection area, status, review note, reviewer | `card_requests` | Reviewed and collected timestamps retained for the audit trail. |
| Issuer reference | `card_requests.issuer_reference` | Opaque manufacturer/issuer reference; never a PAN. |
| Pickup code | `card_request_pickups.code_hash` + `code_salt` | Salted HMAC digest only; plaintext goes through the encrypted outbox. |
| ID inspected | `card_requests.collection_id_type` | Document **type** only. The number is never captured. |
| Readiness evidence | `card_issuer_readiness` | Gate, boolean, evidence reference, note, attesting operator, timestamp. |

`card_requests.user_id` cascades with the customer row, so a DSR erasure removes
the request (and its pickups) with the account; the audit entries evidence the
review and handover independently. Card credentials are never part of this
schema, so there is nothing card-data-shaped to erase or tokenise.

## 9. Before enabling `CARD_REQUESTS_ENABLED`

This workflow is the operational envelope around an external issuer
relationship. It does not replace any of the following, and enabling requests
without them would promise a card the platform cannot issue:

1. **Issuer contract and BIN sponsorship** — who issues, whose name is on the
   plastic, who owns cardholder liability.
2. **A documented issuance API integration** — including card status, block and
   replacement. The platform intentionally ships no undocumented endpoint call;
   the adapter is a separate, contracted piece of work.
3. **Fee and funding model** — issuance fee, replacement fee, and how prefunding
   or settlement reconciles to the ledger. The request itself moves no money.
4. **Fulfilment and custody** — personalisation, delivery to each agent,
   acceptance testing, secure storage and lost-in-transit handling.
5. **Complaints and disputes** — the cardholder-facing route for a card that
   never arrived or was mis-collected, with the audit trail as evidence.
