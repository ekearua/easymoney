# Engineering Services — Payment Methods & Wallet Service

> **Status:** Current-state documentation grounded in the repository at `C:\Users\User\Mobile\Whatsapp Payment`.
> **Source of truth:** Go module `whatsapp-payment-demo`.

---

## 1. Overview

Xego is a Nigeria-focused merchant checkout platform where WhatsApp and Telegram chat are the primary customer interfaces, supplemented by hosted card checkout, bank-transfer instructions, request-money links, invoices, and QR receipt scanning. The platform is delivered as a single Go binary (`cmd/demo`) running a chi HTTP server, a PostgreSQL-backed store layer, a rule-based conversational engine, and background workers.

All money movements are **double-entry ledger postings** chained with SHA-256 hashes (tamper-evident, append-only, never mutated). Payments flow through a state machine (`draft` -> `awaiting_confirmation` -> `pending`/`succeeded`/`failed`/`abandoned`/`expired`/`refunded`). On terminal success, a hook pipeline applies purpose-specific business logic with at-least-once retry semantics.

### Payment rails summary

| Direction | Rail | Implementation | Status |
|---|---|---|---|
| **Inbound (collection)** | Interswitch Web Checkout (card) | `internal/providers/interswitch/client.go` | Real HTTP, test-key only |
| **Inbound (collection)** | Bank transfer (Interswitch checkout) | `internal/providers/interswitch/client.go` (provider `bank_transfer`) | Real HTTP, test-key only |
| **Inbound (collection)** | Wallet payment | `internal/store/store_wallets.go` (ledger debit, no gateway) | Implemented |
| **Outbound (payout)** | NIBSS/NIP bank transfer | `internal/providers/payout/simulated.go` | Simulated (no live rail) |

**Key architectural point:** Interswitch is **purely inbound** — it collects money via card checkout but does not disburse. Outbound payouts go through the `PayoutProvider` interface, which currently has only a simulated implementation.

### All payment methods at a glance

| # | Method | Provider | Fee Channel | Confirmation |
|---|---|---|---|---|
| 1 | Card checkout | `interswitch` | `card` | Interswitch requery + webhook |
| 2 | Bank transfer | `bank_transfer` | `dva` | Interswitch requery + webhook |
| 3 | Wallet payment | `wallet` | `card` | Atomic wallet debit |
| 4 | Individual pay (P2P) | `bank_transfer` | `transfer` | Interswitch requery + settlement hook |
| 5 | Wallet top-up | Any gateway | Per provider | Gateway verification + top-up hook |
| 6 | Checkout link | `interswitch` | `card` | Interswitch requery |
| 7 | Invoice payment | Any | Per provider | Gateway verification + invoice hook |
| 8 | Thrift contribution | Any | Per provider | Gateway verification + thrift hook |
| 9 | Data bundle purchase | Any | Per provider | Gateway verification + service hook |
| 10 | Service purchase | Any (user choice) | Per choice | Gateway verification + service hook |
| 11 | Event ticket purchase | Any (user choice) | Per choice | Gateway verification + event hook |

*Source: `internal/service/fees.go`, `internal/service/payments.go`, `internal/app/app.go`*

> **Flow-surface note (Sep 2026):** The trigger paths in this section describe the
> conversational FSM. Since the two-message web-flows rollout (`7d70ce5`), WhatsApp
> runs pay, invoice, thrift, data, top-up, individual-pay, and onboarding flows in
> the `/w/{token}` web wizard when `WEB_FLOWS_ENABLED` (default); Telegram/SMS and
> the flag-off fallback keep the chat steps below. Both surfaces call the same
> service methods, so the ledger postings and hooks are identical.

---

## 2. Payment Methods

### 2.1 Card Checkout (Interswitch Web Checkout)

The customer pays via an Interswitch-hosted secure payment page. The gateway handles PCI-DSS scope — the platform never sees card details.

**Trigger paths:**
- WhatsApp chat: user selects "Card checkout" -> `select_payment_method` -> `confirm_payment` -> `InitializeCheckout()`
- API: `POST /api/v1/payments`
- Checkout link: `ResolveCheckout()` defaults to provider `interswitch`

**Provider:** `ProviderInterswitch = "interswitch"` — registered in `internal/app/app.go:108`

**Fee channel:** `card` — `fee = min(amount * 200/10000 + 10000, 350000)` (default config)

**Ledger postings on success:**

