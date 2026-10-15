# Local POS agent network — cash pickup remittance plan

**Status:** confirmed, Phases 1–3 in progress. Decisions ratified 2026-10-13 and numeric
configuration values ratified (§2, §12). Phase 3's redemption choke point
(`RedeemPickupTx`) and the agent-facing redemption wrapper + receipt are implemented;
the `/agent` console surface remains.
**Owner:** platform
**Migrations:** `103_pos_agent_network.sql`
Related: `docs/programs/PROGRAM_PURSES.md` (per-entity ledger accounts), `docs/platform/PAYMENT_DOMAIN_METHOD_FLOWS.md`,
`docs/compliance/CBN_AML_COMPLIANCE_SPEC.md` (G2 screening, G6 retention, G16 locality),
`docs/compliance/CBN_DATA_LOCALITY_GAP.md`, `docs/payments/SAVED_CARD_METHODS_PLAN.md` (reserves `103` — coordinate),
`docs/runbooks/GO_LIVE_RUNBOOK.md`

---

## 1. The product in one pass

1. An urban sender picks a **destination region/agent**, the recipient's legal name
   (and optionally a phone), and an amount; pays by card, bank transfer, or wallet.
2. Xego mints a **pickup code** (single-use, expiring, hashed at rest) and returns it to
   the sender. Within the pickup window the sender can **request a second-factor OTP**;
   it is delivered to the **sender** (D8), who forwards it — there is no recipient app
   or account.
3. The recipient goes to the **assigned agent** with the code, the OTP the sender
   forwards, and a government photo ID.
4. The agent's console validates the code and OTP, captures the ID, and pays cash from
   the till.
   Xego discharges the remittance liability and books the amount to the **agent's float**.
5. Xego settles the agent's float to their bank on a cycle. Unclaimed remittances expire
   and **refund the sender** by reversal.

---

## 2. Confirmed decisions (2026-10-13)

| # | Decision | Ratified choice |
|---|---|---|
| D1 | Deliverable | `docs/programs/POS_AGENT_NETWORK_PLAN.md` at repo root |
| D2 | Scope | Implement in this Go codebase |
| D3 | Agent entity | `merchant_type='agent'` (extend the CHECK from migration 100) + `agent_profiles` extension |
| D4 | Float model | Fronted cash — `2400_agent_float` liability, caps + optional deposit, settled to bank |
| D5 | Recipient account | Not required — per-remittance payable `2350_remittance_payable:<id>`, code pickup |
| D6 | Identity | Government photo ID + pickup code + OTP on **every** payout; the ID is sealed and its salted hash keys the CBN per-customer cap |
| D7 | Redemption scope | Assigned agent only, geofenced to the agent's region |
| D8 | Second factor | Sender-requested OTP delivered to the **sender** (forwarded to the recipient); pickup needs code + OTP + ID |
| D9 | v1 scope | Cash-out only; no cash-in |
| D10 | Regulatory route | Operate under a partner/aggregator agent-banking licence |
| D11 | Providers | Simulated/sandbox, matching the current repo posture |

---

## 3. The one design decision everything follows from

**An agent is a merchant (`merchant_type='agent'`) with a dedicated real ledger account
`2400_agent_float:<merchant-id>`, and a remittance is a genuine payable
`2350_remittance_payable:<remittance-id>` — not a tag on a wallet.**

This is the exact move `docs/programs/PROGRAM_PURSES.md` makes for purses, and it buys the same
properties by construction:

| Property | Why it holds |
|---|---|
| A code cannot be spent like cash elsewhere | The code is a *claim*, redeemed only inside `RedeemPickupTx`; it never touches `2301_user_wallet` |
| Agent float can never drift from the book | Balance is `SUM(ledger_entries)` on the account, never stored (mirrors `walletBalanceQ`) |
| Expiry/refund are ordinary movements | Balanced reversals, so the journal always proves where the money went |
| The recipient needs no Xego account | The payable is per-remittance, not per-user (D5) |

---

## 4. What exists today (reusable seams — do not rebuild)

- **Individual pay rail** (`internal/service/conversation_individual_pay.go`): sender
  collection against an inactive system merchant, fee split, allowance reservation,
  gateway verification, post-success hook. A remittance is this flow with the payout leg
  swapped for a pickup claim.
