# Xego — Business & Individual Account Integration Plan

**Status:** Proposal
**Scope:** A cleaner, tighter integration surface for two audiences:
- **Businesses** — a Partner API so a merchant store can initiate payments, a
  hosted checkout, and signed outbound webhooks.
- **Individuals** — payment links, `wa.me` deep links, and a hosted checkout so
  paying does not depend on the chat FSM.

**Core thesis:** payment initiation is currently trapped inside the chat
state machine. The single fix that unlocks every integration is to funnel all
entry points (API, hosted checkout, deep link, chat) through the one existing
provider-neutral primitive — `PaymentService.CreateDraftForProvider`
(`internal/service/payments.go:50`) — and keep verification authoritative and
server-side (`VerifyAndApply`).

All file references are to the current tree (commit `bd82c34`).

---

## 1. Why this is needed (current state)

| Capability | Today | Gap |
|---|---|---|
| Merchant store initiates payment | Only via chat FSM handlers | No HTTP API, no idempotency |
| Public checkout page | None — chat sends a Paystack URL, callback lands on `/payments/return` (`internal/app/app.go:913`) | No branded hosted checkout |
| Notify merchant's server on payment | None | No outbound webhooks |
| Merchant auth for API | Web login: password + TOTP cookie session (`/merchant/login`) | No API-key auth, no HMAC signing |
| Invoice creation | Chat FSM only | No `POST /api/v1/invoices` |
| Pay-from-store for individuals | `PAY XG-INV-…` typed in chat (`internal/service/conversation.go:448`) | No deep link / payment link |
| Ledger / balance for merchants | Double-entry ledger exists (`internal/store/store_ledger.go`) | Not exposed via API |

The merchant concept is already first-class in the DB (`Merchant` struct at
`internal/store/store.go:203`, including payment terms). What does not exist is
any programmatic surface for it.

---

## 2. Core design: one payment-initiation primitive, many channels

`PaymentService.CreateDraftForProvider` (`internal/service/payments.go:50`)
already builds a payment with a `channel` field and moves it through the state
machine (draft → awaiting_confirmation → initialized → verified by
`VerifyAndApply`). Today the only channels are `whatsapp` and `telegram`.

### Change

Extend the channel set so every entry point shares the same lifecycle:

```go
const (
    ChannelAPI     = "api"       // Partner API
    ChannelHosted  = "hosted"    // hosted checkout page
    ChannelDeepLink = "wa_link"  // wa.me deep link
    ChannelWhatsApp = "whatsapp"
    ChannelTelegram = "telegram"
)
```

Every initiator calls:

```go
payment, err := payments.CreateDraftForProvider(ctx, user, merchant, amountKobo, provider, channel, recipient)
```

Then either:
- `InitializeCheckout(ctx, payment)` → hosted card checkout, or
- `InitializeBankTransferSimulation(ctx, payment, account)` → transfer
  instructions.

Notifications (WhatsApp template/text, email receipt) are emitted by the same
`resultOutbox` logic (`internal/service/payments.go:325`) regardless of entry
point. The chat FSM becomes one of several adapters, not the only one. **No new
channel-specific payment logic is ever written again.**

---

## 3. Business integration: the Partner API

### 3.1 API authentication (separate from web login)

New table `merchant_api_keys`:

```sql
CREATE TABLE merchant_api_keys (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id   UUID NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,                       -- "Production storefront"
  key_hash      BYTEA NOT NULL,                      -- SHA-256 of the public key
  secret_hash   BYTEA NOT NULL,                      -- SHA-256 of the signing secret
  prefix        TEXT NOT NULL,                       -- "xeg_sk_live_" / "xeg_sk_test_"
  enabled       BOOLEAN NOT NULL DEFAULT true,
  last_used_at  TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX ON merchant_api_keys (key_hash);
CREATE INDEX ON merchant_api_keys (merchant_id);
```

- Issued once from `/merchant/settings`; only hashes are stored (same posture
  as admin session tokens at `internal/store/store.go:5284`).
- Requests authenticate with HMAC over the request, matching the pattern the
  project already uses in `ValidateSignature`
  (`internal/providers/whatsapp/client.go:51`):

  ```
  X-Xego-Key:      xeg_sk_live_<id>
  X-Xego-Timestamp: <unix seconds>
  X-Xego-Signature: HMAC-SHA256(secret, method + path + timestamp + body)
  ```

