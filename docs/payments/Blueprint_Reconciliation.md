# Blueprint Reconciliation: Xego vs Payment Gateway Architecture Blueprint

This report cross-checks the production payment-gateway blueprint
(`reports/Payment_Gateway_Architecture_Blueprint.docx`, added to this repo as
the missing source artifact referenced by `Payment_System_Implementation_Plan.md`)
against the current Xego codebase and the C-item engineering backlog
(`docs/payments/Remediation_Backlog.md`). Status tags:

- **Present** — already implemented and exercised by tests
- **Partial** — a narrower/different version exists that partly satisfies the requirement
- **Gap** — not present, or present only in placeholder/simulated form

Gap ownership is given by C-item ID where one exists; items with **no C-item**
need a new backlog entry.

---

## A. Container view (blueprint §3.2) vs current monolith

| Blueprint container | Status | Current state / evidence | C-item / notes |
|---|---|---|---|
| API Gateway | Partial | Caddy (deploy/Caddyfile) terminates TLS; no WAF/DDoS, no authN gateway | Phase 1 infra; C19 hardening |
| Merchant API | Present | `internal/app` routes; merchant portal UI + merchant services API | — |
| Checkout / Hosted Fields | Partial | Hosted card checkout delegates PAN capture to provider (Paystack); bank-transfer path in-app | PCI scope minimization (blueprint §7) met for cards |
| Tokenization Service | Gap | No in-house token vault; provider-side tokens only (`receipt_token` is a local opaque token, not a card token) | no C-item; PCI zone Phase 1 |
| Payment Orchestrator | Partial | `internal/service/payments.go` + `transitionPayment` (store.go) = in-monolith orchestrator; state machine of 8 statuses | extracted Phase 2 |
| Connector Services | Present | `internal/providers/{paystack,vtpass}` | — |
| Risk Engine | Gap | No transaction scoring; simulator KYC only | C9-C15 (Phase 2) |
| Ledger Service | Partial | `payment_events` event log + append-only `archive_ledger` (migration 025); no double-entry journal | C16 (Phase 3) |
| Settlement Service | Gap | No settlement batches/lines | C17/Phase 3 |
| Payout Service | Gap | Bank-transfer *payment path* exists; no batch payouts | Phase 3 |
| Reconciliation Service | Partial | `cmd/demo reconcile` → `PaymentService.Reconcile` (service/payments.go:352) verifies unresolved payments against the provider every minute; no bank-statement / ledger three-way | C17 (Phase 3) |
| Disputes Service | Gap | No disputes/chargebacks | no C-item |
| Webhook Delivery Service | Present | `webhook_deliveries` durable queue + `RecordWebhook`/`CompleteWebhook`, redacted storage | — |
| Admin / Operations Console | Present | `/admin/*` (users, audit, legal-holds, archive, admins, TOTP, metrics) with RBAC | — |
| Event Bus | Gap | No Kafka; `message_outbox` is a transactional outbox, not a distributed bus | Phase 2/3 (plan §6) |
| OLTP Database | Present | `payments`, `webhook_deliveries`, `inbound_messages`, sessions, etc. | — |
| Ledger Database | Partial | `archive_ledger` (append-only, hash-chained) — serves retention archive, not the full journal | C16 |
| Data Warehouse | Gap | None | Phase 3 |
| Object Storage | Gap | None | Phase 3 |
| Monitoring / Audit / SIEM | Partial | `audit_logs` tamper-evident log + structured JSON logging with correlation IDs; no SIEM ingest | C5/C30, C4/C26 |

---

## B. Data-model blueprint (blueprint §4) vs migrations (001-025)

### B.1 Merchants and configuration

| Blueprint table | Status | Current state |
|---|---|---|
| `merchants` | Present | 001_init; seeded fixtures + merchant_registrations (011) |
| `merchant_settlement_accounts` | Gap | no payout/settlement account data |
| `pricing_plans` | Partial | `merchant_services` + `service_purchases` (019) model product pricing, not plan/billing |
| `webhook_endpoints` | Gap | webhook *deliveries* are recorded, but there is no registered webhook-endpoint config table |

### B.2 Payments

| Blueprint table | Status | Current state |
|---|---|---|
| `payment_intents` | Partial | `payments` (001_init) = single payment/attempt object; no split intent/attempt |
| `payment_attempts` | Gap | no multi-attempt model; one `payments` row per attempt |
| `payment_method_tokens` | Gap | no card token vault (see Tokenization Service) |
| `authorizations` | Gap | no auth hold/authorize-then-capture |
| `captures` | Gap | single `succeeded` terminal state, no capture step |
| `refunds` | Partial | `payment_events` records refund-ish transitions only if provider reports; no refund table |
| `voids` | Gap | no void table |

### B.3 Ledger

