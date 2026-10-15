# Architecture Review Action Plan

Source: Expert architecture review of Xego WhatsApp Payment Gateway
Date: 2026-08-17

---

## 🔴 MUST FIX BEFORE PRODUCTION

| # | Area | Current State | Required Change |
|---|------|---------------|-----------------|
| R1 | Payment State Machine | Inferred from `status` text column, no explicit transitions | Formal persisted states: CREATED → INITIALIZED → PENDING → SUCCEEDED/FAILED/EXPIRED/REQUIRES_ACTION. Separate settlement state (SETTLEMENT_PENDING → SETTLED) and refund state (REFUND_PENDING → REFUNDED/PARTIALLY_REFUNDED) |
| R2 | Ledger as Source of Truth | Ledger exists but other tables also used as balance source | Rule: ledger is authoritative for all financial balances. `payments.status`, `merchant.balance` etc. are operational views, not truth |
| R3 | Ledger Service Boundary | Services call `postLedgerPair` directly | Introduce `LedgerService` — only controlled financial domain can create accounting entries. Validates rules, creates journals, enforces balancing, commits atomically |
| R4 | Idempotency Strategy | Dedup exists for webhooks but not systematic | Idempotency at every boundary: Merchant→Xego (idempotency key), Xego→Provider (provider-specific refs), webhook processing (dedup), event consumers (idempotent handlers), settlement payout retries, refund retries |
| R5 | Append-Only Financial Records | Ledger is append-only (C16 trigger) but refunds/disputes allow UPDATE | Enforce immutability across all financial records. Corrections via reversal journals, never UPDATE |
| R6 | Webhook Durability | Webhook endpoint processes synchronously | Webhook endpoint: verify → validate → persist raw → dedup → ACK → async processing. Separate webhook gateway from processing |
| R7 | Outbox Reliability | In-memory bus acceptable for demo, but production needs durable | Clarify: in-memory = dev/test only. Production = Kafka. Outbox publisher must be idempotent. Add DLQ for failed events |
| R8 | Settlement State Machine | Settlement is batch-level status only | Explicit settlement lifecycle: CALCULATION → INSTRUCTION → EXECUTION → CONFIRMATION → RECONCILIATION → NOTIFICATION. Fee as config, not architectural constant |
| R9 | Reconciliation Exception Workflow | Three-way recon exists but no investigation workflow | Add: exception detection → assignment → investigation → resolution → approval → audit trail. Track who investigated, when, what resolution |
| R10 | Admin Maker-Checker | Single-step refund/dispute/adjustment | Four-eyes control for: refunds, manual ledger adjustments, merchant suspension, large payouts, settlement corrections, KYC overrides. Financial records immutable with corrective entries |
| R11 | Database HA/Backup | Single PostgreSQL instance | Plan for: replication, automated backups, PITR, failover, connection pooling, read replicas (NOT for ledger writes), DR, backup restore testing |
| R12 | Paystack Isolation | Paystack is the payment provider | Formalize: PaystackAdapter is execution-only. Xego owns: payment records, internal refs, merchant records, lifecycle, ledger, reconciliation, audit. Adapter replacement doesn't touch domain |

## 🟠 SHOULD FIX BEFORE SERIOUS SCALE

| # | Area | Change |
|---|------|--------|
| O1 | Durable Event Bus | Kafka in production with proper topic/partition strategy |
| O2 | DLQ + Retry Policies | Dead letter queues for failed events. Exponential backoff with jitter |
| O3 | Distributed Tracing | Correlation ID through entire request lifecycle. Request ID, Trace ID |
| O4 | Redis/Session Architecture | Redis for sessions, rate limits, short-lived cache. Never source of truth for money |
| O5 | Provider Circuit Breakers | Timeout, circuit breaker, fallback for Paystack + future providers |
| O6 | Service-to-Service Auth | Internal mTLS or token-based auth between services |
| O7 | Secrets/KMS | Move from env vars to secrets management. Envelope encryption with KMS/HSM |
| O8 | API + Event Versioning | Versioned event contracts. API versioning strategy |
| O9 | Rate Limiting Layers | Edge → API → Merchant → User/Session → Provider |
| O10 | Compliance Separation | Hard gates (sync) vs monitoring (async) vs reporting (post-txn). Don't chain all compliance synchronously |
| O11 | Edge Gateway Separation | Split inbound gateway: Edge/API Gateway → channel adapters. Add TLS, WAF, observability |
| O12 | Payment Orchestrator | Explicit orchestrator coordinating lifecycle: Risk → Provider → Ledger → Settlement |

## 🟢 ALREADY GOOD (maintain/enhance)

- Ports & Adapters pattern
- Modular monolith approach
- Transactional outbox pattern
- Double-entry ledger (SHA-256 chained)
- Three-way reconciliation
- External provider adapters
- Conversation/Payment/Settlement separation
- Compliance as dedicated concern
- Simulated payout/refund adapters
- ChatGuard credential detection
- Multi-channel support (WhatsApp, Telegram, API)

---

## Implementation Priority

### Phase 1: Financial Safety (R1-R6, R10, R12)
The payment state machine, ledger service boundary, idempotency, append-only enforcement, webhook durability, maker-checker, and Paystack isolation are all interconnected and should be addressed together as they define the financial safety guarantees.

### Phase 2: Operational Readiness (R7-R9, R11)
Outbox hardening, settlement state machine, reconciliation exceptions, and database HA.

### Phase 3: Scale Preparation (O1-O12)
Durable event bus, tracing, circuit breakers, secrets management, rate limiting layers, edge gateway.
