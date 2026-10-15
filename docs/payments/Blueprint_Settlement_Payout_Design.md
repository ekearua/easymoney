# Settlement & Payout Design Draft

Mapping the Payment Gateway Architecture Blueprint's **Settlement** and **Payout** planes onto the existing Xego implementation. This is a design draft, not yet implemented.

## 1. Context and goals

Xego already keeps an append-only double-entry ledger (C16) where collections are posted as:

- `dr 1100_operating_bank` / `cr 3100_merchant_payable` (merchant accrual, P3)

`GET /api/v1/balance` exposes `available_balance` as the negated `3100_merchant_payable` net. What is missing is the second leg of the money life cycle:

1. **Settlement** — cutting the accrued merchant liability into verifiable batches.
2. **Payout** — moving funds from the operating bank to the merchant's verified bank account.

Design goals:

- Keep the ledger the single source of truth; all money movement is double-entry journal postings with reversals, never destructive edits.
- Idempotent, outbox-driven, retryable, and dead-lettered like the existing webhook notifier (P2).
- Three-way reconciliation (C17) extended to cover settlements and payouts.
- Merchant payout destinations are verified and admin-approved (aligns with C2 RBAC and C4 audit).
- No card data / no changes to the payment capture path.

## 2. Ledger model

New accounts:

| Code | Name | Type | Behavior |
|------|------|------|----------|
| `3200_settlement_payable` | Settlement payable | Liability | Credited when a settlement batch is cut, debited when the payout is completed |
| `1300_settlement_suspense` | Settlement suspense | Asset | Used when a payout is sent but not yet confirmed |

Existing accounts stay: `1100_operating_bank`, `2100_customer_float`, `3100_merchant_payable`, `4100_sales_revenue`.

Updated `available_balance` definition:

```
available_balance = -( net(3100_merchant_payable) + net(3200_settlement_payable) ), clamped >= 0
```

- Before settlement: accrual sits in `3100` → full amount available.
- After a batch is cut: liability moves to `3200` → still available but now scheduled.
- After payout: `3200` nets to zero and the operating bank balance falls.

## 3. Schema blueprint (migration `041_settlements.sql`)

```sql
-- Verified merchant payout destinations (admin-approved)
CREATE TABLE merchant_settlement_accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     uuid NOT NULL REFERENCES merchants(id),
    bank_code       text NOT NULL,
    account_number  text NOT NULL,
    account_name    text NOT NULL,
    is_default      boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'active',          -- active | disabled
    approved_by     uuid REFERENCES admin_users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, bank_code, account_number)
);

-- Cut at a point-in-time; money is frozen out of 3100
CREATE TABLE settlement_batches (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_no        text NOT NULL UNIQUE,                    -- idempotency key, e.g. SET-2026-0001
    merchant_id     uuid NOT NULL REFERENCES merchants(id),
    status          text NOT NULL DEFAULT 'open',            -- open | scheduled | processed | failed
    total_kobo      bigint NOT NULL,
    line_count      integer NOT NULL DEFAULT 0,
    cutoff_at       timestamptz NOT NULL,
    ledger_journal  uuid,                                    -- posting batch reference
    created_at      timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz
);

-- One line per collected payment inside the batch
CREATE TABLE settlement_lines (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id        uuid NOT NULL REFERENCES settlement_batches(id),
    payment_id      uuid NOT NULL REFERENCES payments(id),
    amount_kobo     bigint NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, payment_id)
);

-- The outbound funds movement
CREATE TABLE payouts (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id            uuid NOT NULL REFERENCES settlement_batches(id),
    merchant_id         uuid NOT NULL REFERENCES merchants(id),
    destination_id      uuid NOT NULL REFERENCES merchant_settlement_accounts(id),
    amount_kobo         bigint NOT NULL,
    status              text NOT NULL DEFAULT 'queued',      -- queued | processing | completed | failed | reversed
    external_ref        text,                                -- provider id, unique when set
    provider            text NOT NULL DEFAULT 'simulated',   -- rails adapter port
    attempts            integer NOT NULL DEFAULT 0,
    last_error          text,
    ledger_journal      uuid,                                -- payout posting
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz
);
CREATE UNIQUE INDEX payout_external_ref_uniq ON payouts (external_ref) WHERE external_ref IS NOT NULL;

-- Outbox events for settlements/payouts (extends business_event_outbox)
-- settlements.v1 / payouts.v1 topic families
```

## 4. Payout lifecycle

```
settlement_batches: open -> scheduled -> processed -> failed*
payouts:            queued -> processing -> completed
                                 \-> failed -> (retry | reversed)
```

1. **Cut** — operator (or schedule) runs `CutSettlement(merchantID, cutoff)`: freezes eligible `3100` accruals into a batch. Idempotent on `batch_no` (auto-assigned with a sequence, or caller-supplied).
2. **Post** — single ledger journal: `dr 3100_merchant_payable / cr 3200_settlement_payable` for the batch total (offsetting reversal if the batch is voided).
3. **Send** — payout dispatches via a `PayoutProvider` port (mirrors `SanctionsScreener`/`IdentityVerifier` port pattern). `simulated` ships deterministic success/failure by bank code (e.g. `001` succeeds, `011` fails). On success write `external_ref` and move to `completed`; posting `dr 3200_settlement_payable / cr 1100_operating_bank`.
4. **Retry / dead-letter** — on failure, exponential backoff (reuse notifier backoff policy), then `failed` for operator review; reversal option restores `3100`.

## 5. Idempotency, events, and reconciliation

- **Idempotency**: `batch_no` unique; `payout.external_ref` unique; `CutSettlement` and payout dispatch are safe to replay (identical to the existing `reference` idempotency on Partner API payments).
- **Outbox events**: `settlement.batch.created`, `settlement.batch.processed`, `payout.succeeded`, `payout.failed`. Consumers: merchant notifier (P2) sends `payout.succeeded`/`payout.failed` webhooks; reconciler ingests payout outcomes.
- **Reconciliation (C17)**: `reconciliation_items` gains `type = settlement|payout`; the three-way check extends to "internal ledger position vs settlement batch vs bank payout statement".
- **Audit (C4)**: cutting a batch, approving a destination, and every payout transition are audit-logged with actor + IP.

## 6. API surface (Partner API, `/api/v1`)

- `POST /settlements` — cut a batch for the authenticated merchant (idempotent on `reference`). Body: `{reference, cutoff_at?}`.
- `GET /settlements/{reference}` — batch status, lines, totals.
- `POST /settlements/{reference}/payout` — dispatch the payout to the default (or specified) destination.
- `GET /payouts/{reference}` — payout status.
- `GET /balance` — unchanged shape; `available_balance` now reflects `3100 + 3200` (see §2).

Admin console: `/admin/settlements` — approve destinations, schedule/cut batches, retry/fail/reverse payouts.

## 7. Open questions

1. Should merchants self-serve settlement initiation, or is it operator-scheduled only? (Draft assumes both; operator-scheduled via a worker.)
2. Payout rails: which real provider for the `PayoutProvider` port (NIP / Paystack split / remita / Monnify)? Only the `simulated` adapter ships in this draft.
3. Minimum batch thresholds (e.g. only settle balances above NGN 1,000) and settlement frequency defaults.
4. Whether `4100_sales_revenue` recognition should move to settlement time or stay at collection time (currently collection time).
5. Fee treatment: settlement/payout fees booked to a new `5100_fees` expense account or netted from `3200`.

## 8. Not in scope of this draft

- Disputes and refunds (separate design; refund posting would `dr 3100_merchant_payable`).
- Multi-currency.
- On-demand partial payouts (batch = full cut only).