- Reject requests with timestamps older than 5 minutes (replay protection).
- Per-key rate limiting via the existing `internal/ratelimit` package.
- Every call is written to the existing audit log (`audit_logs`).
- `test` vs `live` keys map to `APP_ENV` behavior; test keys reject real-money
  side effects.

### 3.2 `POST /api/v1/payments` — initiate payment from store

The headline endpoint:

```
POST /api/v1/payments
X-Xego-Key: xeg_sk_live_...
X-Xego-Timestamp: 1723470000
X-Xego-Signature: hmac(...)
Content-Type: application/json

{
  "reference": "ORD-8371",
  "idempotency_key": "c3f8a91b-...",
  "amount": { "value": 250000, "currency": "NGN" },
  "customer": { "phone": "+2347061975340", "email": "customer@example.com" },
  "metadata": { "cart_id": "CART-9", "items": "3" },
  "redirect_url": "https://shop.example.com/thanks"
}
```

Response:

```
200
{
  "payment_id": "7f1c...",
  "reference": "ORD-8371",
  "status": "awaiting_confirmation",
  "checkout_url": "https://pay.xego.ng/checkout/7f1c...",
  "created_at": "2026-08-12T10:00:00Z"
}
```

Implementation notes:

1. **Idempotency:** `UNIQUE (merchant_id, reference)` and a stored
   `idempotency_key`; a replay returns the existing payment instead of creating
   a duplicate. Mirrors the webhook digest dedup at
   `internal/store/store.go:5070`.
2. **Customer resolution:** create-or-find the user by canonical E.164 phone
   (reuse `normalizePhone`, `internal/service/conversation.go:4311`); email is
   stored on the user for checkout metadata.
3. **Reuse:** call `CreateDraftForProvider` with `channel = "api"`, then
   `InitializeCheckout` when a hosted card checkout is desired. Do **not** write
   a parallel payment-creation path.
4. **Errors** are typed and stable:
   `invalid_request`, `unauthorized`, `merchant_inactive`,
   `amount_out_of_range`, `reference_taken`, `customer_invalid`.

### 3.3 Hosted checkout page — `GET /checkout/{token}`

Today there is no public checkout UI; the chat flow sends a Paystack URL and the
return path verifies then redirects to the receipt (`internal/app/app.go:913`).
Add a branded page:

- Bearer capability token (reuse `randomToken`), unguessable like receipt
  tokens.
- Renders order summary (merchant, amount, items from metadata), calls
  `InitializeCheckout` server-side, and posts the customer to Paystack.
- The existing `/payments/return` + `VerifyAndApply` callback stays the
  authoritative completion path.
- Stores only need to redirect to `checkout_url` from their site.

### 3.4 Status and verification endpoints

- `GET /api/v1/payments/{reference}` — current status.
- `POST /api/v1/payments/{reference}/verify` — forces a server-side
  `VerifyAndApply` and returns the authoritative result.
- Status is always the backend verification result, never a client-reported
  success — same discipline as `paymentReturn`.

### 3.5 Outbound webhooks to the business server

The project consumes webhooks (Paystack, WhatsApp, Telegram, VTPass, SMS) but
never delivers them. Add merchant-facing delivery:

- New table `merchant_webhook_deliveries` with the same claim/retry/backoff
  shape as `message_outbox` (`internal/store/store.go:5270`).
- On `payment.succeeded` / `payment.failed`, enqueue a delivery to the
  merchant's `callback_url`:

  ```
  POST https://shop.example.com/xego/webhook
  X-Xego-Signature: HMAC-SHA256(merchant_secret, body)
  X-Xego-Event: payment.succeeded
  {
    "reference": "ORD-8371",
    "payment_id": "7f1c...",
    "status": "succeeded",
    "amount": { "value": 250000, "currency": "NGN" },
    "paid_at": "2026-08-12T10:05:00Z",
    "receipt_url": "https://pay.xego.ng/receipts/<token>"
  }
  ```

