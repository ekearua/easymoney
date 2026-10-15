# Xego Engineering Services Document

> **Scope:** Card Payments, Bank Transfers, and Payment Gateway — current implementation.
> **Codebase:** `whatsapp-payment-demo` (Go 1.26, PostgreSQL, Chi router)

---

## 1. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                        Customer Touchpoints                         │
│  WhatsApp Chat    Telegram Bot    Hosted Checkout Page    Partner API│
└──────────┬───────────────┬────────────────┬───────────────┬─────────┘
           │               │                │               │
           ▼               ▼                ▼               ▼
┌──────────────────────────────────────────────────────────────────────┐
│                     Conversation / API Layer                         │
│  internal/app/ (HTTP handlers, webhooks)                            │
│  internal/service/conversation_*.go (FSM, rule-based engine)         │
└──────────┬───────────────────────────────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      PaymentService                                  │
│  internal/service/payments.go                                       │
│  - Draft creation, gateway init, verification, fee splits           │
│  - Orchestrates all three rails: card, bank_transfer, auto          │
└──────┬──────────────────┬────────────────────────────────────────────┘
       │                  │
       ▼                  ▼
┌──────────────┐  ┌──────────────────────────────────────────────────┐
│   Provider   │  │              Payment Gateway Layer                │
│   Router     │  │  internal/ports/ports.go (PaymentGateway iface)  │
│  (auto mode) │  │  ├── paystack/client.go  (card-only checkout)   │
│  EWMA-based  │  │  └── flutterwave/client.go (card-only checkout)  │
└──────────────┘  └──────────────────────────────────────────────────┘
       │
       ▼
┌──────────────────────────────────────────────────────────────────────┐
│                     Double-Entry Ledger                              │
│  internal/store/store_ledger.go                                     │
│  - Append-only, SHA-256 hash-chained                                │
│  - PostgreSQL trigger rejects UPDATE/DELETE on ledger_entries        │
│  - Balanced debit+credit pairs per journal_ref                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 2. Domain Model

### 2.1 Payment State Machine

**Source:** `internal/domain/payment.go:20-38`

```
draft ──▶ awaiting_confirmation ──▶ initialized ──▶ pending ──▶ succeeded ──▶ refunded
                                │                    │           │
                                ├────────────────────┼───────────┤──▶ failed
                                │                    │           │──▶ abandoned
                                └────────────────────┴───────────┘──▶ expired
```

| Status | Meaning |
|---|---|
| `draft` | Payment record created, not yet visible to customer |
| `awaiting_confirmation` | Customer sees payment details, needs to confirm (card) or select bank (transfer) |
| `initialized` | Gateway checkout page created (card only) |
| `pending` | Awaiting external signal — gateway verification or customer confirmation |
| `succeeded` | Payment verified/confirmed, ledger posted, splits applied |
| `failed` | Provider returned failure or reversal |
| `abandoned` | Customer closed checkout without paying |
| `expired` | Session TTL exceeded (`expireStale` runs periodically) |
| `refunded` | Refund processed (terminal, no further transitions) |

### 2.2 Payment Aggregate

**Source:** `internal/domain/payment.go:148-168`

```go
type Payment struct {
    ID                uuid.UUID
    UserID            uuid.UUID
    MerchantID        uuid.UUID
    AmountKobo        int64          // Amount in kobo (NGN × 100)
    Currency          string         // Always "NGN"
    Status            PaymentStatus
    Provider          string         // "paystack" | "flutterwave" | "bank_transfer"
    ProviderReference string         // "wpd_<uuid-no-dashes>" — unique per attempt
    Channel           string         // "whatsapp" | "telegram" | "checkout" | "api"
    Recipient         string         // Phone/number to send receipt
    CheckoutURL       string         // Hosted gateway URL (card only)
    CheckoutToken     string         // Bearer token for /checkout/{token}
    ReceiptToken      string         // Capability token for /receipts/{token}
    MerchantReference string         // Merchant-supplied reference
    Metadata          json.RawMessage
    FailureReason     string
    CreatedAt         time.Time
    UpdatedAt         time.Time
    PaidAt            *time.Time
}
```

**Note:** There are zero card-data fields. No PAN, CVV, expiry, or card tokens are stored.

---

## 3. Service 1: Card Payments

### 3.1 Provider-Neutral Gateway Interface

**Source:** `internal/ports/ports.go:47-52`

```go
type PaymentGateway interface {
    Initialize(ctx context.Context, input InitializePayment) (Checkout, error)
    Verify(ctx context.Context, reference string) (Verification, error)
    ValidateWebhook(body []byte, signature string) (GatewayWebhook, error)
}
```

**Data contracts:**

| Type | Fields |
|---|---|
| `InitializePayment` | `Reference`, `Email`, `AmountKobo`, `Currency`, `CallbackURL`, `Metadata map[string]string` |
| `Checkout` | `Reference`, `URL` |
| `Verification` | `Reference`, `Status`, `AmountKobo`, `Currency`, `Domain`, `Channel`, `Metadata`, `PaidAt`, `Message` |
| `GatewayWebhook` | `Event`, `Reference`, `Raw json.RawMessage` |

### 3.2 Paystack Implementation

**Source:** `internal/providers/paystack/client.go`

