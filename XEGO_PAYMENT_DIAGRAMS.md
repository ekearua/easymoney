# Xego — Payment Engineering Diagrams

Mermaid diagrams for the three payment surfaces: **merchant payment methods**,
**P2P (individual) transfers**, and the **payment gateway**. Every label maps
to a real symbol in `internal/service`, `internal/store`, `internal/app`, and
`internal/providers/interswitch`. Render with any Mermaid viewer (GitHub,
VS Code + Markdown Preview Mermaid, or `npx @mermaid-js/mermaid-cli`).

Amounts are integer **kobo** (₦1 = 100 kobo). Fees come from
`service/fees.go`; ledger postings from `store/store_payments.go`
(transitionPayment) and `store/store_split_payments.go`.

---

## 1. System component overview

```mermaid
flowchart TB
    subgraph Channels
        WA[WhatsApp / Telegram<br/>conversational FSM]
        WF["Web flows<br/>/w/{token} wizard"]
    end

    subgraph App HTTP layer
        R[chi router]
        CO[Checkout + payment-return handlers]
        WH[Webhook handler]
    end

    subgraph Services
        CS[ConversationService<br/>session FSM + message budget]
        PS[PaymentService<br/>drafts, fees, verify, hooks]
    end

    subgraph Store["PostgreSQL store"]
        PAY[(payments + events)]
        LED[(ledger_entries<br/>chart of accounts)]
        WAL[(wallets)]
        HOOK[(payment_hooks)]
    end

    subgraph Providers
        ISW[interswitch.Client<br/>hosted checkout + requery]
        PAYOUT[NIP payout rail]
        SIM[simulators: data / identity / screening]
    end

    WA --> R
    WF --> R
    R --> CS
    R --> CO
    R --> WH
    CS --> PS
    CO --> PS
    WH --> PS
    PS --> PAY
    PS --> LED
    PS --> WAL
    PS --> HOOK
    PS --> ISW
    ISW --> PAYOUT
    PS --> SIM
```

---

## 2. Merchant payment methods ("Make payment")

One payment, three rails. The **provider** picks the fee channel
(`FeeChannelForProvider`) and the ledger flavor of money-in:

| Rail | Provider | Fee channel | Money-in posting |
|---|---|---|---|
| Card | `interswitch` | `card` | DR operating bank / CR customer float |
| Bank transfer | `bank_transfer` (Interswitch-hosted) | `dva` | same as card |
| Wallet | `wallet` | `card` | DR **payer wallet** / CR customer float (W1) |

Collection fee = `min(amount × bps/10000 + fixed, cap)`, never more than the
amount. Card defaults: 200 bps + ₦100 fixed, capped ₦3,500. DVA/bank:
150 bps, capped ₦1,500.

```mermaid
sequenceDiagram
    autonumber
    actor C as Customer
    participant M as Conversation/Web flow
    participant P as PaymentService
    participant S as Store (Postgres)
    participant G as Interswitch gateway
    participant L as Ledger

    C->>M: "Make payment" → merchant → amount
    M->>P: CreateCollectionDraft(user, merchant, base, provider)
    Note over P: draft total = base + collectionFee(provider)
    P->>S: reserve allowance (kyc tier) + create payment
    S-->>P: status awaiting_confirmation

    alt Card (interswitch) / Bank transfer (bank_transfer → Interswitch-hosted)
        M->>P: InitializeCheckout
        P->>S: SetCheckout → status initialized
        S-->>M: checkout URL /checkout/{token}
        C->>M: "Continue to secure checkout"
        M-->>G: auto-submit form → CheckoutBaseURL /collections/w/pay
        C->>G: pays on hosted page (card PIN / OTP / transfer)
        G-->>P: return hop /webhook → VerifyAndApply(ref)
        P->>G: requery gettransaction.json (signed, amount echoed)
        G-->>P: ResponseCode 00
        P->>S: transition → succeeded + C16 money-in posting
        S->>L: CR customer_float ₦(base+fee)  ↔  DR operating_bank ₦(base+fee)
        P->>S: hook "splits" (ApplyPaymentSplits)
        S->>L: DR customer_float ↔ CR merchant_payable ₦base<br/>DR customer_float ↔ CR xego_payable ₦fee
    else Wallet
        M->>P: ConfirmWalletPayment
        P->>S: reserve money-out allowance → transition → succeeded
        Note over S: DR payer wallet ₦(base+fee) ↔ CR customer_float (W1)
        P->>S: hook "splits"
        S->>L: CR merchant_payable ₦base + CR xego_payable ₦fee
    end

    M-->>C: confirmation message (message 2)
```

Worked example (card, base ₦2,500): fee = `250000×200/10000 + 10000` =
₦150 → customer charged **₦2,650**, merchant payable ₦2,500, Xego payable
₦150, books net to zero.

---

## 3. P2P transfer ("Pay an individual")

Payer sends to a recipient **bank account**; money lands in the recipient's
Xego wallet (in-platform until cash-out). Payer is charged `amount +
collectionFee` (transfer channel: 180 bps, cap ₦2,500); a flat **NIP fee
₦100** is deducted from the payout, so the recipient wallet gets
`amount − ₦100`.