```
Money-in:
  DR 1100_operating_bank    (amountKobo)
  CR 2100_customer_float    (amountKobo)

Collection splits (post-success hook):
  DR 2100_customer_float    (feeKobo)
  CR 2300_xego_payable      (feeKobo)

  DR 2100_customer_float    (merchantReceivable)
  CR 3100_merchant_payable  (merchantReceivable)
```

**Confirmation flow:**
1. `InitializeCheckout()` calls `gateway.Initialize()` -> returns platform form page URL `/checkout/interswitch/{reference}`
2. Auto-submit form posts to Interswitch `/collections/w/pay`
3. Interswitch fires signed JSON webhook (`TRANSACTION.COMPLETED`) to `/webhooks/interswitch`
4. `VerifyAndApply()` calls `gateway.Verify()` (server-side requery to `gettransaction.json`, InterswitchAuth-signed)
5. Gateway status mapped: `ResponseCode "00"` -> `success` -> `domain.StatusSucceeded`
6. Background reconciler (`Reconcile`) retries unresolved payments after 30s

*Sources: `internal/providers/interswitch/client.go`, `internal/service/payments.go`, `internal/app/webhooks.go`, `internal/app/checkout.go`*

---

### 2.2 Bank Transfer (Interswitch-Routed)

The customer pays by bank transfer through the Interswitch-hosted checkout. The transfer is verified server-side — `gettransaction.json` requery plus the signed `TRANSACTION.COMPLETED` webhook — before the payment succeeds. The mechanics mirror card checkout (Section 2.1); the difference is the fee channel (`dva`) and that the bank transfer is completed on the Interswitch-hosted payment page.

**Trigger paths:**
- WhatsApp/Telegram chat: "Bank transfer" on any payment method picker -> review -> `confirm_payment` -> `InitializeCheckout()`
- Web flow / API: method `bank_transfer` on the same `CreateCollectionDraft` path

**Provider:** `ProviderBankTransfer = "bank_transfer"` — registered at `internal/app/app.go:109` to the same Interswitch client instance.

**Fee channel:** `dva` — `fee = min(amount * 150/10000, 150000)` (default config)

**Ledger postings on success (identical to card):**

```
Money-in:
  DR 1100_operating_bank    (amountKobo)
  CR 2100_customer_float    (amountKobo)

Collection splits (post-success hook):
  DR 2100_customer_float    (feeKobo)
  CR 2300_xego_payable      (feeKobo)

  DR 2100_customer_float    (merchantReceivable)
  CR 3100_merchant_payable  (merchantReceivable)
```

**Confirmation flow:**
1. Customer confirms the review -> `InitializeCheckout()` calls `gateway.Initialize()` -> platform form page `/checkout/interswitch/{reference}`
2. Auto-submit form posts to Interswitch; the customer completes the bank transfer on the hosted page
3. Interswitch fires a signed JSON webhook (`TRANSACTION.COMPLETED`) to `/webhooks/interswitch`
4. `VerifyAndApply()` calls `gateway.Verify()` (server-side requery of `gettransaction.json`, InterswitchAuth-signed)
5. `ResponseCode "00"` -> `success` -> `succeeded`; the money-in posting and collection splits run atomically with the transition
6. Background reconciler (`Reconcile`) retries unresolved payments after 30s

**History:** before commit `7ae7367` the rail was simulated in-app: the customer picked a Xego collection bank, received transfer instructions with a unique payment reference, and tapped "I have transferred" to confirm. That path is retired — no conversation state reaches it — and the simulation helpers (`InitializeBankTransferSimulation`, `ConfirmBankTransferSimulation`) remain in code as unreachable legacy only.

*Sources: `internal/service/payments.go`, `internal/store/store_payments.go`, `internal/service/conversation_payment.go`*

---

### 2.3 Wallet Payment

The customer pays directly from their Xego wallet balance. No external gateway is called. The wallet is debited atomically with the payment success transition.

**Trigger path:** WhatsApp chat -> user selects "Pay from wallet" -> review shows current balance -> confirm

**Provider:** `ProviderWallet = "wallet"` — **not registered as a gateway** (no external call needed)

**Fee channel:** `card` — same formula as card checkout

**Ledger postings on success:**

```
Money-in (wallet debit instead of bank):
  DR 2301_user_wallet:<payer-uuid>  (amountKobo)
  CR 2100_customer_float            (amountKobo)

Collection splits (same as card):
  DR 2100_customer_float    (feeKobo)
  CR 2300_xego_payable      (feeKobo)

  DR 2100_customer_float    (merchantReceivable)
  CR 3100_merchant_payable  (merchantReceivable)
```