| Operation | Endpoint | Key Detail |
|---|---|---|
| Initialize | `POST /transaction/initialize` | Forces `"channels": ["card"]` (line 47). Returns `authorization_url`. |
| Verify | `GET /transaction/verify/{reference}` | Server-to-server. Returns amount, status, domain, channel. |
| Webhook | `POST /webhooks/paystack` | HMAC-SHA512 signature via `X-Paystack-Signature` header. |

**Config:** `PAYSTACK_SECRET_KEY`, `PAYSTACK_BASE_URL` (default `https://api.paystack.co`)

**Request/response flow:**
```
Xego                           Paystack
  │── POST /transaction/initialize ──▶│
  │   {reference, email, amount,      │
  │    currency, channels:["card"],   │
  │    metadata, callback_url}        │
  │◀── {authorization_url, reference}─│
  │                                   │
  │  [Customer enters card on Paystack hosted page]
  │                                   │
  │◀── POST /webhooks/paystack ───────│  (charge.success)
  │    HMAC-SHA512 verified           │
  │                                   │
  │── GET /transaction/verify/{ref} ─▶│  (always re-verifies server-side)
  │◀── {status, amount, channel} ─────│
```

### 3.3 Flutterwave Implementation

**Source:** `internal/providers/flutterwave/client.go`

| Operation | Endpoint | Key Detail |
|---|---|---|
| Initialize | `POST /v3/payments` | Forces `"payment_options": "card"` (line 51). Returns checkout `link`. |
| Verify | `GET /v3/transactions/verify?tx_ref=...` | Server-to-server. Amount is float (converted to kobo). |
| Webhook | `POST /webhooks/flutterwave` | HMAC-SHA512 via `verif-hash` header. |

**Config:** `FLUTTERWAVE_SECRET_KEY`, `FLUTTERWAVE_PUBLIC_KEY`, `FLUTTERWAVE_BASE_URL` (default `https://api.flutterwave.com`), `FLUTTERWAVE_WEBHOOK_SECRET`

**Status normalization:** Flutterwave statuses are normalized before mapping to domain states:
```
"successful" / "success"  →  "success"  →  StatusSucceeded
"cancelled"               →  "failed"   →  StatusFailed
"pending"                 →  "pending"  →  StatusPending
```

**Channel normalization:** `"card-charge"` → `"card"`, `"banktransfer"` → `"bank_transfer"`, etc.

### 3.4 End-to-End Card Payment Flow

```
Step 1: Customer Initiates (WhatsApp / Checkout Link / Partner API)
         │
         ▼
Step 2: PaymentService.CreateDraftForProvider()
         │  - Generates provider_reference: "wpd_<uuid>"
         │  - Generates receipt_token, checkout_token
         │  - Reserves KYC tier allowance (C9 tiers)
         │  - Persists Payment (status: draft → awaiting_confirmation)
         │
         ▼
Step 3: Customer Completes the Chat Web Flow (/w/{token})
         │  - Horizontal stepper: Merchant → Item → Amount → Review → Pay
         │  - Fixed-price items fold quantity onto the item step and skip
         │    the amount step; single-option steps auto-skip entirely
         │  - Review doubles as the method page; a typical card charge is
         │    4 page loads (3 in a single-option catalog)
         │  - NO card data requested by Xego
         │
         ▼
Step 4: Customer Taps Pay → PaymentService.InitializeCheckout()
         │  - Validates status == awaiting_confirmation
         │  - Looks up gateway: gateways[payment.Provider]
         │  - Calls gateway.Initialize({reference, email, amount, callback_url, metadata})
         │  - Interswitch returns the hosted checkout URL → saved as CheckoutURL
         │  - Review routes the browser straight there (the /checkout/{token}
         │    hub remains as the resume/status entry for saved links)
         │
         ▼
Step 5: Customer Enters Card on the Hosted-Fields Gateway Page
         │  - Entirely outside Xego's domain
         │  - Xego never sees, processes, or stores card data
         │
         ▼
Step 6: Webhook Received
         │  POST /webhooks/paystack (charge.success)
         │  POST /webhooks/flutterwave (charge.completed)
         │
         │  Handler flow:
         │  1. Read body (max 1MB)
         │  2. Validate HMAC signature
         │  3. Deduplicate via ON CONFLICT (provider, event_key)
         │  4. If valid + terminal event → enqueue for worker
         │
         ▼
Step 7: Background Worker → VerifyAndApply()
         │  1. Load payment by provider_reference
         │  2. Call gateway.Verify(reference) — server-to-server
         │  3. validateVerification():
         │     - Reference must match
         │     - Amount must match
         │     - Currency must match
         │     - Paystack: domain=="test", channel=="card"
         │     - Metadata payment_id/merchant_id must match
         │  4. mapGatewayStatus() → domain status
         │  5. Transition payment to terminal status
         │  6. Enqueue WhatsApp receipt notification
         │
         ▼
Step 8: Ledger Posting + Fee Splits
         │  1. transitionPayment() posts:
         │     DR 1100_operating_bank / CR 2100_customer_float
         │  2. applyCollectionSplits() computes:
         │     fee = min(amount × BPS / 10000 + fixed, cap)
         │     DR 2100_customer_float / CR 2300_xego_payable      (platform fee)
         │     DR 2100_customer_float / CR 3100_merchant_payable   (merchant share)
         │  3. Generate QR scan token if applicable
         │
         ▼
Step 9: Customer Receives Receipt
         WhatsApp text/template with status + receipt URL
```

### 3.8 Receipt Delivery — Individual (Payer)