```mermaid
sequenceDiagram
    autonumber
    actor A as Payer
    participant M as Conversation/Web flow
    participant P as PaymentService
    participant S as Store (Postgres)
    participant G as Interswitch gateway
    participant L as Ledger

    A->>M: "Pay an individual" → recipient phone → amount
    A->>M: recipient bank code + account number
    M->>P: CreateIndividualPayDraft(totalPay, meta)
    Note over P: payer charged amount + fee<br/>recipientGets = amount − NIP₦100
    P->>S: create payment (awaiting_confirmation)
    M->>P: InitializeCheckout (Interswitch-hosted)
    M-->>G: hosted checkout → payer completes
    G-->>P: return / webhook
    P->>G: requery (signed, amount echoed) → ResponseCode 00
    P->>S: transition → succeeded + C16 money-in posting
    S->>L: CR customer_float ↔ DR operating_bank (payer funds)
    P->>S: hook "individual_pay" (RecordIndividualPaySettlement)
    Note over S,L: journal refs "individual-pay:<payment_id>"
    S->>L: Xego collection fee CR xego_payable<br/>NIP flat fee leg<br/>recipient wallet credit (amount − ₦100)
    S-->>M: recipient wallet balance = amount − ₦100
    M-->>A: confirmation message (message 2)
```

Worked example (₦5,000, verified live in simulation): collection fee
`500000×180/10000` = ₦90 → payer charged **₦5,090**; NIP ₦100; recipient
wallet credited **₦4,900**; ledger nets to zero.

---

## 4. Payment gateway

### 4.1 Host split (why the checkout host differs from the API host)

`interswitch.Options` carries two bases after the 2026 fix: the form posts to
**`CheckoutBaseURL`** (default per mode: `newwebpay-sandbox…` TEST /
`newwebpay…` LIVE — the hosts that actually render the payment page), while
requery/webhook traffic stays on **`BaseURL`** (the API host that answers
signed `gettransaction.json`: `sandbox.interswitchng.com` TEST /
`webpay.interswitchng.com` LIVE).

```mermaid
flowchart LR
    subgraph App
        T[interswitch_checkout.html<br/>auto-submit form]
    end
    subgraph CheckoutBaseURL [CheckoutBaseURL — renders the page]
        H[newwebpay-sandbox / newwebpay<br/>/collections/w/pay]
    end
    subgraph BaseURL [BaseURL — API/requery host]
        RQ[gettransaction.json<br/>signed InterswitchAuth + echoed amount<br/>20031 if the amount is missing]
    end

    T -- POST merchant_code, pay_item_id,<br/>txn_ref, amount, currency=566, mode --> H
    H -- customer pays here --> T
    T -- callback ref --> RQ
```

### 4.2 Payment lifecycle (state machine)

`domain.CanTransition` monotonic edges (`internal/domain/payment.go`):

```mermaid
stateDiagram-v2
    [*] --> draft
    draft --> awaiting_confirmation
    awaiting_confirmation --> initialized : InitializeCheckout / SetCheckout
    awaiting_confirmation --> pending : bank-transfer initialize
    initialized --> succeeded : VerifyAndApply, requery 00
    initialized --> failed : requery decline
    pending --> succeeded : verify / confirm
    pending --> failed
    succeeded --> refunded
    draft --> expired
    awaiting_confirmation --> expired
    initialized --> expired
    pending --> expired
```

### 4.3 Confirmation pipeline

The only path that marks a payment successful is `PaymentService.VerifyAndApply`
(never the client-side redirect): signed requery → guard checks → outbox
transition → C16 money-in → idempotent post-success hooks.

```mermaid
sequenceDiagram
    autonumber
    participant H as App /payments/return or /webhooks/interswitch
    participant P as PaymentService.VerifyAndApply
    participant C as interswitch.Client
    participant S as Store

    H->>P: VerifyAndApply(reference, source)
    P->>S: PaymentByReference
    P->>C: Verify(ref, amountKobo)
    C->>C: InterswitchAuth headers (HMAC-SHA1)<br/>query: merchantcode, transactionreference, amount
    Note over C: normalize: "00" → success<br/>01/02/…/93 → failed<br/>other → pending (requery later)
    C-->>P: ports.Verification
    P->>P: validateVerification<br/>(reference, amount, currency, domain=test, payment/merchant metadata)
    alt success
        P->>S: TransitionPaymentWithOutbox → succeeded
        S->>S: C16 money-in posting (operating bank / customer float)
        P->>S: EnsurePaymentHooks + run hooks<br/>(invoice · thrift · service · event · splits · receipt_scan<br/>— replaced by wallet_topup / individual_pay)
    else failed
        P->>S: transition → failed (release allowance)
    else pending
        Note over P: no transition — reconciler requeries later
    end
```

Webhooks are authenticated separately: Interswitch signs the raw JSON body
with HMAC-SHA512 using the dashboard webhook secret
(`Client.ValidateWebhook`); a webhook is only a *signal* — the authoritative
outcome always comes from the requery above.