- The merchant verifies the signature on their side (contract identical to
  Paystack's `ValidateWebhook`).
- Retries with exponential backoff; dead-letter to an operator alert after the
  cap (mirror `RetryOutbox`).

### 3.6 Invoices and balance endpoints

- `POST /api/v1/invoices` — create an invoice from line items (reuse the
  `InvoiceView` / `InvoiceItem` structs, `internal/store/store.go:239-272`).
- `GET /api/v1/invoices/{reference}` — status incl. `amount_paid`.
- `GET /api/v1/balance` — merchant balance once the double-entry ledger
  (`internal/store/store_ledger.go`) is exposed to merchants.
- The `/merchant/*` web console becomes a thin wrapper over this API instead of
  a parallel implementation.

### 3.7 Example: "Pay with Xego" button for a store

```
<script>
fetch("https://api.xego.ng/api/v1/payments", {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    "X-Xego-Key": "xeg_sk_live_...",
    "X-Xego-Timestamp": String(Date.now()/1000 | 0),
    "X-Xego-Signature": hmacSign(secret, ...)
  },
  body: JSON.stringify({
    reference: "ORD-" + order.id,
    idempotency_key: order.checkout_uuid,
    amount: { value: order.total_kobo, currency: "NGN" },
    customer: { phone: order.customer_phone },
    redirect_url: location.origin + "/thanks"
  })
}).then(r => r.json()).then(res => {
  location.href = res.checkout_url;   // hosted checkout
});
</script>
```

---

## 4. Individual integration

### 4.1 `wa.me` deep links — cheapest store→WhatsApp bridge

A store's "Pay with Xego" can be a plain link with no backend:

```
https://wa.me/<business_phone>?text=PAY%20XG-INV-ABC123
```

Tapping it opens WhatsApp with the `PAY XG-INV-…` command that `handleMenu`
already parses (`internal/service/conversation.go:448`); the existing FSM
completes the payment in chat. Zero new backend — businesses add a link,
individuals pay inside WhatsApp with one tap.

### 4.2 General "request money" payment link

Beyond invoices, add `POST /api/v1/checkouts` (no merchant/customer bound at
creation) returning `/checkout/{token}`. An individual shares that link in any
channel (WhatsApp, SMS, email, social); the hosted checkout resolves the payer,
verifies server-side, and pushes the receipt to their WhatsApp/email via the
existing outbox.

### 4.3 Chat becomes optional

Individuals can pay via hosted checkout without messaging the bot — good for
store contexts, gift payments, and users not on WhatsApp. The chat FSM stays as
one of several paths, not the only one.

---

## 5. Reuse — do not rebuild

| Concern | Existing building block |
|---|---|
| Payment lifecycle + authoritative verification | `PaymentService` + `VerifyAndApply` (`internal/service/payments.go`) |
| HMAC request/event signing | `ValidateSignature` pattern (`internal/providers/whatsapp/client.go:51`) |
| Webhook dedup + durable retry queue | `RecordWebhook` / `message_outbox` (`internal/store/store.go:5070,5270`) |
| Per-key rate limiting | `internal/ratelimit` |
| API action audit trail | `audit_logs` + append-only trigger |
| Secrets at rest | `internal/crypto` seal, `internal/redact` sanitize |
| Payment terms per merchant | `Merchant` struct fields (`store.go:203`) |
| Ledger / reconciliation | `internal/store/store_ledger.go`, `store_reconcile.go` |

---

## 6. Sequencing

| Phase | Scope | Effort |
|---|---|---|
| **P0** | Hosted checkout page (`/checkout/{token}`) + `wa.me` deep link for invoices | 2–3 days |
| **P1** | `merchant_api_keys`, `POST /api/v1/payments` + idempotency, `GET /api/v1/payments/{ref}`, `POST .../verify` | 3–4 days |
| **P2** | Outbound signed webhooks (`payment.succeeded` / `payment.failed`) + `POST /api/v1/invoices` | 3 days |
| **P3** | `GET /api/v1/balance` once the ledger is merchant-facing; general `POST /api/v1/checkouts` for individuals | 2 days |

**P0 is the highest leverage for the least work** — it makes stores able to
collect payments (via link or deep link) with zero API surface, and it exercises
the unified-channel model before the full API lands.

## 7. Preconditions and open questions

1. **Channel unification** in `CreateDraftForProvider` must land first (small
   change: extend the channel constants; the method already takes `channel`).
2. **E.164 canonicalization** for customer phone resolution across API + chat
   (today `normalizePhone` is loose).
3. **Test vs live keys:** confirm the demo's Paystack test-only posture carries
   through the API (non-test transactions are already rejected at
   `internal/service/payments.go:290`).
4. **Webhook retry cap and signature rotation** policy for merchant callbacks.
5. **Do merchants get their own WhatsApp business number** per account? That is
   the multi-tenant WhatsApp proposition and is independent of this API plan,
   but both are needed for a complete business offering.