**Confirmation flow:**
1. Draft created (no money-in allowance reservation since funds are already in the wallet)
2. Customer taps "Pay from wallet" -> `ConfirmWalletPayment()` called
3. C9 money-**out** allowance reserved for the payer
4. `TransitionPaymentWithOutbox()` atomically checks wallet balance, debits wallet, credits customer float
5. If transition fails, money-out reservation is released

**Constraints:** Wallet must be `active` (L1+ KYC) with sufficient balance.

*Sources: `internal/store/store_wallets.go`, `internal/service/payments.go`, `internal/service/conversation_payment.go`*

---

### 2.4 Individual Pay (P2P Transfer)

A sender transfers money to another individual's bank account. The sender pays the transfer amount plus a collection fee via hosted checkout. The recipient's share (minus NIP fee) is credited to their internal wallet for later withdrawal. This is a two-leg flow: money-in from sender, money-out to recipient.

**Prerequisite:** Sender must have completed KYC Level 2 (`individual` account level).

**Trigger path:** WhatsApp chat -> "pay individual" -> `pay_individual_phone` -> `pay_individual_amount` -> `pay_individual_bank_code` -> `pay_individual_account` -> `pay_individual_confirm` -> hosted checkout

**Provider:** `ProviderBankTransfer` (sender's payment goes through Interswitch gateway). System merchant: `xego-individual-pay` (inactive internal merchant).

**Fee channel:** `transfer` for collection fee + flat NIP fee for payout

**Fee breakdown:**
- Collection fee: `fee = min(amount * 180/10000, 250000)`
- NIP payout fee: flat 10,000 kobo (NGN 100)
- Total sender pays: `amountKobo + collectionFee`
- Recipient receives: `amountKobo - nipFee`

**Ledger postings on sender's payment success:**

```
Money-in:
  DR 1100_operating_bank    (totalPay = amount + collectionFee)
  CR 2100_customer_float    (totalPay)

Settlement splits (post-success hook individual_pay):
  DR 2100_customer_float    (collectionFee)
  CR 2300_xego_payable      (collectionFee)

  DR 2100_customer_float    (nipFee)
  CR 2300_xego_payable      (nipFee)

  DR 2300_user_payable      (recipientGets = amount - nipFee)
  CR 2301_user_wallet:<recipient-uuid>  (recipientGets)
```

**Confirmation flow:**
1. `CreateIndividualPayDraft()` creates draft against `xego-individual-pay` system merchant
2. Hosted checkout URL generated; sender pays via Interswitch
3. On gateway verification success -> `applyIndividualPaySettlement()` hook runs:
   - Resolves/creates recipient user
   - Saves payout destination
   - Books split legs (collection fee + NIP fee)
   - Credits recipient's wallet (UserPayable -> user wallet)
4. Recipient withdraws from wallet via the payout rail

**Key point:** The recipient does NOT receive bank credit directly. Money sits in their Xego wallet until they initiate a withdrawal.

*Sources: `internal/service/conversation_individual_pay.go`, `internal/store/store_split_payments.go`, `internal/service/fees.go`*

---

### 2.5 Wallet Top-Up

A customer loads funds into their Xego wallet by paying through a gateway. Once confirmed, the customer float is transferred to the user's wallet account.

**Trigger path:** WhatsApp chat -> "top up wallet" flow

**Provider:** Any registered gateway. System merchant: `xego-wallet-topup` (inactive internal merchant). Payment metadata: `{"wallet_topup": {"amount_kobo": N}}`

**Fee channel:** Depends on the provider used for the gateway checkout.

**Ledger postings on success:**

```
Money-in:
  DR 1100_operating_bank    (amountKobo)
  CR 2100_customer_float    (amountKobo)

Wallet top-up hook:
  DR 2100_customer_float    (amountKobo)
  CR 2301_user_wallet:<user-uuid>  (amountKobo)
```

**Confirmation flow:**
1. `CreateWalletTopupDraft()` creates payment against `xego-wallet-topup` system merchant
2. Standard gateway checkout (card/bank transfer)
3. On gateway verification success -> `wallet_topup` hook fires (instead of standard hooks)
4. `ApplyWalletTopup()` credits payer's wallet from customer float
5. Serialized against refunds via `FOR UPDATE` lock on the payment row

*Sources: `internal/store/store_wallets.go`, `internal/service/payments.go`*

---

### 2.6 Checkout Link (Request Money)

A payee merchant creates a request-money link. Any person who opens the link pays via card checkout. No customer is bound at link creation; the payer is resolved when the link is opened.

**Trigger path:** API: `POST /api/v1/checkouts` -> link `/link/{token}` shared

**Provider:** Always `ProviderInterswitch` (card checkout). `ResolveCheckout()` creates a `CreateCollectionDraft` with provider `interswitch` and channel `checkout`.

**Fee channel:** `card` — standard card fee formula.

**Ledger postings:** Same as card checkout (collection splits applied via the `splits` hook).

**Confirmation flow:**
1. Merchant calls `CreateCheckout()` with payee and amount
2. Payer opens link -> `ResolveCheckout()` called with payer's phone
3. Idempotent: if a non-terminal payment exists for this checkout, it is returned
4. Otherwise, a new `CreateCollectionDraft` is created and linked
5. Standard Interswitch hosted checkout flow follows

*Sources: `internal/app/checkout.go`, `internal/service/payments.go`*

---

### 2.7 Invoice Payment

A customer pays against a merchant-created invoice. Supports partial payments and installments. Invoice payments skip collection splits — funds are allocated directly to merchant payable.

**Trigger path:** Web: `/invoices/{reference}` page. API: `POST /api/v1/invoices`

**Fee channel:** Depends on the provider used.

**Ledger postings:**

```
Money-in:
  DR 1100_operating_bank    (paidKobo)
  CR 2100_customer_float    (paidKobo)

Invoice allocation:
  DR 2100_customer_float    (paidKobo)
  CR 3100_merchant_payable  (paidKobo)
```

**Post-success hook:** `PaymentHookInvoice` -> `ApplyInvoicePaymentSuccess()` — handles invoice status updates, installment tracking, partial payment allocation.

*Sources: `internal/service/conversation_invoice.go`, `internal/store/store_invoices.go`*

---

### 2.8 Thrift Contribution

Members contribute to a thrift group pool. Contributions are pooled and periodically disbursed to members. Thrift payments skip collection splits.

**Trigger path:** Web: `/thrift/{name}` page. WhatsApp: conversation flow.

**Ledger postings:**

```
Money-in:
  DR 1100_operating_bank    (contributionKobo)
  CR 2100_customer_float    (contributionKobo)

Thrift allocation:
  DR 2100_customer_float    (contributionKobo)
  CR 6100_thrift_pool       (contributionKobo)

Thrift payout (disbursement):
  DR 6100_thrift_pool       (payoutKobo)
  CR 1100_operating_bank    (payoutKobo)
```

**Post-success hook:** `PaymentHookThrift` -> `ApplyThriftContributionPaymentSuccess()`.

*Sources: `internal/service/conversation_thrift.go`, `internal/store/store_thrifts.go`*

---

### 2.9 Data Bundle Purchase

Customer purchases a mobile data bundle (MTN, Airtel, Glo, 9mobile) through a VTU/data provider. Data orders skip collection splits. Revenue is recognized directly.

**Trigger path:** WhatsApp chat -> data purchase conversation flow.

**Provider:** Configurable: `"simulated"` (default) or `"vtpass"`.

**Ledger postings:**

```
Money-in:
  DR 1100_operating_bank    (amountKobo)
  CR 2100_customer_float    (amountKobo)

Revenue recognition:
  DR 2100_customer_float    (amountKobo)
  CR 4100_sales_revenue     (amountKobo)
```

**Post-success hook:** `PaymentHookService` -> `ConfirmServicePurchase()` (data orders share this hook path with service purchases).

*Sources: `internal/service/conversation_data.go`, `internal/providers/vtpass/`*

---

### 2.10 Service Purchase

Customer purchases a merchant-defined service (e.g., "Consultation", "Delivery"). Quantity-based with optional custom fields. Fee channel depends on the payment method chosen.

**Trigger path:** WhatsApp chat -> user selects service from list -> enters quantity -> provides custom fields -> picks payment method (card/wallet/bank transfer)

**Ledger postings:** Same as the chosen payment method's collection splits.

**Post-success hook:** `PaymentHookService` -> `ConfirmServicePurchase()`.

*Sources: `internal/service/conversation_data.go` (shared flow), `internal/store/store_services.go`*

---

### 2.11 Event Ticket Purchase

Customer purchases tickets to a merchant-created event. Supports multiple tiers with capacity tracking and optional custom fields per tier. Fee channel depends on the chosen payment method.

**Trigger path:** WhatsApp chat -> user selects event -> picks ticket tier -> enters quantity -> provides custom fields -> picks payment method

**Ledger postings:** Same as the chosen payment method's collection splits.

**Post-success hook:** `PaymentHookEvent` -> `ConfirmEventTicketPurchase()` — decrements tier capacity and marks the ticket purchase confirmed.

*Sources: `internal/store/store_events.go`*

---

## 3. Wallet Service

### 3.1 Architecture

The wallet system is a **ledger-native construct** — there is no stored balance column. A wallet's balance is always computed as `SUM(credits) - SUM(debits)` from the append-only `ledger_entries` table. Every wallet operation posts a balanced debit+credit pair under a shared `journal_ref`, making the system idempotent (replays are no-ops) and auditable.

*Source: `internal/store/store_wallets.go`, `internal/store/store_ledger.go`*

### 3.2 Wallet Types

| Owner Type | Account Code Pattern | Created At | Initial Status |
|---|---|---|---|
| `user` (individual) | `2301_user_wallet:<user-uuid>` | L0 user creation (`EnsureKYCProfile`) | `pending` |
| `business` (merchant) | `3101_business_wallet:<merchant-uuid>` | KYB verification | `active` |

*Source: `internal/store/store_wallets.go`*

### 3.3 Lifecycle

| Status | Meaning | Money-In | Money-Out |
|---|---|---|---|
| `pending` | Awaiting L1 activation (individual) | Allowed | Blocked |
| `active` | L1+ individual or verified business | Allowed | Allowed |
| `frozen` | Operator-frozen | Allowed | Blocked |

**Key rule:** Money-in is **always** allowed regardless of wallet status — a customer is never blocked from receiving funds. Money-out requires `active` status and sufficient balance.

**Transitions:**
- `pending` -> `active`: `ActivateUserWallet()` called when KYC reaches L1
- `active`/`pending` -> `frozen`: operator action
- `frozen` -> `active`: operator unfreeze

*Source: `internal/store/store_wallets.go`*

### 3.4 Money-In (Credits)

| Source | Function | Ledger Posting |
|---|---|---|
| Wallet top-up | `ApplyWalletTopup()` | `DR 2100_customer_float / CR 2301_user_wallet:<id>` |
| Individual pay recipient | `CreditUserWallet()` | `DR 2300_user_payable / CR 2301_user_wallet:<id>` |
| Refund reversal | `CreditUserWallet()` | `DR <source-account> / CR 2301_user_wallet:<id>` |

All credits go through `creditWalletTx()` which:
1. Ensures wallet exists (creates as `pending` if missing)
2. Checks idempotency via `journal_ref`
3. Requires `amountKobo > 0`
4. Posts balanced ledger pair

*Source: `internal/store/store_wallets.go`*

### 3.5 Money-Out (Withdrawals)

**Function:** `WithdrawFromUserWallet(ctx, userID, amountKobo, journalRef, description)`

**Pre-checks (in order):**
1. Wallet exists and is `active` (otherwise `ErrWalletNotActive`)
2. Sufficient balance: `balance >= amountKobo` (otherwise `ErrInsufficientWalletBalance`)
3. Idempotency: if `journal_ref` already posted, returns immediately (no-op)

**Ledger posting:**
```
DR 2301_user_wallet:<user-uuid>  (amountKobo)
CR 1100_operating_bank           (amountKobo)
```

**KYC enforcement:** Money-out allowance is reserved via `ReserveAllowance` (C9 tier system) before the withdrawal. If the tier ceiling is exceeded, the withdrawal is blocked.

*Source: `internal/store/store_wallets.go`*

### 3.6 Balance Calculation

```sql
SELECT COALESCE(SUM(
  CASE WHEN entry_type='credit' THEN amount_kobo ELSE -amount_kobo END
), 0)
FROM ledger_entries
WHERE account = '<wallet_account_code>'
```

Since the wallet is a liability account, credits increase the balance and debits decrease it. The balance is **never stored denormalized** — it is always computed from the ledger.

*Source: `internal/store/store_wallets.go:WalletBalance()`*

### 3.7 KYC Tier Allowance Enforcement

The platform enforces tiered money-in and money-out limits through the `ReserveAllowance` / `ReleaseAllowance` mechanism:

- **Money-in:** Reserved at payment draft creation (all providers **except** wallet — wallet money is already in the platform). Released on failure, abandonment, expiration, or refund.
- **Money-out:** Reserved at confirmation time for wallet payments (payer side) and settlement payouts (merchant side). Released on failure.
- **Individual pay recipient:** Money stays inside the platform (credited to recipient wallet). Money-out ceiling is enforced at withdrawal time, not at settlement time.

*Source: `internal/kyc/allowances.go`*

---

## 4. Fee Model

### 4.1 Collection Fee Channels

Each payment provider maps to a fee channel via `FeeChannelForProvider()`:

| Provider | Fee Channel |
|---|---|
| `interswitch` | `card` |
| `bank_transfer` | `dva` |
| `wallet` | `card` |
| Other/unknown | returns the provider name as channel (zero fee) |

*Source: `internal/service/fees.go`*

### 4.2 Fee Formula

```
fee = min(bps * amountKobo / 10000 + fixed, cap)
```

If `fee > amountKobo`, fee is clamped to `amountKobo`. If `amountKobo <= 0`, fee is zero.

### 4.3 Channel Parameters

| Channel | BPS | Fixed (kobo) | Cap (kobo) | Env Vars |
|---|---|---|---|---|
| `card` | 200 (2.0%) | 10,000 (NGN 100) | 350,000 (NGN 3,500) | `XEGO_FEE_CARD_BPS`, `XEGO_FEE_CARD_FIXED_KOBO`, `XEGO_FEE_CARD_CAP_KOBO` |
| `dva` | 150 (1.5%) | 0 | 150,000 (NGN 1,500) | `XEGO_FEE_DVA_BPS`, `XEGO_FEE_DVA_FIXED_KOBO`, `XEGO_FEE_DVA_CAP_KOBO` |
| `transfer` | 180 (1.8%) | 0 | 250,000 (NGN 2,500) | `XEGO_FEE_TRANSFER_BPS`, `XEGO_FEE_TRANSFER_FIXED_KOBO`, `XEGO_FEE_TRANSFER_CAP_KOBO` |

### 4.4 Worked Examples

All amounts below are kobo (1 NGN = 100 kobo). The formula is
`fee = bps * amountKobo / 10000 + fixed`, capped at the channel cap and clamped
to the full amount when the computed fee would exceed it.

**Card checkout, NGN 10,000 (1,000,000 kobo):**
```
fee = 1,000,000 * 200 / 10,000 + 10,000 = 20,000 + 10,000 = 30,000 kobo (NGN 300)
Cap check: 30,000 <= 350,000 -- no cap
Merchant receives: 1,000,000 - 30,000 = 970,000 kobo (NGN 9,700)
```

**Card checkout, NGN 50,000 (5,000,000 kobo):**
```
fee = 5,000,000 * 200 / 10,000 + 10,000 = 100,000 + 10,000 = 110,000 kobo (NGN 1,100)
Merchant receives: 5,000,000 - 110,000 = 4,890,000 kobo (NGN 48,900)
```

**DVA/bank transfer, NGN 20,000 (2,000,000 kobo):**
```
fee = 2,000,000 * 150 / 10,000 + 0 = 30,000 kobo (NGN 300)
Merchant receives: 2,000,000 - 30,000 = 1,970,000 kobo (NGN 19,700)
```

**Clamp rule, NGN 100 (10,000 kobo) via card:** the formula yields 10,200 kobo,
which exceeds the amount, so the fee is clamped to the full 10,000 kobo and the
merchant receives 0.

---
### 4.5 Payout Fee

A flat NIP payout fee is deducted from the payout amount:

```
nipFee = min(cfg.FeeNIPPayoutFlatKobo, amountKobo)
```

Default: 10,000 kobo (NGN 100). Env var: `XEGO_FEE_NIP_PAYOUT_FLAT_KOBO`.

For individual pay: the recipient receives `amountKobo - nipFee`.

### 4.6 Settlement Fee

Applied at batch cut time:

```
feeKobo = totalKobo * settlementFeeBps / 10000
```

Default: 250 bps (2.5%). Env var: `SETTLEMENT_FEE_BPS`.

*Sources: `internal/service/fees.go`, `internal/config/config.go`*

---

## 5. Ledger Architecture

### 5.1 Chart of Accounts

| Code | Constant | Type | Description |
|---|---|---|---|
| `1100_operating_bank` | `LedgerAccountOperatingBank` | Asset | External bank where fiat is held |
| `1300_settlement_suspense` | `LedgerAccountSettlementSuspense` | Asset | Suspense during settlement cuts |
| `2100_customer_float` | `LedgerAccountCustomerFloat` | Liability | Customer funds held in transit |
| `2300_user_payable` | `LedgerAccountUserPayable` | Liability | Money owed to individual users |
| `2300_xego_payable` | `LedgerAccountXegoPayable` | Liability | Platform fees/revenue payable |
| `2301_user_wallet:<uuid>` | `LedgerAccountUserWallet` + ID | Liability | Per-user wallet (dynamic code) |
| `3100_merchant_payable` | `LedgerAccountMerchantPayable` | Liability | Money owed to merchants |
| `3101_business_wallet:<uuid>` | `LedgerAccountBusinessWallet` + ID | Liability | Per-business wallet (dynamic code) |
| `3200_settlement_payable` | `LedgerAccountSettlementPayable` | Liability | Frozen payable in settlement batch |
| `4100_sales_revenue` | `LedgerAccountSalesRevenue` | Revenue | Recognized sales revenue |
| `5100_provider_cost` | `LedgerAccountProviderCost` | Expense | Payment provider costs |
| `5200_settlement_fees` | `LedgerAccountSettlementFees` | Expense | Settlement fee expense |
| `6100_thrift_pool` | `LedgerAccountThriftPool` | Liability | Thrift group pooled funds |

### 5.2 Posting Rules

- Every money movement is a balanced **debit + credit pair** under a shared `journal_ref`
- All postings are **idempotent** on `journal_ref` — replays are no-ops
- The `postLedgerPair` function writes both entries in a single transaction
- Merchant-scoped postings include a `merchant_id` column for isolation

### 5.3 Hash Chain (Tamper-Evidence)

Each row in `ledger_entries` includes a SHA-256 hash computed from:
```
hash = SHA256(journal_ref + account + entry_type + amount_kobo + previous_hash)
```

This creates a chain where modifying any historical row would break all subsequent hashes. The chain is verified by the admin audit tool at `/admin/audit`.

### 5.4 Reversals

Refunds and corrections use `PostLedgerReversal` which writes **offsetting entries** for every row in a journal reference:
- Debit becomes credit and vice versa
- Each offsetting row is linked to the original via `reversal_of`
- Original rows are **never mutated**

*Sources: `internal/store/store_ledger.go`, `internal/app/ledger.go`*

---

## 6. Settlement & Payout

### 6.1 Batch Cut (CutSettlement)

`CutSettlement(ctx, merchantID, batchNo, cutoffAt, feeBps)` freezes the merchant's accrued `3100_merchant_payable` into a settlement batch.

**Fee formula:** `feeKobo = totalKobo * feeBps / 10000` (default 2.5%)

**Ledger postings:**

```
DR 3100_merchant_payable   (totalKobo)     -- Move liability to settlement
CR 3200_settlement_payable (totalKobo)

DR 3200_settlement_payable (feeKobo)       -- Deduct settlement fee
CR 5200_settlement_fees    (feeKobo)

DR 3200_settlement_payable (netKobo)       -- Credit business wallet
CR 3101_business_wallet:<merchant>  (netKobo)
```

**Idempotency:** Replaying a known `batchNo` returns it unchanged.

*Source: `internal/service/settlement.go`, `internal/store/store_settlements.go`*

### 6.2 Payout Request & Dispatch

`RequestPayout(ctx, batchNo, destinationID)` creates a payout and dispatches it.

**Pre-checks (in order):**
1. Batch exists and is `open` or `scheduled`
2. Payout amount >= `PAYOUT_MIN_KOBO` (default NGN 100)
3. Payout amount <= `PAYOUT_MAX_KOBO` (default NGN 10,000)
4. Daily count < `PAYOUT_DAILY_COUNT_LIMIT` (default 10/day)
5. Daily volume + payout amount <= `PAYOUT_DAILY_CAP_KOBO` (default NGN 50,000/day)
6. KYC tier money-out allowance reserved via `ReserveAllowance`

**Dispatch flow:**
1. Payout claimed for processing (prevents concurrent dispatch)
2. `PayoutProvider.Payout()` called with bank details
3. On success: `CompletePayout()` with external reference
4. On failure: `FailPayout()` with error message
5. On pending: `ErrPayoutPending` -> payout stays queued for later retry

**Payout statuses:** `queued` -> `processing` -> `completed` / `failed` / `reversed`

**Payout max attempts:** 3 (`PayoutMaxAttempts`) before operator action required.

*Source: `internal/service/settlement.go`*

### 6.3 PayoutProvider Interface

```go
type PayoutProvider interface {
    Payout(ctx context.Context, request PayoutRequest) (PayoutResult, error)
}
```

**Current implementation:** `payout.SimulatedProvider` only — returns `succeeded` for all bank codes except `"011"` (First Bank), which returns `failed`. No live NIBSS/NIP integration exists.

**PayoutRequest fields:** `Reference`, `BankCode`, `AccountNumber`, `AccountName`, `AmountKobo`, `Narration`

*Source: `internal/ports/ports.go`, `internal/providers/payout/simulated.go`*

### 6.4 Worker Loop

`runSettlementDispatcher` runs every **10 seconds** (via `settlementTicker` in `workers.go`):
1. `DispatchPending()` claims up to 50 queued payouts and processes them
2. `RequeueStalePayouts()` recovers payouts stuck in `processing` state (crash recovery)

*Source: `internal/app/workers.go`*

---

## 7. Provider Registry

### 7.1 PaymentGateway Interface

```go
type PaymentGateway interface {
    Initialize(ctx context.Context, payment InitializePayment) (Checkout, error)
    Verify(ctx context.Context, reference string) (Verification, error)
    ValidateWebhook(body []byte, signature string) (GatewayWebhook, error)
}
```

**Registered gateways:**

| Key | Implementation | Used By |
|---|---|---|
| `interswitch` | `interswitch.Client` | Card checkout |
| `bank_transfer` | `interswitch.Client` (same instance) | Bank-transfer checkout (hosted Interswitch) |

**Not registered:** `wallet` — wallet payments never call an external gateway.

*Source: `internal/ports/ports.go`, `internal/app/app.go`*

### 7.2 PayoutProvider Interface

```go
type PayoutProvider interface {
    Payout(ctx context.Context, request PayoutRequest) (PayoutResult, error)
}
```

**Registered:** `payout.SimulatedProvider` (default, no live rail)

### 7.3 RefundProvider Interface

```go
type RefundProvider interface {
    Refund(ctx context.Context, request RefundRequest) (RefundResult, error)
}
```

**Registered:** `refund.SimulatedProvider` (default, no live rail)

### 7.4 Provider Router

`ProviderRouter` selects the healthiest gateway based on:
- **EWMA latency** tracking per provider
- **Consecutive error count** — marks provider unhealthy after 3 consecutive errors
- **Failover:** if all are unhealthy, picks the one with fewest consecutive errors (best-effort)

Used when `provider == "auto"` in the payment creation flow.

*Source: `internal/service/provider_router.go`*

---

## 8. Post-Success Hook Pipeline

### 8.1 Standard Hook Chain

When a payment transitions to `succeeded`, hooks execute in this order:

| Order | Hook | Purpose |
|---|---|---|
| 1 | `invoice` | Invoice payment allocation, installment tracking |
| 2 | `thrift` | Thrift contribution allocation |
| 3 | `service` | Service purchase confirmation |
| 4 | `event` | Event ticket purchase confirmation (decrements capacity) |
| 5 | `splits` | Collection fee + merchant receivable ledger postings |
| 6 | `receipt_scan` | Receipt scan QR code generation |

*Source: `internal/store/store_payment_hooks.go`*

### 8.2 Specialized Hook Chains

| Payment Type | Hooks Executed |
|---|---|
| Standard collection (card, bank transfer, wallet) | `invoice`, `thrift`, `service`, `event`, `splits`, `receipt_scan` |
| Wallet top-up | `wallet_topup` only |
| Individual pay | `individual_pay` only |
| Invoice/thrift/data payments | `invoice`/`thrift`/`service` + skip `splits` (their ledger flows handle allocation directly) |

### 8.3 Retry Semantics

- Hooks tracked in `payment_hooks` table with status (`pending`/`done`/`failed`), attempt count, and next retry time
- **Backoff:** exponential, `delay = 2^attempts` seconds, capped at 15 minutes
- **Initial delay:** 1 minute (prevents worker pickup while inline chain is still running)
- **Worker batch:** `PendingPaymentHooks` fetches up to 50 hooks ordered by `next_retry_at ASC`
- **Max attempts:** 8 (`maxPaymentHookAttempts`) — after which the hook is marked `failed` for manual review
- **Idempotency:** all hooks check `payment_hooks` for existing `done` status before re-executing

### 8.4 Idempotency

- All hook executions are keyed by `payment_id + hook_name`
- A `done` hook is never re-executed
- A `failed` hook with attempts >= 8 is permanently failed
- Ledger postings within hooks are idempotent on `journal_ref`

*Source: `internal/store/store_payment_hooks.go`, `internal/service/payments.go`*