Every payment mints a `ReceiptToken` at draft creation (`internal/service/payments.go:69,88`) and a receipt page is always served at `/receipts/{token}`. The **individual/payer is always notified with the receipt link** on every terminal transition:

| Transition | Mechanism | Source |
|---|---|---|
| Succeeded / Failed / Expired | `resultOutbox()` builds a WhatsApp message containing `BaseURL + "/receipts/" + payment.ReceiptToken`, enqueued transactionally via `TransitionPaymentWithOutbox` | `internal/service/payments.go:515-545` |
| Conversation status check | "Latest Xego payment … Receipt/status: …/receipts/{token}" | `internal/service/conversation_helpers.go:117-119` |

**Outbox detail:** for `channel == "api"` the outbox row is skipped (the caller is told where to pay and the API response carries status — no chat session exists to message). For all chat channels (whatsapp/checkout/telegram), the receipt link is delivered as a WhatsApp text; a template message is used when the last inbound message is older than 23 hours.

**Conclusion:** a customer who pays a merchant **always receives a receipt** (hosted page + WhatsApp notification with the `/receipts/{token}` link).

---

## 4. Service 2: Bank Transfers

**Source:** `internal/service/fees.go:17-46`

```
fee = min(amount × BPS / 10000 + fixed, cap)
```

| Parameter | Config Key | Default |
|---|---|---|
| BPS | `XEGO_FEE_CARD_BPS` | 200 (2%) |
| Fixed | `XEGO_FEE_CARD_FIXED_KOBO` | 10,000 (₦100) |
| Cap | `XEGO_FEE_CARD_CAP_KOBO` | 350,000 (₦3,500) |

**Merchant receivable** = `amount - fee` (`internal/service/fees.go:60-66`)

### 3.6 PCI Compliance — Card Data Protection

| Layer | Mechanism | Source |
|---|---|---|
| **Chat Guard** | Scans every inbound message for PANs (Luhn-validated 13-19 digits), CVV (3-4 digits near keywords), PIN (4-6 digits near keywords), OTP (4-8 digits near keywords). Blocked + redacted. | `internal/chatguard/chatguard.go` |
| **Log Redaction** | Masks API keys, card PANs, phone numbers, tokens in error text before persistence. | `internal/redact/redact.go` |
| **No Storage** | Payment struct and `payments` table have zero card-data fields. | `internal/domain/payment.go` |
| **No Processing** | Card entry happens entirely on Paystack/Flutterwave hosted pages. | `internal/providers/paystack/client.go:47` |
| **Enforcement** | Menu help text, onboarding, AI system prompts all state "Xego never asks for card details in chat." | Various conversation files |

### 3.7 API Endpoints (Card)

| Method | Path | Purpose | Source |
|---|---|---|---|
| `POST` | `/api/v1/payments` | Partner API: initiate card payment | `internal/app/api_keys.go` |
| `GET` | `/api/v1/payments/{reference}` | Partner API: check payment status | `internal/app/api_keys.go` |
| `POST` | `/api/v1/payments/{reference}/verify` | Partner API: re-verify against gateway | `internal/app/api_keys.go` |
| `GET` | `/payments/return` | Gateway callback: verify + redirect to receipt | `internal/app/checkout.go` |
| `GET` | `/w/{token}` | Chat web-flow pages (payments, KYC, onboarding) | `internal/app/webflow.go` |
| `GET` | `/checkout/{token}` | Hosted checkout hub (resume/status for saved links) | `internal/app/checkout.go` |
| `POST` | `/checkout/{token}/pay` | Initialize gateway checkout (hub path) | `internal/app/checkout.go` |
| `GET` | `/checkout/interswitch/{reference}` | Hosted-fields secure card page | `internal/app/checkout.go` |
| `POST` | `/webhooks/paystack` | Paystack webhook receiver | `internal/app/webhooks.go:160` |
| `POST` | `/webhooks/flutterwave` | Flutterwave webhook receiver | `internal/app/webhooks.go:194` |

---

## 4. Service 2: Bank Transfers

### 4.1 Nature

Bank transfers in this codebase are a **simulated/demo rail**. There is no real bank API integration. The system presents Xego collection account details; the customer manually transfers money; the customer taps "I have transferred" to confirm — and the system trusts this confirmation.

### 4.2 Database Schema

**Source:** `internal/store/migrations/004_bank_transfer_simulation.sql`

```sql
-- Pre-seeded collection accounts (5 Nigerian banks)
CREATE TABLE bank_transfer_accounts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bank_name     TEXT NOT NULL,
    account_name  TEXT NOT NULL DEFAULT 'Xego Collections',
    account_number TEXT NOT NULL,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    search_keywords TEXT,   -- added in migration 007
    sort_order    INTEGER,  -- added in migration 007
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Links a payment to a chosen collection account
CREATE TABLE bank_transfer_simulations (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id           UUID NOT NULL REFERENCES payments(id),
    bank_account_id      UUID NOT NULL REFERENCES bank_transfer_accounts(id),
    simulated_reference  TEXT NOT NULL UNIQUE,
    status               TEXT NOT NULL DEFAULT 'awaiting_transfer',
    confirmed_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**Pre-seeded banks:** Access Bank, Guaranty Trust Bank, Zenith Bank, First Bank of Nigeria, United Bank for Africa — all under "Xego Collections".

### 4.3 Data Model

**Source:** `internal/store/store_payments.go:101-123`

```go
type BankTransferAccount struct {
    ID, BankName, AccountName, AccountNumber, Active,
    SearchKeywords, SortOrder, CreatedAt
}