- **Split payments** (migration 052, `internal/store/store_split_payments.go`,
  `RecordIndividualPaySettlement`): already debits `2100_customer_float` and books fee +
  payout legs idempotently on the payment id. The remittance settlement is a sibling that
  credits `2350_remittance_payable` instead of a recipient wallet.
- **Settlement** (`internal/store/store_settlements.go`, `ClaimPayoutForDispatch`): reuse
  to settle agent float to a bank on a cycle (mirrors merchant payout).
- **OTP / verification**: hashed short-lived codes (`VerifyEmailCode` / `emailCodeDigest`),
  TOTP + MFA (migration 071), step-up payout grants
  (`internal/app/merchant_payouts.go`), SMS provider (`internal/providers/sms`).
- **Screening** (G2 `ScreenPayout`), **fraud lane** (092), **transaction monitoring**
  (028), **geography** (095).
- **Merchant console** (KYB, password reset, MFA, business wallet
  `3101_business_wallet:<id>`): the agent console inherits this shape.
- **Encrypt-at-rest** `sealValue` (024) for ID numbers; **audit chain** (023/084);
  **outbox** (033); **idempotency** (045).

---

## 5. Ledger accounts and money flows

New chart entries in `internal/store/store_ledger.go` (constants + `validLedgerAccountPrefixes`):

- `2400_agent_float:<agent-merchant-id>` — liability; **positive = Xego owes the agent**
  (agent fronted cash), negative = agent holds Xego cash not yet remitted.
- `2350_remittance_payable:<remittance-id>` — liability; Xego owes the recipient.
- `2401_agent_deposit:<agent-merchant-id>` — agent security/collateral (risk control, D4).

```
sender card/gateway ──money-in──▶ 2100_customer_float
2100_customer_float  ──settlement split──▶ 4100_sales_revenue (fee)
                                        └▶ 2350_remittance_payable:<id>   (Xego owes recipient)
agent pays the recipient cash:
   dr 2350_remittance_payable:<id>   cr 2400_agent_float:<agent>           (claim shifts to agent)
agent settlement:
   dr 2400_agent_float               cr 1100_operating_bank                (agent made whole)
unclaimed expiry:
   dr 2350_remittance_payable        cr 1100_operating_bank → refund the sender
```

Agent commission is a split leg off the collection (bps × payout), reusing the 043/052 fee
machinery and **added on top of** the sender's existing local-transfer fee (no new
remittance fee is introduced).

---

## 6. Data model (`103_pos_agent_network.sql`)

- `merchants.merchant_type` CHECK gains `'agent'` (one line; the migration-100 pattern —
  classification only).
- `agent_profiles` — `merchant_id` PK, `region_id`, `geo_point`, `tier`,
  `max_outstanding_float_kobo`, `per_payout_limit_kobo`, `daily_payout_limit_kobo`,
  `commission_bps`, `status (pending|active|suspended)`, `agreement_ref`. **No FK to
  `users`** beyond the merchant, mirroring `wallet_accounts` (058) so retention/DSR purge
  is never blocked.
- `agent_regions` — state/LGA taxonomy, reusing the geography vocabulary (095).
- `remittances` — `id`, `sender_user_id`, `agent_id`, `region_id`, `amount_kobo`,
  `fee_kobo`, `recipient_name`, `recipient_phone`, `status
  (awaiting_pickup|paid|expired|refunded|cancelled)`, `payment_id`, timestamps.
- `remittance_pickups` — `remittance_id`, `code_hash`, `code_salt`, `expires_at`,
  `attempts`, `max_attempts`, `status`, `redeemed_at`, `redeemed_by_agent_id`. Unique
  active code per remittance. **Never a plaintext code column.**
- `remittance_otps` — `remittance_id`, `otp_hash`, `otp_salt`, `expires_at`, `attempts`,
  `max_attempts`, `status`, `consumed_at`. Unique active OTP per remittance; a reissue
  supersedes. **Never a plaintext OTP column.**