| Blueprint table | Status | Current state |
|---|---|---|
| `ledger_accounts` | Gap | no chart of accounts |
| `journal_batches` | Gap | no journal batches |
| `journal_entries` | Partial | `archive_ledger` records archived payloads, not double-entry journal entries; `payment_events` is an event log |
| `balance_snapshots` | Gap | no balance ledger |

### B.4 Settlement, payouts, reconciliation, disputes

| Blueprint table | Status | Current state |
|---|---|---|
| `settlement_batches`, `settlement_lines` | Gap | none |
| `payouts` | Gap | none |
| `processor_reports`, `bank_statements` | Gap | none |
| `reconciliation_runs`, `reconciliation_items` | Gap | reconciliation is code-only (`reconcile` command); no persistent run/line records |
| `disputes`, `dispute_evidence` | Gap | none |

---

## C. Event model (blueprint §5) vs current events

### C.1 Envelope

| Blueprint envelope field | Current state |
|---|---|
| `event_id`, `event_type`, `event_version`, `occurred_at`, `producer` | `payment_events(payment_id, from_status, to_status, source, detail)` covers a subset (type inferred from transition; no version/producer) |
| `tenant_id`, `correlation_id`, `causation_id`, `trace_id` | **Gap** — logging middleware attaches `request_id`; no event correlation/causation/trace fields |
| `data` | `detail` jsonb payload |

### C.2 Topic families

All blueprint topics (`payments.v1`, `ledger.v1`, `settlements.v1`, `payouts.v1`,
`disputes.v1`, `reconciliation.v1`, `webhooks.v1`) are **Gap** — no Kafka. The
`message_outbox` table (outbox `OutboxSpec`, store.go:525) is the transactional
outbox the blueprint calls for; it is converted to Kafka topics at Phase 2/3
per `Payment_System_Implementation_Plan.md` §6.

---

## D. Operational requirements (blueprint §7) vs current implementation

| Requirement | Status | Current state | C-item |
|---|---|---|---|
| Strict idempotency on write endpoints + connector calls | Partial | `receipt_token` UNIQUE + inbound/webhook dedupe (`RecordWebhook` fresh flag); not applied to every write endpoint | — |
| Ledger as append-only journal with reversals | Partial | `archive_ledger` + `audit_logs` are append-only & mutation-rejected (migration 025/023); **no double-entry reversals** | C16 |
| Outbox / event-driven integration | Present | `message_outbox` transactional outbox; `payment_events` appended in same tx as payment transition | — |
| PCI scope minimization (hosted fields, isolated tokenization) | Partial | PAN stays with provider via hosted checkout; no in-house tokenization zone | Phase 1 |
| Three-way reconciliation (internal vs ledger vs external) | Partial | `Reconcile` verifies provider state for unresolved payments; no bank/ledger leg | C17 |
| Webhook signing + replay protection | Partial | Inbound signatures validated (Paystack HMAC-SHA512, Meta `sha256=`, VTPass header secret); outbound *delivery* to merchants has **no signing/replay** | C28 (inbound) |
| Tamper-evident operator audit logs | Present | `audit_logs` hash chain + append-only trigger + `/admin/audit` verification | C4/C26 |

---

## E. Gap-to-C-item matrix

| Gap | Covered by | Phase |
|---|---|---|
| Tokenization Service / `payment_method_tokens` / token DB | **no C-item** | 1 (plan §6) |
| Risk Engine, KYC ladder, screening, monitoring, STR/CTR | C9-C15 | 2 |
| Kafka event bus + envelope (tenant/correlation/causation/trace) | **no C-item** (outbox conversion implied by plan §6) | 2/3 |
| Append-only ledger + journal tables + reversals | C16 | 3 |
| Three-way reconciliation (ledger + bank) | C17 | 3 |
| Settlement + payout tables/services | **no C-item** | 3 |
| Disputes service | **no C-item** | 3 |
| Webhook *delivery* signing + replay to merchants | **no C-item** (C28 covers inbound VTPass) | 3 |
| Webhook endpoint config (`webhook_endpoints`) | **no C-item** | 3 |
| Data warehouse / object storage | **no C-item** | 3 |
| Balance snapshots / chart of accounts | **no C-item** (part of C16) | 3 |

---

## F. Summary

- **Present (no action):** connector providers, webhook delivery queue, outbox,
  tamper-evident audit, RBAC/MFA, hosted card checkout, admin console.
- **Partial (extend in place):** orchestrator (extract Phase 2), ledger
  (`archive_ledger` → full journal C16), reconciliation (C17), webhook delivery
  signing, idempotency breadth.
- **Gaps needing new C-items:** tokenization service, Kafka event envelope,
  settlement/payout, disputes, `webhook_endpoints` config, outbound webhook
  signing, data warehouse/object storage.

The blueprint is the target; `Payment_System_Implementation_Plan.md` §6 defines
the extraction order and DB ownership for each gap. Items without a C-item
should be added to `docs/payments/Remediation_Backlog.md` before Phase 2 planning.