type BankTransferInstruction struct {
    PaymentID, BankAccountID, BankName, AccountName, AccountNumber,
    SimulatedReference, Status, CreatedAt
}
```

### 4.4 End-to-End Flow

```
Step 1: Customer Selects "Bank Transfer" in WhatsApp
         │  Session state: select_payment_method → select_transfer_bank
         │  Source: internal/service/conversation_payment.go:461
         │
         ▼
Step 2: Customer Picks a Collection Bank
         │  Searchable bank picker (search_keywords column)
         │  Source: sendTransferBankPicker() → handleTransferBank()
         │  Source: internal/service/conversation_payment.go:536
         │
         ▼
Step 3: PaymentService.CreateDraftForProvider("bank_transfer")
         │  Same draft flow as card: generate reference, reserve KYC allowance
         │  Source: internal/service/payments.go:56
         │
         ▼
Step 4: PaymentService.InitializeBankTransferSimulation()
         │  - Validates provider == "bank_transfer"
         │  - Validates status == awaiting_confirmation
         │  - store.InitializeBankTransferSimulation():
         │    1. BEGIN transaction
         │    2. SELECT payment FOR UPDATE (row lock)
         │    3. INSERT into bank_transfer_simulations
         │       (simulated_reference = payment.provider_reference)
         │    4. Read bank details from bank_transfer_accounts
         │    5. Transition payment: awaiting_confirmation → pending
         │    6. Record event: bank_transfer.initialize
         │    7. COMMIT
         │  Source: internal/store/store_payments.go:384
         │
         ▼
Step 5: Customer Sees Instructions (WhatsApp)
         │  "Bank transfer details
         │   Merchant: <name>
         │   Amount: <amount>
         │   Bank: <bank_name>
         │   Account name: <account_name>
         │   Account number: <account_number>
         │   Reference: <simulated_reference>
         │
         │   What to do:
         │   1. Open your bank app.
         │   2. Transfer the exact amount above to this account.
         │   3. Put the reference exactly as shown in the narration field.
         │   4. After sending, tap I have transferred."
         │  Source: sendBankTransferInstructions() → conversation_payment.go:714
         │
         ▼
Step 6: Customer Taps "I Have Transferred"
         │  Session state: await_bank_transfer
         │  Input: "confirm_bank_transfer" button / "transferred" keyword
         │  Source: handleBankTransferConfirmation() → conversation_payment.go:610
         │
         ▼
Step 7: PaymentService.ConfirmBankTransferSimulation()
         │  - store.ConfirmBankTransferSimulation():
         │    1. BEGIN transaction
         │    2. SELECT payment FOR UPDATE
         │    3. UPDATE bank_transfer_simulations SET status='user_confirmed', confirmed_at=NOW()
         │    4. Transition payment: pending → succeeded (sets paid_at)
         │    5. Ledger: DR 1100_operating_bank / CR 2100_customer_float
         │    6. For plain collections: DR 2100_customer_float / CR 3100_merchant_payable
         │    7. Record event: bank_transfer.user_confirmation
         │    8. Enqueue WhatsApp notification
         │    9. COMMIT
         │  Source: internal/store/store_payments.go:468
         │
         ▼
Step 8: Post-Confirmation
         │  - ApplyInvoicePaymentSuccess (if linked)
         │  - ApplyThriftContributionPaymentSuccess (if linked)
         │  - ConfirmServicePurchase (if applicable)
         │  - ConfirmEventTicketPurchase (if applicable)
         │  - applyCollectionSplits (fee + merchant receivable)
         │  - createReceiptScanToken (QR for service/event)
         │  Source: internal/service/payments.go:254-271
         │
         ▼
Step 9: Customer Receives Confirmation
         "Thanks. Xego has received your transfer confirmation.
          You'll receive the final update shortly."
         Then: outbox worker sends WhatsApp text with receipt URL
```

### 4.5 Fee Model (Bank Transfer)

**Source:** `internal/service/fees.go:27-30`

Bank transfers use the DVA fee parameters:

```
fee = min(amount × BPS / 10000 + fixed, cap)
```

| Parameter | Config Key | Default |
|---|---|---|
| BPS | `XEGO_FEE_DVA_BPS` | 150 (1.5%) |
| Fixed | `XEGO_FEE_DVA_FIXED_KOBO` | 0 |
| Cap | `XEGO_FEE_DVA_CAP_KOBO` | 150,000 (₦1,500) |

### 4.6 Bank Transfer in Other Flows

Bank transfer is offered across five conversation flows, all sharing the same backend:
the payment is created as an ordinary draft and routed to Interswitch - the hosted
checkout for the card rail, the one-time DVA instruction page (`/transfer/:ref`) for bank
transfer. No flow parks the session on an "await confirmation" state: the session
returns to the menu and the payment completes only on the provider webhook plus the
server-side requery, so a customer's word can never mark a payment received.

| Flow | Payment-method entry state | Router |
|---|---|---|
| Standard merchant payment | `select_payment_method` | `conversation_payment.go` |
| Invoice payment | `invoice_pay_method` | `conversation_invoice.go` |
| Thrift contribution | `thrift_pay_method` | `conversation_thrift.go` |
| Data purchase | `select_data_payment_method` | `conversation_data.go` |
| Individual-to-individual pay | `pay_individual_method` | `conversation_individual_pay.go` |

The individual pay flow is unique: the sender pays Xego (amount + fee), Xego disburses to the recipient's bank account via the payout system.

### 4.7 Outbound Bank Transfers (Settlements & Payouts)

**Source:** `internal/store/store_settlements.go`

```
Step 1: Merchant registers payout destination
         Partner API: POST /api/v1/settlements/accounts
         {bank_code, account_number, account_name}
         → Stored in merchant_settlement_accounts