- `remittance_payouts` — `remittance_id`, `agent_id`, `operator_user_id`,
  `receiver_id_type`, `receiver_id_number_enc` (sealed), `receiver_id_hash` (salted, the
  per-customer cap key), `geo_point`, `paid_at`, `journal_ref`.
- `agent_users` — agent console identity (owner/operator), password/MFA, channel binding.

---

## 7. Where the rules are enforced (single choke point)

Exactly one function can authorise a payout: **`RedeemPickupTx`**, inside the same
transaction that moves the money (mirrors `PurseSpendAllowedTx`, `docs/programs/PROGRAM_PURSES.md`). It
re-checks, atomically:

- the pickup code hash and the sender-requested OTP both match, neither expired, both
  under their attempt limits, and the remittance is `awaiting_pickup`;
- agent is `active`, in the correct region, and the remittance is scoped to them (D7);
- `per_payout` and `daily` limits, and the resulting `2400_agent_float` stays within
  `max_outstanding_float_kobo`;
- a government ID is captured (D6), sealed, and hashed, and the recipient's
  `receiver_id_hash` daily/weekly cash-outs stay within the CBN per-customer ceiling;
- recipient screening decision is not `blocked`;
- the code and OTP are marked single-use and the float credited in the same transaction.

A `PickupForDisplay` selection helper may show the sender early, but it is never the
authority.

---

## 8. Pickup code / OTP design

- Code = cryptographically random numeric (`crypto/rand`), 6–8 digits, generated
  server-side; stored as `SHA-256(salt||code)`; never logged (reuse `internal/redact`).
- TTL = the pickup window (§12), single-use, attempt limit with lockout → manual review.
- **Second factor (D8):** the **sender** requests an OTP within the pickup window; it is
  delivered to the sender (chat/SMS) and forwarded to the recipient, who presents
  code + OTP + government ID. The OTP is hashed with its own salt/TTL and is single-use;
  a reissue supersedes the prior active OTP.
- **Per-customer cap key:** the ID captured at payout is sealed and its salted hash
  (`receiver_id_hash`) is the stable key used to sum a recipient's daily (₦100,000) and
  weekly (₦500,000) cash-outs across agents, since the recipient has no Xego account.
- Anti-fraud: per-agent and per-phone velocity, geofenced redemption, step-up for large
  amounts, structuring detection in monitoring.

---

## 9. Surfaces

- **Sender:** chat flow + `WebFlowRemittance` (mirrors `WebFlowIndividualPay`,
  `internal/service/webflow.go`). Code shown and re-sendable; the sender requests the
  OTP, which is delivered back to the sender.
- **Agent:** `/agent` console (merchant-console auth shape) + chat `payout <code>`;
  step-up TOTP per payout; printable receipt showing agent name and geo coordinates.
- **Recipient:** SMS (when a phone is on file) with the agent name/location, amount, and
  ID requirement; the code and OTP are relayed by the sender. No app.
- **Admin/ops:** `/admin/agents` (approve, set tier/limits, suspend, view float),
  reconciliation of float vs. cash, dispute queue, kill switch.

---

## 10. Compliance posture

- Cash-out is high-risk: screen the recipient name (G2), monitor cash-out/structuring,
  and record payer + recipient + agent + location + ID per transaction.
- **Identity (D6):** a government photo ID (NIN, BVN, driver's licence, voter's card or
  passport) plus the pickup code and OTP on **every** payout; the ID number is sealed
  (024) and a salted hash keys the per-customer cap.
- **CBN Agent Banking Guidelines (circular dated 6 Oct 2025; agent-location/exclusivity
  provisions from 1 Apr 2026):** agent daily cumulative cash-out ≤ ₦1,200,000; per
  customer daily cash-out ≤ ₦100,000 and weekly ≤ ₦500,000; dedicated agent
  accounts/wallets mandatory; real-time processing with instant settlement and automatic
  reversal; receipts must show the agent name and geographic coordinates; 5-year record
  retention; agents restricted to a single principal from Apr 2026 and personally liable,
  with an industry fraud watchlist.
- **Tiered KYC (CBN CDD Regulations 2023):** BVN and/or NIN is mandatory even at Tier 1;
  migration 073 already maps L1/L2/L3 to the CBN individual ceilings.