Step 2: CutSettlement() batches succeeded payments
         - Locks all succeeded, un-batched payments for merchant
         - Creates settlement_batch + settlement_lines
         - Ledger: DR 3100_merchant_payable / CR 3200_settlement_payable
         - Fee:    DR 3200_settlement_payable / CR 5200_settlement_fees

Step 3: CreatePayout()
         - amount = batch_total - fee
         - Target: merchant's default settlement account
         - Provider: "simulated"

Step 4: ClaimPayoutForDispatch() → worker
         - Marks payout as "processing"
         - Dispatches to provider

Step 5: CompletePayout()
         - Ledger: DR 3200_settlement_payable / CR 1100_operating_bank
         - Events: payout.succeeded, settlement.batch.processed

Step 6: Failure handling
         - Retry: up to 3 attempts
         - Reverse: reopens the batch
```

---

## 5. Service 3: Payment Gateway (Provider Router)

### 5.1 Provider Registry

**Source:** `internal/app/app.go:94-109`

At startup, the app builds a `map[string]ports.PaymentGateway`:

```
if PAYSTACK_SECRET_KEY is set    → register "paystack"
if FLUTTERWAVE_SECRET_KEY is set → register "flutterwave"
if none configured               → force-register "paystack" (backwards compat)
```

The map is passed to `PaymentService` and `ProviderRouter`.

### 5.2 Provider Router

**Source:** `internal/service/provider_router.go`

```go
type ProviderRouter struct {
    mu       sync.RWMutex
    stats    map[string]*providerStats  // per-gateway health + latency
    gateways map[string]ports.PaymentGateway
    ewmaAlpha float64                   // 0.3 (higher = weight recent more)
}

type providerStats struct {
    healthy           bool
    consecutiveErrors int
    latencyEWMA       float64  // milliseconds
    lastError         error
}
```

**Selection algorithm (`PickProvider()`, line 50-77):**

```
1. Filter to healthy providers (consecutiveErrors < 3)
2. Pick the one with lowest latencyEWMA
3. If all unhealthy → pick the one with fewest consecutive errors (best-effort)
```

**Health tracking (`ReportResult()`, line 80-103):**

```
latencyEWMA = 0.3 × newLatency + 0.7 × previousEWMA

On error:
  consecutiveErrors++
  if consecutiveErrors >= 3 → mark unhealthy

On success:
  consecutiveErrors = 0
  healthy = true
```

**Manual override:** `SetHealthy(name, bool)` for admin recovery.

**Important caveat:** `ReportResult()` is only called in tests. All user-facing flows currently hardcode `ProviderPaystack`. Flutterwave is only reachable via `ProviderAuto` in the router path.

### 5.3 Provider Constants

**Source:** `internal/service/payments.go:34-43`

```go
ProviderPaystack     = "paystack"
ProviderFlutterwave  = "flutterwave"
ProviderBankTransfer = "bank_transfer"
ProviderAuto         = "auto"        // uses ProviderRouter.PickProvider()
```

### 5.4 Gateway Wiring Flow

```
CreateDraftForProvider(provider="auto")
    │
    ▼
ProviderRouter.PickProvider()
    │  Returns healthiest gateway name
    │
    ▼
CreateDraftForProvider(provider="paystack")  // resolved name
    │  Validates: paystack | flutterwave | bank_transfer
    │
    ▼
Payment persisted with resolved provider
    │
    ▼
InitializeCheckout()
    │  gateway = gateways[payment.Provider]
    │  gateway.Initialize(...)
    │
    ▼
VerifyAndApply()
    │  gateway = gateways[payment.Provider]
    │  gateway.Verify(reference)
    │  validateVerification() — per-provider rules
    │  mapGatewayStatus() — normalize + map to domain
    │
    ▼
Payment terminal state reached
```

### 5.5 Webhook Processing Pipeline

**Source:** `internal/app/webhooks.go` + `internal/app/workers.go`

```
Gateway sends webhook
    │
    ▼
HTTP Handler (receivePaystackWebhook / receiveFlutterwaveWebhook)
    │  1. Read body (max 1MB)
    │  2. Validate HMAC signature (provider-specific)
    │  3. RecordWebhook() — ON CONFLICT deduplication
    │  4. If valid + terminal event → worker processes
    │  5. Return 200 immediately
    │
    ▼
Background Worker (processGatewayWebhooks)
    │  1. Claim unprocessed webhook per provider
    │  2. IsGatewaySuccessEvent? (paystack: charge.success, flutterwave: charge.completed)
    │  3. If yes → VerifyAndApply() (re-verifies server-side)
    │  4. If no → mark "ignored"
    │
    ▼
VerifyAndApply()
    │  (see Section 3.4 Step 7)
```

### 5.6 Webhook Authentication

| Provider | Header | Algorithm | Source |
|---|---|---|---|
| Paystack | `X-Paystack-Signature` | HMAC-SHA512(secret_key, body) | `paystack/client.go:112-137` |
| Flutterwave | `verif-hash` | HMAC-SHA512(webhook_secret, body) | `flutterwave/client.go:119-143` |

Both use constant-time comparison (`hmac.Equal`).

### 5.7 Receipt & Notification Delivery — Merchant

**Merchants receive confirmation through three separate mechanisms**, each with different coverage:

#### 5.7.1 Signed Merchant Webhook (server-to-server, opt-in)

**Source:** `internal/service/merchant_webhooks.go:41-51, 180-214`

```
Merchant registers:
  POST /api/v1/webhooks
  {url, secret}

On payment.succeeded / payment.failed:
  EnqueueMerchantWebhook() → idempotent enqueue
  MerchantWebhookDeliverer worker:
    1. Claim delivery
    2. POST signed (HMAC-SHA256) payload to merchant URL
    3. Retry: up to 5 attempts, exponential backoff
    4. Dead-letter after exhausting retries
```

The delivery envelope **includes the receipt URL**:

```json
{
  "reference":   "<merchant_reference>",
  "payment_id":  "<uuid>",
  "status":      "succeeded",
  "amount":      {"value": 250000, "currency": "NGN"},
  "paid_at":     "2026-01-01T10:00:00Z",
  "receipt_url": "https://pay.xego.ng/receipts/<receipt_token>"
}
```

- `receipt_url` and `paid_at` are populated **only on success**, and empty on failure (`merchant_webhooks.go:209-212`).
- This is the **only merchant channel that carries the hosted receipt URL.**

#### 5.7.2 WhatsApp Notification — Invoice-Linked Payments Only (opt-in)

**Source:** `internal/service/payments.go:427-447`, `internal/service/eventbus.go:66-91`, `workers.go:102`

The `payment.succeeded` event is emitted for **every** succeeded payment (`store_bus.go:70-84`) and consumed by the `xego.notifications` consumer, which calls `NotifyMerchantPayment`. However:

| Condition | Result |
|---|---|
| Payment **not linked to an invoice** (`InvoiceByPaymentID` returns error) | Returns immediately — **no merchant WhatsApp message** (`payments.go:431-434`) |
| Invoice-linked, but `NotifyPayment` pref disabled | **No message** (`payments.go:435-438`) |
| Invoice-linked + pref enabled + owner has WhatsApp | **Plain-text alert sent** ("Invoice payment received!") — amount/status/invoice reference, but **no `/receipts/{token}` URL** (`payments.go:443-446`) |

**Gap:** a plain merchant collection (hosted checkout link, WhatsApp card/bank-transfer payment, Partner API payment) that is **not** attached to an invoice generates **no merchant WhatsApp notification**. Only the opt-in server-to-server webhook (§5.7.1) covers those.

#### 5.7.3 Merchant Console / Partner API (pull)

- `GET /api/v1/balance` exposes the merchant's ledger position (collected but unsettled funds).
- The admin/merchant consoles list succeeded payments with their receipt tokens.
- Receipt scans and invoice progress are surfaced via the merchant dashboard.

### 5.8 Receipt Delivery Summary

| Party | Receipt link (`/receipts/{token}`) | Notification trigger | Gap |
|---|---|---|---|
| **Individual / payer** | ✅ Always (outbox on every terminal transition) | Every succeeded/failed/expired payment | API-initiated payments notify the caller in the response, not WhatsApp |
| **Merchant — webhook (opt-in)** | ✅ Yes | `payment.succeeded` for all payments | Requires merchant to register `/api/v1/webhooks` |
| **Merchant — WhatsApp (opt-in)** | ❌ No link, text alert only | **Invoice-linked payments only**, and only if `NotifyPayment` pref on | No notification for non-invoice plain collections |

### 5.9 Reconciliation

**Source:** `internal/store/store_reconcile.go`

Three-way reconciliation runs periodically:

```
Leg 1: Internal payments state    (payments WHERE status='succeeded')
Leg 2: Ledger money-in postings   (DR 1100_operating_bank WHERE source_type='payment')
Leg 3: Bank rail                  (bank_transfer_simulations WHERE status='user_confirmed')

Discrepancies tracked:
  - internal_without_ledger     — succeeded payment has no ledger posting
  - amount_mismatch             — payment amount ≠ ledger amount
  - ledger_without_internal     — ledger posting with no succeeded payment
  - bank_without_internal       — bank simulation confirmed but payment not succeeded
  - bank_amount_mismatch        — payment amount ≠ bank simulation amount
  - internal_without_bank       — succeeded bank_transfer payment has no confirmed simulation

Fourth leg: payout reconciliation
  - Completed payouts vs DR 3200 / CR 1100 ledger postings
```

---

## 6. Double-Entry Ledger

### 6.1 Chart of Accounts

**Source:** `internal/store/store_ledger.go:22-34`

| Code | Account | Role |
|---|---|---|
| `1100_operating_bank` | Operating Bank | Real bank holding collected funds |
| `1300_settlement_suspense` | Settlement Suspense | Transient during settlement processing |
| `2100_customer_float` | Customer Float | Transit account for customer payments |
| `2300_user_payable` | User Payable | Amount owed to individual users for payouts |
| `2300_xego_payable` | Xego Payable | Platform fee liability |
| `3100_merchant_payable` | Merchant Payable | Money owed to merchants (not yet settled) |
| `3200_settlement_payable` | Settlement Payable | Money in a settlement batch |
| `4100_sales_revenue` | Sales Revenue | Revenue recognition |
| `5100_provider_cost` | Provider Cost | Payment provider costs |
| `5200_settlement_fees` | Settlement Fees | Fees deducted during settlement |
| `6100_thrift_pool` | Thrift Pool | Thrift/savings group funds |

### 6.2 Posting Rules

**Source:** `internal/store/store_ledger.go:120-154`

Every posting is a balanced debit+credit pair:

```
postLedgerPair(journalRef, sourceType, sourceID,
    debitAccount, creditAccount, currency, description,
    postedBy, amountKobo, merchantID)