- **CBN cash policy (2 Dec 2025, effective 1 Jan 2026):** individual withdrawals
  ≤ ₦500,000/week across all channels, ATM ≤ ₦100,000/day; excess-cash charges 3%/5%.
- The `max` tier equals the CBN ceiling; `validateAgentNetwork` refuses a configured tier
  above it. Retention 5-year floor (G6), in-country only (G16).
- Unclaimed expiry → sender refund by reversal; full dispute/evidence pack (receipt, code
  sighting, ID, screening, geo, agent/operator).
- Operate under a partner/aggregator licence (D10) — the partner owns the agent network
  and cash rules.

---

## 11. Phases

- **Phase 0 — decisions + simulation.** Complete: decisions ratified (§2).
- **Phase 1 — agent ledger + onboarding.** `merchant_type='agent'`, `agent_profiles`,
  `2400_agent_float`, funding/settlement, limits, admin + agent console.
- **Phase 2 — send + code issuance.** Sender chat/web flow, `2350_remittance_payable`,
  pickup code + SMS, expiry/refund.
- **Phase 3 — cash-out.** `RedeemPickupTx`, ID capture, float credit, settlement, receipt.
  Store choke point + per-customer caps + sealed/hashed ID + `AgentRemittanceService`
  wrapper (screening + receipt) done; `/agent` console + step-up TOTP remaining.
- **Phase 4 — ops.** Reconciliation, monitoring/disputes, kill switch, multiple agents per
  region, then cash-in.

---

## 12. Configuration values — RATIFIED

Every value is read from configuration (`internal/config/config.go`), not baked into
logic. Per-agent overrides live on `agent_profiles`; the tier values below are the seed
defaults applied when an operator activates an agent.

1. **Pickup window TTL.** 72 hours (`REMITTANCE_TTL`); a pickup code is single-use and
   expires at the window close.
2. **OTP.** Sender-requested within the window, delivered to the sender, TTL 10 minutes
   (`REMITTANCE_OTP_TTL`); single-use, hashed, reissue supersedes.
3. **Identity.** A government ID + code + OTP on **every** payout (D6); `AGENT_ID_REQUIRED`.
4. **Caps by tier** (kobo; `AGENT_<TIER>_*`), bounded by the CBN ceilings
   (`CBNMaxAgentDailyCashOutKobo` = ₦1.2m; `CBNMaxCustomerDailyCashOutKobo` = ₦100k):

   | Tier | per-payout | daily | max outstanding (float+deposit) | commission |
   |---|---|---|---|---|
   | `standard` | ₦20,000 | ₦200,000 | ₦500,000 | 50 bps |
   | `super` | ₦50,000 | ₦600,000 | ₦1,500,000 | 40 bps |
   | `max` | ₦100,000 | ₦1,200,000 | ₦2,500,000 | 30 bps |

5. **Commission** is **added on top of** the sender's existing local-transfer fee
   (`XegoCollectionFee`); no separate remittance fee is introduced. Outstanding cap counts
   **float + deposit** (`2400_agent_float` + `2401_agent_deposit`). `0` means uncapped.
6. **Kill switch.** `AGENT_NETWORK_ENABLED`.

---

## 13. Test strategy (repo style)

- **Ledger invariants** (integration, `TEST_DATABASE_URL`): code and OTP single-use;
  float cap enforced atomically; per-customer daily/weekly cap enforced across agents via
  `receiver_id_hash`; idempotent redemption; expiry refund restores each account;
  concurrent redemption of the same code loses exactly once.
- **Schema guards:** no plaintext code/OTP/ID column (mirror
  `internal/store/payment_card_data_guard_test.go`); the guard must have teeth.
- **Service:** TTL/expiry/refund, ID-required, screening block stops payout,
  allowance/velocity.
- **App:** agent-console CSRF + region/agent scoping, sender flow render, kill switch.
- **Docs guard:** the runbook and this plan agree on the licence/ID-threshold lines.

---

## 14. Explicitly out of scope (v1)

Agent credit lines, international/FX remittance, a recipient mobile app, card acquiring at
agents, cash-in (D9), and any auto-enrollment without explicit consent.