```

Validations:
- `amountKobo > 0`
- `debitAccount ≠ creditAccount`
- Both accounts in `validLedgerAccounts`
- Currency and description required
- Insert serialized on tail row (`SELECT ... FOR UPDATE`) to prevent chain forks

### 6.3 Hash Chain

**Source:** `internal/store/store_ledger.go:73-80`

```go
func ledgerHash(prevHash, journalRef, sourceType, sourceID, entryType,
    account, currency, description, postedBy string, amountKobo, reversalOf int64) string {
    fields := []string{prevHash, journalRef, sourceType, sourceID, entryType, account,
        currency, description, postedBy, fmt.Sprintf("%d", amountKobo),
        fmt.Sprintf("%d", reversalOf)}
    sum := sha256.Sum256([]byte(strings.Join(fields, "|")))
    return hex.EncodeToString(sum[:])
}
```

### 6.4 Immutability

- PostgreSQL trigger `reject_ledger_entry_mutation` rejects all `UPDATE` and `DELETE` on `ledger_entries`.
- Corrections are offsetting entries linked via `reversal_of`.
- `PostLedgerReversal()` reads originals, posts opposite-type entries, never mutates.

### 6.5 Balance Computation

No stored balance field. Always computed:

```sql
-- Global balance (must net to zero)
SELECT account,
    SUM(CASE WHEN entry_type='debit' THEN amount_kobo ELSE -amount_kobo END) AS net_kobo,
    SUM(CASE WHEN entry_type='debit' THEN amount_kobo ELSE 0 END) AS debit_kobo,
    SUM(CASE WHEN entry_type='credit' THEN amount_kobo ELSE 0 END) AS credit_kobo
FROM ledger_entries GROUP BY account;

-- Per-merchant balance (add WHERE merchant_id=$1)
-- Merchant available = -(net of 3100_merchant_payable + 3200_settlement_payable)
```

### 6.6 Ledger Entry Flow Summary

| Event | Debit | Credit |
|---|---|---|
| Payment received | `1100_operating_bank` | `2100_customer_float` |
| Platform fee split | `2100_customer_float` | `2300_xego_payable` |
| Merchant receivable split | `2100_customer_float` | `3100_merchant_payable` |
| Settlement batch created | `3100_merchant_payable` | `3200_settlement_payable` |
| Settlement fee | `3200_settlement_payable` | `5200_settlement_fees` |
| Payout completed | `3200_settlement_payable` | `1100_operating_bank` |
| Individual payout | `2300_user_payable` | `1100_operating_bank` |
| Refund (reversal) | Offsetting entries with flipped types, linked via `reversal_of` |

---

## 6A. Payment Splits & Charges

### 6A.1 Core Fee Functions

**Source:** `internal/service/fees.go`

```
XegoCollectionFee(cfg, channel, amountKobo):
    fee = min(amountKobo × BPS / 10000 + fixed, cap)
    if fee > amountKobo: fee = amountKobo          // never exceeds the amount

MerchantReceivable(amountKobo, feeKobo):
    receivable = amountKobo - feeKobo              // clamp at 0
```

Fee params by channel: card (`BPS 200 / fixed 10,000 / cap 350,000`), dva+bank_transfer (`BPS 150 / fixed 0 / cap 150,000`), transfer (`NIP/payout` model).

### 6A.2 How Splits Are Posted

**Standard merchant collection** (`createCollectionDraft` + `applyCollectionSplits`, `internal/service/payments.go`):

The merchant quotes a base amount; the customer is charged `base + collection fee`. `CreateCollectionDraft` stores the **charge** (`base + fee`) as `payment.AmountKobo` and carries `base_amount_kobo` / `collection_fee_kobo` in `payment.Metadata`. On success `collectionSplitAmounts` reads the base + fee from metadata so the legs reconcile with the charge the customer paid:

| Step | Ledger pair | Amount |
|---|---|---|
| Money-in (on success) | DR `1100_operating_bank` / CR `2100_customer_float` | charge (`base + fee`) |
| Platform fee split | DR `2100_customer_float` / CR `2300_xego_payable` | `fee` |
| Merchant receivable split | DR `2100_customer_float` / CR `3100_merchant_payable` | **`base` (full base — not reduced by fee)** |

`ApplyPaymentSplits` (`internal/store/store_split_payments.go:22-60`) is **idempotent**: if `payment_splits` rows already exist for the payment it returns immediately. Each leg is a balanced `postLedgerPair` from `2100_customer_float`; only `3100_merchant_payable` is merchant-tagged. Legacy/unknown payments without metadata fall back to the historical model (`fee` deducted from the paid amount).

**Skipped splits:** invoice, thrift, and data payments skip collection splits (`applyCollectionSplits` calls `IsInvoiceThriftOrData` first and returns; `store_split_payments.go:62-73`). Their ledger flows are handled by their own hooks.

### 6A.3 Are Charges Added to the Base Amount? — By Flow

#### Standard merchant collection (card, checkout link, bank transfer, WhatsApp pay, Partner API): **YES — collection fee added to the customer**

- `CreateCollectionDraft` (`internal/service/payments.go`) computes `fee = XegoCollectionFee(cfg, FeeChannelForProvider(provider), baseKobo)` and books the **charge = base + fee** as `payment.AmountKobo`. The fee channel is derived from the **rail/provider** (card rails → `"card"`, bank-transfer rail → `"dva"`) via `FeeChannelForProvider`, not the conversation channel.
- The charge is passed to the gateway: `InitializePayment.AmountKobo = payment.AmountKobo` (`payments.go`), and verified against the same value (`validateVerification`).
- Customer-facing copy shows the fee and total:
  - Card review: "Amount: {amount}, Collection fee: {fee}, Total to pay: {charge}" (`conversation_payment.go`, `sendCardReview`)
  - Bank transfer: "Amount: {charge}” (the charge, which is what they transfer; `sendBankTransferInstructions`)
- The merchant receives the **full base**; Xego keeps the fee as a surcharge. `collectionSplitAmounts` (`payments.go`) reconciles `charge = fee + base`.

**Example (card, base ₦10,000):** fee = min(200×10,000/10000 + 100, 3500) = ₦300. Charge to customer = ₦10,300. Merchant receivable = ₦10,000 (full base); Xego keeps ₦300.

#### Individual-to-individual pay: **YES — collection fee IS added on top of the base**

**Source:** `internal/service/conversation_individual_pay.go:95-99, 150-152`

```go
collectionFee := XegoCollectionFee(cfg, "transfer", amountKobo)
totalPay := amountKobo + collectionFee.FeeKobo      // ← fee added on top
recipientGets := amountKobo - XegoPayoutFee(...)    // NIP flat fee deducted
```

The UI tells the sender: "Total you pay: {amount + collection fee}" and "Transfer this amount to Xego's bank account." The collection fee is a **surcharge on the sender**, and the NIP flat fee is **deducted from the recipient's** payout (`recipientGets = amount − nipFee`).

**Reconciliation (fixed):** the money-in split is now posted against `totalPay` via `RecordIndividualPaySplits` (`store_split_payments.go`), with three legs — `collection fee` (credit `2300_xego_payable`), `NIP fee` (credit `2300_xego_payable`), and `individual_payout` (credit `2300_user_payable`, amount `recipientGets`). These reconcile exactly with the quoted total:

```
totalPay = amountKobo + collectionFee
         = collectionFee + nipFee + recipientGets
```

#### Settlement payout fees / data / invoice / thrift: **not affected**

- Settlement batch fee: `feeKobo = total × feeBps / 10000`, payout `= total − fee` (`store_settlements.go:284, 429`).
- Data orders, invoice contributions, and thrift contributions use `CreateDraftForProvider` and are **not** surcharged (they skip collection splits; their ledger flows are handled by their own hooks).

### 6A.4 Summary Table: Who Bears the Charge

| Flow | Customer pays | Fee source account | Relationship to base amount |
|---|---|---|---|
| Merchant card / checkout / bank transfer / WhatsApp / Partner API | `base + collection fee` | fee added on top (surcharge); full base to merchant | **Fee ADDED** to customer |
| Individual-to-individual pay | `base + collection fee` | collection fee + NIP flat fee (both Xego) | **Fee ADDED** to sender |
| Data / invoice / thrift | exact quoted amount | own hooks; no collection split | **No surcharge** |
| Settlement payout | — | batch `total × feeBps` | Deducted from merchant payout |

---

## 7. Key Files Reference

| File | Purpose |
|---|---|
| `internal/domain/payment.go` | Payment aggregate, status enum, transitions, NGN formatting |
| `internal/ports/ports.go` | PaymentGateway interface + all port interfaces |
| `internal/service/payments.go` | PaymentService: draft, init, verify, splits, reconcile |
| `internal/service/fees.go` | Fee computation: `XegoCollectionFee`, `XegoPayoutFee`, `MerchantReceivable` |
| `internal/service/provider_router.go` | EWMA-based health/latency provider selection |
| `internal/providers/paystack/client.go` | Paystack API: initialize, verify, webhook validation |
| `internal/providers/flutterwave/client.go` | Flutterwave API: initialize, verify, webhook validation, health |
| `internal/app/webhooks.go` | Webhook ingress: Paystack, Flutterwave, WhatsApp, Telegram, VTPass |
| `internal/app/checkout.go` | Hosted checkout pages, gateway initialization |
| `internal/app/api_keys.go` | Partner API: payment CRUD, verify, balance |
| `internal/chatguard/chatguard.go` | PCI: blocks card PANs, CVVs, PINs, OTPs in chat |
| `internal/redact/redact.go` | PCI: masks card data in logs |
| `internal/store/store_payments.go` | Payment persistence, bank transfer simulation, webhook recording |
| `internal/store/store_ledger.go` | Double-entry ledger, hash chain, balance queries |
| `internal/store/store_settlements.go` | Settlement batches, payout creation/dispatch/completion |
| `internal/store/store_reconcile.go` | Three-way reconciliation engine |
| `internal/store/migrations/004_bank_transfer_simulation.sql` | Bank transfer schema |
| `internal/store/migrations/030_ledger.sql` | Ledger schema + immutability trigger |
| `internal/config/config.go` | Fee params, provider keys, feature flags |
