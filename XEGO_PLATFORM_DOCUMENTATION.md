# Xego Platform — Comprehensive Feature & Technical Documentation

> **Status:** Current-state documentation, grounded in a completed repository analysis.
> **Source of truth:** The repository at `C:\Users\User\Mobile\Whatsapp Payment` (Go module `whatsapp-payment-demo`).
> **Scope statement:** This document describes what the repository contains and supports today. Where the analysis classifies a capability as partially implemented, simulated, scaffolded, or configured-only, that classification is preserved. Where the analysis does not establish something, that is stated explicitly. This document describes the platform **as it exists in the repository**, not a roadmap.

---

## 1. Executive Overview

Xego is a Nigeria-focused merchant checkout platform in which **WhatsApp and Telegram chat are the primary customer interfaces**, supplemented by a hosted card checkout page, bank-transfer payment instructions, request-money links, invoices, QR-based receipt scanning, and mobile-data purchases.

The platform is delivered as a **single Go binary** (`cmd/demo`) that runs a chi HTTP server, a large PostgreSQL-backed store layer, a rule-based conversational engine, and a set of background workers. It integrates with external providers through provider-neutral interfaces (`internal/ports`) with a mix of real HTTP clients and deterministic local simulators.

**Primary user experience.** A customer messages the merchant's WhatsApp number or Telegram bot, is guided through a conversational flow (menu-driven, keyword-matched), and can make card or bank-transfer payments, receive and pay invoices, join thrift savings groups, and buy mobile data. Merchants and operators use web consoles, and merchants can integrate server-to-server through a signed Partner API.

**Major product capabilities:**

- Conversational checkout (card and bank transfer)
- Merchant registration and invoice generation
- Request-money links ("checkouts")
- QR receipt scanning for merchant services/readers
- Thrift (group savings) with simulated payouts
- Mobile data purchase (MTN, Airtel, Glo, 9mobile)
- Settlements, payouts, refunds, and disputes
- Partner API for server-to-server payments
- Signed merchant webhook notifications
- Interswitch Web Checkout (hosted card checkout) as the sole payment gateway with verification-driven success
- Per-payment fee model (card, DVA, bank transfer) with configurable caps
- Payment split application (platform fee + merchant receivable) at collection time
- Individual pay (customer-to-customer payouts via bank transfer, conversational flow)

**Major technical capabilities:**

- Rule-based conversational engine (no AI/LLM)
- Double-entry, append-only, hash-chained ledger
- Transactional outbox + event bus (in-memory or Kafka)
- Idempotent payment resolution and webhook processing
- KYC tier ladder, sanctions screening, risk scoring, transaction monitoring
- Hash-chained audit log, SIEM, DSR, retention, and legal holds
- Three-way reconciliation
- RBAC admin console, TOTP two-factor authentication, encryption at rest
- Automatic provider routing with EWMA latency tracking and health-based failover
- Configurable per-provider fee model with breakpoint-aware caps
- Payment split ledger posting (platform fee + merchant receivable + individual payouts)
- Conversational individual pay flow (8-step WhatsApp guided payout)

**Supported channels:** WhatsApp Cloud API, Telegram Bot API, SMS (data-order commands only, MVP), hosted web pages (checkout, invoice, receipt, link, scan), and the Partner API.

**Financial functionality:** card checkout through Interswitch Web Checkout (hosted checkout, verification-driven success), a bank-transfer path (routed through the Interswitch gateway, verification-driven success like cards), per-payment fee computation (card: 2%+₦100 capped ₦3,500; DVA: 1.5% capped ₦1,500; bank transfer: 1.8% capped ₦2,500), payment split application at collection time (platform fee + merchant receivable), individual payouts (bank transfer, NIP flat ₦100 fee deducted from payout), settlements, payouts, refunds, disputes, and a double-entry ledger. The **payout, refund, data-fulfilment, identity, and screening rails are deterministic simulators** behind real interfaces; the Interswitch integration is a real HTTP client restricted to test keys.

**AI/conversational functionality:** There is **no artificial intelligence**. The conversational engine is purely rule-based (intent/keyword matching and menu state). Any expectation of AI/NLP capabilities is not supported by the repository analysis.

**External integrations:** Interswitch Web Checkout (real, test-key), WhatsApp Cloud API (real), Telegram Bot API (real), SMTP email (optional), VTPass (sandbox), plus simulated payout, refund, data-fulfilment, identity (NIN/BVN), and sanctions/PEP screening providers. Redis (optional) and Kafka (optional) back rate limiting and the event bus respectively.

**Overall platform architecture.** Customers and merchants interact through channel webhooks and web consoles; a chi router (`internal/app`) receives requests, enqueues inbound messages, and exposes admin/merchant/Partner APIs; domain services (`internal/service`) orchestrate business logic; the store layer (`internal/store`) persists state, applies financial invariants, and writes business events to a transactional outbox; background workers drain outbox, deliver webhooks, run settlements, reconciliation, retention, rescreening, and transaction monitoring. PostgreSQL is the system of record.

**Production readiness:** The repository analysis explicitly establishes that the build is **not** production-ready for live money. README.md:443-444 states the build is not a licensed payment processor and lists required additions before live-money use. Simulated rails, `EMAIL_DEMO_CODE_IN_CHAT`, and test-key-only Interswitch policy corroborate this.

---

## 2. Platform Overview

The platform is best understood as logical layers. Layers that the analysis does not support (e.g., a true AI layer) are omitted or described accurately as their actual nature.

### 2.1 User / Channel Layer

- **Purpose:** Accept inbound customer interactions and render customer- and merchant-facing web pages.
- **Components:**
  - WhatsApp webhook (`/webhooks/whatsapp`) — signature-verified, parses text and interactive selections (`internal/providers/whatsapp/client.go`).
  - Telegram webhook (`/webhooks/telegram`) — secret-token-verified.
  - SMS webhook (`/webhooks/sms`) — shared-secret-verified, data-order commands only.
  - Public web pages: hosted checkout (`/checkout/{token}`), request-money link (`/link/{token}`), receipt (`/receipts/{token}`), invoice (`/invoices/{reference}`), thrift group (`/thrift/{name}`), scan landing (`/scan/{token}`).
  - Merchant console and admin console (htmx-based templates in `web/templates`).
- **Relationship to other layers:** Normalizes inbound messages into `inbound_messages`, enqueues them, and renders results from domain services. It does not contain business logic.

### 2.2 Conversational Layer

- **Purpose:** Interpret chat input and drive guided workflows.
- **Components:** `internal/service/conversation.go` (~380 lines, dispatcher core) plus domain-specific flow files (`conversation_onboarding.go`, `conversation_menu.go`, `conversation_merchant.go`, `conversation_merchant_register.go`, `conversation_individual.go`, `conversation_thrift.go`, `conversation_invoice.go`, `conversation_payment.go`, `conversation_data.go`, `conversation_helpers.go`). A deterministic rule engine with intents, conversation state, and channel-specific outbound messaging via `messengerFor` (conversation.go:409).
- **Relationship to other layers:** Consumes inbound messages, invokes domain services (payments, invoices, thrift, data, KYC), and produces outbound responses. **This layer contains no AI models, prompts, or NLP.**

### 2.3 Application / Domain Layer

- **Purpose:** HTTP surface, orchestration, and business rules.
- **Components:**
  - `internal/app/app.go` (~750 lines, core routing) plus handler files (`admin.go`, `auth.go`, `checkout.go`, `cli.go`, `merchant.go`, `webhooks.go`, `workers.go`) — chi router, admin/merchant consoles, Partner API, worker loop, event consumer wiring.
  - `internal/service/*` — `PaymentService`, `SettlementService`, `RefundService`, `DisputeService`, `DataService`, `MerchantWebhookConsumer`, `EventPublisher`, notification/compliance consumers.
  - `internal/domain/*` — status machines and invariants (`CanTransition` at `domain/payment.go:40-46`), event payloads.
- **Relationship to other layers:** Routes call services; services use ports and the store; store enforces state transitions and emits events.

### 2.4 Financial Layer

- **Purpose:** Double-entry accounting, per-entity wallets, settlements, payouts, refunds.
- **Components:**
  - `internal/store/store_ledger.go` — hash-chained ledger, chart of accounts, balanced debit/credit pairs.
  - `internal/store/store_wallets.go` — per-entity wallet accounts (individual pending→active on L0→L1, business active on KYB verification; credit/withdraw primitives; migration 058).
  - `internal/store/store_settlements.go` — settlement batches, payouts (cut credits the business wallet; payout discharges it).
  - `internal/store/store_refunds.go` — refunds and disputes (refunds credit the customer wallet).
  - Simulated payout/refund providers under `internal/providers/payout` and `internal/providers/refund`.
- **Relationship to other layers:** Payments post ledger entries; settlements move money between ledger accounts (including the merchant's business wallet); reconciliation verifies payment state against the ledger and bank rail.

### 2.5 Compliance / Risk Layer

- **Purpose:** Identity, monitoring, and regulatory operations.
- **Components:** `internal/kyc` (tier ladder, screening, risk scoring, transaction monitor), `internal/reports` (STR/CTR/PEP), DSR, legal holds, retention, SIEM, chat guard, RBAC, audit log.
- **Relationship to other layers:** Consumers listen to `payment.succeeded` events to run transaction monitoring; admin console surfaces review queues.

### 2.6 Integration Layer

- **Purpose:** Provider-neutral access to external systems.
- **Components:** `internal/ports` (interfaces: `PaymentGateway`, `PayoutProvider`, `RefundProvider`, `DataProvider`, `IdentityVerifier`, `SanctionsScreener`, `Messenger`, `EventBus`) and `internal/providers` (interswitch, whatsapp, telegram, vtpass, data, identity, screening, email). A gateway registry maps provider names to `PaymentGateway` implementations; a `ProviderRouter` tracks per-provider health and latency and selects the optimal provider automatically.
- **Relationship to other layers:** Domain services depend only on port interfaces; concrete providers are wired at application start.

### 2.7 Data Layer

- **Purpose:** Persistence and system of record.
- **Components:** `internal/store` (Store struct + sidecar files), 45 embedded SQL migrations, PostgreSQL 15+.
- **Relationship to other layers:** All state, queues, outbox, ledger, audit, and archive records live here.

### 2.8 Infrastructure Layer

- **Purpose:** Deployment and operations.
- **Components:** Docker Compose (PostgreSQL 17 with SSL and WAL archiving, one-shot backup service, migrate job, app, Caddy reverse proxy with automatic TLS), native VPS rebuild script, `.env.example` configuration surface, `Makefile` targets.
- **Relationship to other layers:** Runs the single binary; Redis (optional) and Kafka (optional) back rate limiting and the event bus.

### 2.9 Administration / Operations Layer

- **Purpose:** Operator-facing control.
- **Components:** Admin console routes (metrics, users, merchants, payments, thrift, KYC, settlements, refunds, disputes, ledger, reconciliation, SIEM, analytics, reports, DSR, legal holds, archive, chat guard, webhooks, admins, audit) with RBAC middleware; CLI commands (`reconcile`, `reconcile3`, `settle`, `refund`, `wallet-balance`, `wallet-withdraw`, `retain`, `rescreen`, `recompute-risk`, `monitor`, `reports`, `sync-vtpass-data-plans`, `health`).
- **Relationship to other layers:** Administrators invoke the same store/services as other actors, with role gates.

---

## 3. Complete Feature Catalogue

Status vocabulary used throughout (per the repository analysis): **Implemented**, **Partially Implemented**, **Scaffolded**, **Mocked/Simulated**, **Configured Only**, **Documented Only**, **Unclear**.

### 3.1 Conversational & Channel Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| WhatsApp Cloud API channel | Signature-verified inbound messages and outbound text/interactive/checkout/template/image messages | Customers, merchants | Implemented | `internal/providers/whatsapp/client.go`; `app/webhooks.go` |
| Telegram Bot API channel | Secret-token-verified webhook and outbound messages | Customers | Implemented | `app/webhooks.go` |
| SMS channel (data commands) | `DATA`, `PLANS`, `STATUS` commands; reply returned in webhook response | Customers | Partially Implemented (MVP; no outbound SMS sender) | `app/webhooks.go`; README:268 |
| Rule-based chat bot | Keyword/intent/menu-state engine for payments, invoices, thrift, data, KYC | Customers | Implemented | `internal/service/conversation.go` |
| Conversational AI / NLP | LLM, intent/entity ML, prompts | — | **Not implemented; no AI code exists** | — |
| Catalog search in chat | Merchant name/category search, recent merchants, data plan pages/search | Customers | Implemented | migrations 007, 009; README:402,306 |

### 3.2 Payments & Financial Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Hosted card checkout | Interswitch Web Checkout hosted checkout (form redirect), verification-driven success | Customers | Implemented | `internal/providers/interswitch/client.go`; `app/checkout.go` |
| Bank-transfer path | Generated transfer instructions; success only after customer confirms | Customers | Implemented (rail simulated) | `bank_transfer_simulations`; `store_reconcile.go:112-119` |
| Payment lifecycle | draft → … → succeeded/failed/refunded with `CanTransition` enforcement | System | Implemented | `domain/payment.go:32-46` |
| Partner API payments | Server-to-server initiate/status/verify | Merchants | Implemented | `app/api_keys.go`; routes in `app.go` |
| Invoices | Line-item invoices, split/partial payments, `wa.me` pay link | Merchants, customers | Implemented | migration 012; routes in `app.go` |
| Request-money links (checkouts) | General links resolved by a payer; idempotent | Merchants, customers | Implemented | migrations 035, 039; `app/checkout.go` |
| Payment fees | Settlement fee booked at batch cut | System, merchants | Implemented | migration 043; `SETTLEMENT_FEE_BPS` |
| Refunds (full) | Full-payment refunds with ledger reversal | Merchants (API), admins | Implemented (rail simulated) | `store_refunds.go:66-181` |
| Refunds (partial) | — | — | Not implemented (full-only per analysis) | `store_refunds.go:81` |
| Disputes | open/won/lost/expired; admin resolution | Admins, merchants | Implemented | `store_refunds.go:334-382` |
| Settlement batches | Cut open batches, scheduled/processed/failed | Merchants (API), admins | Implemented | `store_settlements.go`; migration 041 |
| Payouts | queued/processing/completed/failed/reversed; simulated rail | Merchants (API), admins | Implemented (rail simulated) | README:368; `app/workers.go` |
| Payout reversal | Reverse failed/processing payout, reopen batch | Merchants (API), admins | Implemented | `app/settlements.go`; README:363 |
| Settlement accounts | Register/manage payout destinations, defaults | Merchants (API), admins | Implemented | routes in `app.go` |
| Automatic provider routing | EWMA latency + consecutive-error health tracking; `PickProvider()` auto-selects optimal gateway | System | Implemented | `internal/service/provider_router.go` |
| Per-payment fee model | Card 2%+₦100/cap ₦3,500; DVA 1.5%/cap ₦1,500; bank transfer 1.8%/cap ₦2,500; NIP payout flat ₦100 | System | Implemented | `internal/service/fees.go` |
| Payment split application | Platform fee + merchant receivable + individual payout posted at collection time | System | Implemented | `internal/store/store_split_payments.go`; migration 052 |
| Individual pay | Conversational customer-to-customer payout via bank transfer (8-step WhatsApp flow, KYC L2 required) | Customers | Implemented | `internal/service/conversation_individual_pay.go` |
| Wallet funding (top-up) | "Fund wallet" conversational flow (main menu keyword, or *fund wallet*/*top up*) mints a payment against the inactive `xego-wallet-topup` system merchant (card or bank transfer on the secure checkout); a `wallet_topup` post-success hook credits the payer's wallet from the customer float, replay-safe on the payment's journal ref | Customers | Implemented | `internal/service/conversation_wallet.go`; migration 059 |

### 3.3 Ledger & Accounting Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Double-entry append-only ledger | Debit/credit entries, hash-chained, mutation-rejected | System, admins | Implemented | `store_ledger.go:55-124`; migrations 030, 040 |
| Chart of accounts | Operating bank, customer float, settlement suspense, merchant payable, settlement payable, sales revenue, provider cost, settlement fees, thrift pool, user wallet, business wallet | System | Implemented | `store_ledger.go:22-32` |
| Per-entity wallets (W1) | Individual wallet opened pending at L0 creation, activated at L1; business wallet active on KYB verification; credit on refunds/individual-pay/wallet top-up, debit on payouts/withdrawals; "pay from wallet" collection method and "fund wallet" top-up (migration 059 system merchant) debit/credit the wallet atomically with the payment transition; balance derived from the ledger | Customers, merchants, admins | Implemented | `store_wallets.go`; migration 058 |
| Ledger reversals | Offsetting entries for a journal reference | Admins | Implemented | `store_refunds.go:185-237`; `app/ledger.go:55-80` |
| Ledger chain verification | Verify hash-chain integrity from admin console | Admins | Implemented | `app/ledger.go:29-34` |

### 3.4 Compliance, Risk & Security Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| KYC tier ladder L0–L4 | Adjacent rung advancement gated on evidence | Customers, admins | Implemented | `internal/kyc`; migration 026 |
| KYC/KYB allowance ladder | DB-driven per-tier payment/payout ceilings (single/daily/monthly, in & out) | Customers, merchants, admins | Implemented | `internal/kyc` + `internal/store/store_allowances.go`; migration 053 |
| Sanctions/PEP screening | Deterministic simulated screener; live vendor plug-in | Customers | Implemented (provider simulated) | README:188 |
| NIN/BVN identity verification | Deterministic simulated verifier | Customers | Implemented (provider simulated) | README:190 |
| ML/FT risk scoring | 0–100 score, low/medium/high band | System | Implemented | `internal/kyc.ScoreRisk`; migration 027 |
| Transaction monitoring | Velocity, structuring, round amounts, high-risk categories | System, admins | Implemented | `kyc/monitor.go:82-197`; migration 028 |
| Regulator reports | STR, CTR, PEP CSV generation | Admins/compliance | Implemented | `app/reports.go:38-90` |
| DSR (access/erasure) | Consent records, full data export, erasure with archive | Admins/compliance, customers | Implemented | `store_dsr.go`; migration 029 |
| Legal holds | Freeze subjects from retention | Admins | Implemented | `store_dsr.go:154-159`; README:162-172 |
| Retention purge | Archive-then-delete in bounded batches | System | Implemented | README:162-172; migration 025 |
| Hash-chained audit log | Every privileged action; append-only trigger | Admins | Implemented | migration 023; README:142-144 |
| SIEM event log | Unified view of domain events + privileged actions, CSV/JSON export | Admins/compliance | Implemented | `app/siem.go` |
| Chat guard | Blocks card/PIN/CVV/OTP in chat; records redacted copies | Compliance | Implemented | `app/chatguard.go`; migration 032 |
| RBAC admin roles | admin/compliance/support/readonly on every admin route | Admins | Implemented | migration 022; `app/auth.go` |
| TOTP two-factor auth | Admin and merchant login | Admins, merchants | Implemented | migration 021; `app/auth.go` |
| Encryption at rest | Chat payloads + CSRF tokens sealed AES-256-GCM | System | Implemented | migration 024 |
| Rate limiting | Per-IP fixed windows; per-API-key | System | Implemented | `app.go` (middleware); `.env.example:111-118` |

### 3.5 Messaging & Notification Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Merchant webhooks | Signed HMAC-SHA256 deliveries on terminal events, retried | Merchants | Implemented | `service/merchant_webhooks.go:218-291` |
| Merchant notification | Chat notification of payment result | Merchants | Implemented | `service/eventbus.go:66-92` |
| Email confirmation | 6-digit OTP for merchant registration | Merchants | Implemented (SMTP optional; chat demo mode) | migration 010; `.env.example:20-27` |
| Outbound SMS | — | — | Not implemented (explicitly future work) | README:268 |

### 3.6 Merchant & Partner Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Merchant registration | Email-confirmed requests; admin approval | Merchants, admins | Implemented | migrations 010, 011; README:90-107 |
| Partner API keys | HMAC-signed keys, shown once, SHA-256 stored | Merchants | Implemented | README:318-338 |
| Merchant services catalogue | Services with payments and phone whitelists | Merchants, admins | Implemented | migration 019; `app/merchant.go` |
| QR receipt scanning | Merchant reader scans customer receipt QR | Merchants, admins | Implemented | migration 014; `app/checkout.go` |
| Merchant settings | Webhook URL/secret, profile, payment terms | Merchants, admins | Implemented | `app/merchant.go` |
| Settlement account approval | Admin approve/disable | Admins | Implemented | `app/admin.go` |

### 3.7 Administration Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Admin dashboard/metrics | Operational metrics | Admins | Implemented | `app/admin.go` |
| User & merchant management | List, mask, reset passwords, payment terms | Admins | Implemented | `app/admin.go` |
| KYC review queue | Approve/reject manual review cases, alerts | Admins/compliance | Implemented | `app/admin.go` |
| Reconciliation console | Run history + discrepancies, manual trigger | Admins/compliance | Implemented | `app/reconcile.go`; `store_reconcile.go` |
| Analytics dashboard | Revenue, merchant summaries, volume, payouts, CSV export | Admins/compliance | Implemented | `app/analytics.go` |
| Thrift management | Groups, contributions, simulated payout completion | Admins | Implemented | `app/admin.go` |
| Accepted numbers | Demo invoice allow-list management | Admins | Implemented | `app/admin.go` |
| Webhook log | VTPass callback records | Admins | Implemented | `app/admin.go` |

### 3.8 Thrift & Data Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Thrift groups | Fixed contribution, weekly/monthly, 2–12 members, payout rotation | Customers | Implemented | migrations 013, 015; `app/admin.go` |
| Thrift pool ledger account | 6100_thrift_pool | System | Implemented | `store_ledger.go:22-32` |
| Mobile data purchase | Order lifecycle, fulfilment (simulated/VTPass) | Customers | Implemented (fulfilment simulated) | `service/data.go`; migration 008 |
| Data plan catalog | Networks/plans, VTPass `provider_sku` sync | System | Implemented | `sync-vtpass-data-plans`; README:270-314 |

### 3.9 Platform Infrastructure Features

| Feature | Description | Users/Actors | Implementation Status | Key Components |
| ------- | ----------- | ------------ | --------------------- | -------------- |
| Transactional outbox | Business events written in-tx, drained by publisher | System | Implemented | `business_event_outbox`; migrations 033, 045 |
| Event bus | Kafka-compatible in-memory or Kafka | System | Implemented (memory default; Kafka Configured Only) | `.env.example:120-127` |
| Idempotency keys | Unique column + dedup on outbox | System | Implemented | migration 045 |
| Redis rate limiting | Shared counters across replicas | System | Configured Only (empty REDIS_URL → in-memory) | `.env.example:111-113` |
| WAL archiving + backups | `pg_basebackup` + rclone to S3-compatible bucket | Ops | Implemented (deployment) | README:196-209; `compose.yaml` |
| Caddy reverse proxy | Automatic TLS | Ops | Implemented (deployment) | `compose.yaml:79-95` |

---

## 4. User & Account Management

### 4.1 Registration & Onboarding

- **WhatsApp/Telegram customers:** A customer messages the bot, enters a name and email, and confirms the WhatsApp number or Telegram account. Standard onboarding collects an email for checkout and receipts but does **not** require an OTP. Evidence: README:90-92, 398-400.
- **Merchant registration:** A user choosing **Register merchant** receives a 6-digit email confirmation code (OTP), enters business name/category/description, and the request lands in `/admin/merchants` as `awaiting_approval`. An admin approves the request; approval creates the payable merchant record, links the requesting user as merchant owner, and unlocks merchant-only invoice features. Evidence: README:94, 401; migration 011.
- **Implementation status:** Implemented. Email delivery requires SMTP configuration; a chat-delivered demo code exists for rehearsal and is refused when `APP_ENV=production`. Evidence: `.env.example:20-27`; README:107.

### 4.2 Authentication

- **Customers:** Authenticated implicitly by channel identity (WhatsApp number / Telegram account) confirmed at onboarding. Evidence: README:399-400.
- **Admins and merchants:** Two-step sign-in when TOTP is enabled — password first, then a 6-digit authenticator code. The first successful login after enabling enrolls the account and shows a QR code. The shared secret is stored AES-256-GCM encrypted. Evidence: README:109-118; migration 021.
- **Merchant set-password flow:** A merchant can set a password via `/merchant/set-password`. Evidence: `app/merchant.go`.
- **Password reset:** Merchant password reset tokens are supported. Evidence: migration 017.
- **Implementation status:** Implemented. TOTP defaults on in production and production refuses to start with TOTP enabled but no valid key. Evidence: `.env.example:32-33`; README:118.

### 4.3 Authorization (RBAC)

- **Admin roles:** `admin` (full console control), `compliance` (read dashboards + approve merchant registrations), `support` (read dashboards + reset merchant passwords), `readonly` (view dashboards only). Role checks enforced by middleware on every admin route; disabled accounts rejected at session validation. Evidence: README:120-129; migration 022.
- **Customer account levels:** Users carry `verification_level` and `account_level` columns and a KYC tier (see Section 12). Evidence: `store_dsr.go:293-300`; migration 026.

### 4.4 Account Security

- Sessions use cookies that are secure, HTTP-only, and same-site in production; bearer tokens are stored only as SHA-256 hashes. Evidence: README:437.
- CSRF tokens protect admin and merchant POST actions. Evidence: e.g., `app/refunds.go:192`, `app/dsr.go:119`, `app/reconcile.go:42`.
- CSRF tokens are sealed with AES-256-GCM at rest. Evidence: README:152-160.
- **Transaction authentication:** There is no separate transaction PIN. Payment initiation for card goes through the hosted Interswitch Web Checkout; bank-transfer payments are created as Interswitch gateway transactions and verified through the same server-side requery. Evidence: `internal/app/app.go`, `internal/service/payments.go`.

### 4.5 Account Controls & Status

- Merchant payment terms can be set by admins (`/admin/merchants/{id}/payment-terms`). Evidence: `app/admin.go`.
- Settlement accounts have status `pending` / `active` / `disabled` with admin approval and disable actions. Evidence: README:366; `app/admin.go`.
- Admin accounts can be enabled/disabled and have roles reassigned (`/admin/admins`). Evidence: `app/admin.go`.
- **Implementation status:** Implemented.

### 4.6 User Preferences

- Users record consent per purpose (processing / marketing / data_sharing) with grant and revoke. Evidence: `store_dsr.go:20-25`, 61-113.
- **Implementation status:** Implemented.

---

## 5. Conversational & AI Platform

### 5.1 Architecture

The conversational platform is a **rule-based, stateful menu engine**. There is no AI layer: no LLM, no NLP library, no model inference, no prompts, no AI provider integration exists in the repository. The analysis is explicit that any expectation of AI capabilities is not supported by the code.

- **Channel handling:** WhatsApp and Telegram are live messaging channels; SMS is a separate, data-orders-only path. Channel constants are defined in `internal/service/conversation.go:18-27`.
- **Inbound routing:**
  - WhatsApp webhook → `receiveWhatsAppWebhook` (`app/webhooks.go`) → `EnqueueInboundMessage`.
  - Telegram webhook → `receiveTelegramWebhook` (`app/webhooks.go`).
  - SMS webhook → `receiveSMSWebhook` (`app/webhooks.go`) → synchronous `data.HandleSMS`.
- **Processing:** A 2-second ticker drains `inbound_messages` in batches of 20 and calls `conversation.Handle`; failures are retried. Evidence: `app/workers.go`.
- **Outbound:** `messengerFor` (`conversation.go:409`) dispatches text, interactive, checkout, and image messages per channel via `sendText`, `sendInteractive`, `sendCheckout`, `sendImage`.

### 5.2 How a conversational request becomes a platform operation

The analysis supports the following flow (no AI steps exist to insert):

```text
User
 ↓
Channel (WhatsApp / Telegram / SMS webhook)
 ↓
Signature verification & inbound enqueue
 ↓
Drain worker (ClaimInboundMessages)
 ↓
Conversation engine: keyword / intent / menu-state matching
 ↓
Financial / Business command (pay, invoice, thrift, data, KYC)
 ↓
Domain Service (PaymentService / DataService / ...)
 ↓
Store transaction + business event outbox
 ↓
Result
 ↓
Conversational response (sendText / sendInteractive / sendCheckout / sendImage)
```

### 5.3 Capabilities by channel

- **WhatsApp:** text messages, interactive reply buttons, interactive lists (merchant/menu), call-to-action checkout URLs, utility templates outside the service window, and image messages (receipt QR upload). Evidence: `internal/providers/whatsapp/client.go:122-229`.
- **Telegram:** same conversational surface via the Telegram Bot API.
- **SMS:** `DATA <NETWORK> <PLAN> <PHONE>`, `PLANS <NETWORK>`, `STATUS <CODE>`, `DATA HELP`. The reply text is returned in the webhook response (MVP). Evidence: README:259-268.
- **Voice and image understanding:** Not supported. WhatsApp `SendImage` uploads an image that was already generated as bytes (receipt QR); there is no image understanding.

### 5.4 Intent recognition, entity extraction, memory

- Intent recognition is **keyword/menu-state matching** implemented inside `internal/service/conversation.go`.
- Entity extraction (amount, phone number, plan, group code) is done by the same rule engine.
- Conversation state is persisted per session (conversation sessions / message outbox tables from migration 001).
- There is no long-term semantic memory beyond persisted chat/session records; inbound message bodies and outbox payloads are encrypted at rest. Evidence: README:152-160.

### 5.5 AI-to-domain boundary

The boundary between conversation and business operation is the conversation engine invoking domain services. Because there is no AI, there is no AI-generated response, tool-calling, or prompt handling to document.

---

## 6. Payments & Transactions

### 6.1 Payment initiation

- **Card checkout:** A payment is created in draft state, then an Interswitch Web Checkout redirect form is prepared whose fields (merchant code, pay item id, transaction reference, amount in kobo, currency code 566) post to the hosted `/collections/w/pay` page; the customer is directed to a platform page that auto-submits that form. Evidence: `internal/providers/interswitch/client.go`; `app/checkout.go`.
- **Bank transfer:** A draft payment is created on the bank-transfer rail, which is backed by the Interswitch gateway (registered for both `interswitch` and `bank_transfer`). The customer completes the payment on the Interswitch hosted checkout and success is written **only** after the server-side requery verification confirms it. Evidence: `internal/app/app.go`, `internal/service/payments.go`.
- **Partner API:** Merchants initiate via `POST /api/v1/payments` with a merchant `reference` as idempotency key. Evidence: README:341; `app/api_keys.go`.
- **Request-money links / invoices / thrift / data:** These create payments through the same payment service on the card or bank path.

### 6.2 Payment lifecycle and status

Payment states: `draft`, `awaiting_confirmation`, `initialized`, `pending`, `succeeded`, `failed`, `abandoned`, `expired`, `refunded`. Allowed transitions are enumerated (`validTransitions` at `domain/payment.go:32-38`) and enforced by `CanTransition` (`domain/payment.go:40-46`), including during refunds (`store_refunds.go:88-90`). The repository contains immutability triggers on payments/refunds/payouts (migration 044).

### 6.3 Payment verification

- **Card:** Success is written only after a backend requery to Interswitch `gettransaction.json` (InterswitchAuth-signed) and confirmation of the reference, amount, currency, and demo test-mode. Both the callback (`/payments/return`) and the outbound webhook (`TRANSACTION.COMPLETED`) invoke this authoritative server-side requery; neither the redirect notification alone nor the webhook alone marks a transaction successful. Evidence: README:22, 249; `internal/providers/interswitch/client.go`; `app/checkout.go`.
- **Bank transfer:** Success is written only after the customer confirms the transfer. Evidence: README:22.

### 6.4 Payment references

- The Interswitch transaction reference doubles as the payment reference; Partner API uses a merchant `reference` (≤64 chars) as idempotency key (README:341).
- Webhooks are processed idempotently (duplicate webhook does not double-effect), with idempotency keys on the outbox (migration 045) and a `payment.succeeded` event emitted once per payment.

### 6.5 Fees

Two fee systems exist on the platform:

**Settlement fee (legacy):** A settlement fee is configurable via `SETTLEMENT_FEE_BPS` (default 250 = 2.5%). It is calculated at batch-cut time and recorded as a separate ledger entry (`dr 3200 / cr 5200_settlement_fees`); payout amount is always `total_kobo - fee_kobo`. Evidence: README:375; migration 043.

**Per-payment collection fee model:** A fee is computed per payment at collection time using the formula `fee = min(bps × amount / 10000 + fixed, cap)`, configured per payment method via environment variables. Evidence: `internal/service/fees.go`.

| Payment Method | BPS (variable) | Fixed (kobo) | Cap (kobo) | Breakpoint (flat above) |
| -------------- | -------------- | ------------ | ---------- | ---------------------- |
| Card | 200 (2.0%) | 10,000 (₦100) | 350,000 (₦3,500) | ₦170,000 |
| DVA (bank account) | 150 (1.5%) | 0 | 150,000 (₦1,500) | ₦100,000 |
| Bank transfer | 180 (1.8%) | 0 | 250,000 (₦2,500) | ₦139,000 |

- **Collection fee** is charged to the sender (sender pays amount + collection fee). Evidence: `XegoCollectionFee` in `service/fees.go`.
- **Payout (NIP) fee** is ₦100 flat, deducted from the payout amount (recipient receives amount − ₦100). Evidence: `XegoPayoutFee` in `service/fees.go`.
- **Merchant receivable** = payment amount − collection fee − NIP fee (when applicable). Evidence: `MerchantReceivable` in `service/fees.go`.
- Env vars: `CARD_FEE_BPS`, `CARD_FEE_FIXED_KOBO`, `CARD_FEE_CAP_KOBO`, `DVA_FEE_BPS`, `DVA_FEE_FIXED_KOBO`, `DVA_FEE_CAP_KOBO`, `TRANSFER_FEE_BPS`, `TRANSFER_FEE_FIXED_KOBO`, `TRANSFER_FEE_CAP_KOBO`, `NIP_PAYOUT_FEE_KOBO`. Evidence: `.env.example`.

**Payment split posting:** On collection, `ApplyPaymentSplits` posts ledger entries: `dr 2100_customer_float / cr 2300_xego_payable` for the platform fee share, and `dr 2100_customer_float / cr 3100_merchant_payable` for the merchant receivable. Individual payouts post `dr 2100_customer_float / cr 2300_user_payable`. Evidence: `internal/store/store_split_payments.go`; migration 052.

### 6.6 Payment provider integration & webhooks

- **Provider routing:** The `ProviderRouter` tracks per-provider latency (EWMA) and consecutive failures. `PickProvider()` returns the healthiest gateway from the registry. `SetHealthy` can manually override provider status. Evidence: `internal/service/provider_router.go`.
- **Interswitch:** Outbound JSON webhooks (`TRANSACTION.CREATED/UPDATED/COMPLETED`) are authenticated with HMAC-SHA512 via the `X-Interswitch-Signature` header using the dashboard webhook secret (`INTERSWITCH_WEBHOOK_SECRET`), recorded, and deduped. A missing or invalid signature is rejected with `401`. Only terminal `TRANSACTION.COMPLETED` events trigger an authoritative server-side requery (`Verify`) via `VerifyAndApply`. A background reconciler requeries any unresolved Interswitch payment after 30s. Evidence: `internal/providers/interswitch/client.go`; `app/webhooks.go`.
- **Multi-provider webhook worker:** The `processGatewayWebhooks` worker iterates all registered providers via `ProviderList()`, drains each provider's webhook queue, and calls `VerifyAndApply` for each. Evidence: `app/workers.go`.
- Terminal events (`payment.succeeded`, `payment.failed`) flow through the outbox to consumers: notifications, compliance monitoring, and merchant webhook delivery. Evidence: `app/workers.go`.

### 6.7 Retries, idempotency, failures

- Inbound webhooks are claimed and retried (`RetryWebhook`); payments reconcile on a 1-minute ticker (`app/workers.go`).
- Partner API payment creation is idempotent on `reference` (replaying returns the existing payment). Evidence: README:341.
- Failed payments emit `payment.failed`; failed webhook deliveries are retried with exponential backoff and dead-lettered as `failed` in `merchant_webhook_deliveries`. Evidence: README:394.

### 6.8 Refunds, reversals, disputes

See Section 10.

---

## 7. Transfers

### 7.1 What "transfer" means on this platform

The platform distinguishes three money movements:

1. **Customer → merchant payment** (card and bank transfer both via Interswitch Web Checkout; verification-driven success).
2. **Merchant settlement payout** (batch → merchant settlement account) through the **simulated payout rail**.
3. **Refund** (merchant → customer reversal) through the **simulated refund rail**.

There is **no peer-to-peer transfer or general money-transfer product**. "Transfers" in the README are documented as bank-transfer payments (customer-initiated) and settlement payouts.

### 7.2 Bank-transfer payments (customer side)

- **Initiation:** In chat, the customer selects **Bank transfer** and chooses a collection bank (browsable/searchable list).
- **Beneficiary handling / validation:** Bank-transfer payments are completed on the Interswitch hosted checkout; the recipient payout for individual pay is booked by the post-success settlement hook.
- **Provider interaction:** The bank rail is a **simulation** (`bank_transfer_simulations`), with status `user_confirmed`.
- **Status:** The payment transitions to succeeded only after confirmation.
- **Ledger implications:** On success, the money-in posting is debited to `1100_operating_bank` with `source_type='payment'`. Evidence: `store_reconcile.go:84-87`.
- **Reconciliation:** Bank-transfer payments reconcile through the internal-vs-ledger legs like every other gateway payment (the simulated bank rail leg was retired with the rail). Evidence: `store_reconcile.go`.
- **Implementation status:** Implemented end-to-end with a simulated bank rail.

### 7.3 Settlement payouts (merchant side)

See Section 9. The payout provider is a deterministic simulator: bank code `011` always declines; any other bank code succeeds and returns `external_ref = SIM-PAY-<reference>-<unix>`. Idempotency key is the payout row id. Evidence: README:368.

---

## 8. Financial Core & Ledger

### 8.1 Double-entry accounting

Xego implements a **double-entry, append-only, hash-chained ledger** — every money movement is recorded as balanced debit and credit entries in `ledger_entries`:

- `entry_type` is constrained to `debit` or `credit`; `amount_kobo` must be positive.
- Entries reference a journal (source), and `reversal_of` points at original entries for reversals.
- Every entry carries `prev_hash`/`hash` (SHA-256 chain) and a unique hash; an append-only trigger rejects updates and deletes. Evidence: `store_ledger.go:55-62`; migrations 030, 040; README:162-164.
- Balanced pairs are written atomically via `postLedgerPair` (`store_ledger.go:102-124`), with serialization on the tail row (`FOR UPDATE`) to keep the chain consistent.

### 8.2 Chart of accounts

Evidence: `store_ledger.go:22-32`.

| Account | Role |
| ------- | ---- |
| 1100_operating_bank | Money-in destination for payments; money-out for payouts |
| 2100_customer_float | Float between money-in and payable |
| 1300_settlement_suspense | Suspense account (per migration 030) |
| 3100_merchant_payable | Merchant's collected-but-not-settled balance |
| 3200_settlement_payable | Settled-but-not-paid balance |
| 4100_sales_revenue | Platform revenue recognition |
| 5100_provider_cost | Provider cost |
| 5200_settlement_fees | Settlement fee revenue |
| 6100_thrift_pool | Thrift group pool |
| 2300_user_payable | Individual payout liability (customer-to-customer payouts) |
| 2300_xego_payable | Xego platform fee share (collection fee revenue) |

### 8.3 Balances, journal entries, reversals

- Balances are exposed through `LedgerBalanceSummary` (`app/ledger.go:23`) and Partner API `/balance`, which reports merchant-scoped net positions per account ("derived from the append-only ledger"). Evidence: README:348-359.
- Ledger balances must net to zero; the admin ledger page shows the per-account balance and its total. Evidence: `app/ledger.go:35-45`.
- **Reversals** post offsetting entries for a journal reference without mutating original rows (admin console), and refunds reverse the original payment's entries transactionally. Evidence: `app/ledger.go:52-80`; `store_refunds.go:185-237`.
- **Caveat (Unclear):** The exact SQL of `LedgerBalanceSummary` was not line-verified in the analysis; its existence and call sites are established, the aggregation implementation is not. Balance derivation is treated as established (README:348) but its implementation detail remains unverified.

### 8.4 Relationship between Payment, Ledger, Settlement, Reconciliation

```text
Payment (state machine + verification)
    ↓ money-in (dr 1100 / cr ...)
Ledger (append-only journal, merchant-scoped)
    ↓ batch cut (3100 → 3200, + fee entry)
Settlement (open → scheduled → processed)
    ↓ payout (dr 3200)
Reconciliation (payments vs ledger vs bank rail, per run)
    ↓ discrepancies → investigation
```

Money-in postings use `source_type='payment'`; payout money-out uses `source_type='payout'` — reconciliation keys on these. Evidence: `store_reconcile.go:83-100, 227-247`.

### 8.5 Accounting constraints enforced in code

- Refund precondition: payment must be `succeeded` and not already refunded; reversal refuses already-reversed journals. Evidence: `store_refunds.go:85-99, 214-221`.
- Settlement line removal on refund only allowed while the batch is `open`. Evidence: `store_refunds.go:103-130`.
- Reconciliation persists every discrepancy with expected vs actual amounts for the CBN paper trail. Evidence: `store_reconcile.go:43-301`.

---

## 9. Settlement & Payouts

### 9.1 Settlement creation and calculation

- A settlement **batch cut** freezes the merchant's succeeded, un-batched payments and moves funds from `3100 merchant_payable` to `3200 settlement_payable`. Evidence: README:360; migration 041.
- The fee is computed at cut time using `SETTLEMENT_FEE_BPS` (default 250 bps) and booked separately (`dr 3200 / cr 5200_settlement_fees`). Evidence: README:375.
- Batch statuses: `open`, `scheduled`, `processed`, `failed`. Evidence: README:361; `store_refunds.go:109`.

### 9.2 Payout processing

- A payout is requested for an open batch to the merchant's default active settlement account: statuses `queued`, `processing`, `completed`, `failed`, `reversed`. Evidence: README:362-364.
- A 10-second ticker dispatches queued payouts (`app/workers.go`). Payout max attempts = 3.
- The default provider is `simulated`: bank code `011` always declines (tests failure/retry/reverse); any other code succeeds with `external_ref = SIM-PAY-<reference>-<unix>`. Idempotency key is the payout row id, so a retry of the same payout replays its outcome while a reversed payout (a fresh row) gets a fresh outcome. Evidence: README:368.
- **Payout reversal:** reverses a failed/processing payout, reopens its batch, and (merchant-only) releases funds back to `3100 merchant_payable`; a fresh payout can then be requested. Evidence: README:363; `app/settlements.go`.
- **Bank interactions:** Payout destinations are settlement accounts (`bank_code` NIP code, `account_number`, `account_name`); status `pending`/`active`/`disabled`, admin approve/disable. Evidence: README:365-366.

### 9.3 Reconciliation

Completed payouts must have a matching money-out posting on `3200` (reconciliation leg 4). Evidence: `store_reconcile.go:198-267`.

### 9.4 Implementation status

Implemented end-to-end with a **simulated payout rail**. No live bank integration exists in the repository.

---

## 10. Refunds & Disputes

### 10.1 Refunds

- **Scope:** Full refunds only. The refund amount is the succeeded payment's amount; the analysis does not establish partial refunds. Evidence: `store_refunds.go:81`.
- **Who initiates:** Merchants self-serve via Partner API (`POST /api/v1/payments/{reference}/refund`); operators can request a refund from the admin console. Both routes create a `pending_approval` refund requiring a different admin to approve.
- **Preconditions:** Payment must be `succeeded` (status enforced), must not have an active refund, and must not be in a `scheduled`/`processed` settlement batch (reverse the payout first). Evidence: `store_refunds.go:85-115`; `app/refunds.go:65-75`.
- **Atomic flow (`RequestRefund`, `store_refunds.go`):** lock payment `FOR UPDATE` → validate preconditions → if in an open batch, remove the settlement line and recompute batch totals → insert refund (`pending_approval`) → emit `payment.refunded` → commit. All in one transaction.
- **Approval flow (`ApproveRefund`):** A **different** admin approves → transitions refund to `pending` → posts the ledger reversal via offsetting entries → transitions payment to `refunded` → records `payment_events`. Evidence: `store_refunds.go`.
- **Refund statuses:** `pending_approval`, `pending`, `succeeded`, `failed`. The default rail is **simulated** — always succeeds with `provider_refund_id = SIM-REF-<payment_id>-<unix>`. Evidence: README:374.
- **Provider interactions:** `CompleteRefund` / `FailRefund` (`store_refunds.go:240-265`). A live rail plugs in behind the same interface.

### 10.2 Disputes

- **States:** `open`, `won`, `lost`, `expired` (`store_refunds.go:29-34`).
- **Creation:** `CreateDispute` records a provider-initiated dispute against a payment and emits `payment.disputed`. Evidence: `store_refunds.go:334-365`.
- **Resolution:** Admin console resolves as `won` or `lost` (`app/refunds.go:234-257`); `ResolveDispute` also accepts `expired`. Evidence: `store_refunds.go:368-382`.
- **Notifications:** `payment.disputed` is delivered to merchants via webhooks. Evidence: `service/merchant_webhooks.go:108-115`.

### 10.3 Accounting effects

- Refund reverses the original payment's ledger entries (offsetting, not mutation) and, if in an open batch, removes the line and recomputes the batch totals. Evidence: `store_refunds.go:118-161`.
- Dispute outcomes do not post ledger entries per the analysis; dispute resolution is a state transition only.

### 10.4 Implementation status

Implemented end-to-end; both refund and dispute provider rails are simulated.

---

## 11. Reconciliation

### 11.1 Architecture

The analysis establishes **three-way reconciliation** (plus a payout leg) between:

```text
External Provider (Interswitch verification for card and bank transfer)
      ↓
Xego Transaction Records (payments state machine)
      ↓
Xego Ledger (money-in postings)
      ↓
Settlement / Bank Records (confirmed transfers, completed payouts)
      ↓
Reconciliation Run (auto daily + manual weekly)
      ↓
Discrepancies → Admin review (reconciliation_items persisted)
```

### 11.2 Matching logic (`RunReconciliation`, `store_reconcile.go:43-301`)

- **Leg 1 — Internal:** succeeded payments (reference, amount, currency, provider).
- **Leg 2 — Ledger:** money-in postings (`debit`, account `1100_operating_bank`, `source_type='payment'`, unreversed), summed per journal ref.
- **Leg 3 — Bank rail:** retired with the simulated bank rail; bank-transfer payments reconcile through the internal-vs-ledger legs.
- **Leg 4 — Payouts:** completed payouts must have matching money-out postings on `3200` with `source_type='payout'`.

Checks produce categorized discrepancies such as `internal_without_ledger`, `amount_mismatch`, `ledger_without_internal`, `bank_without_internal`, `bank_amount_mismatch`, `internal_without_bank`, `payout_without_ledger`, `payout_amount_mismatch`, `ledger_without_payout`.

### 11.3 Jobs, exceptions, auditability

- **Scheduled:** daily automatic run (24h ticker, `app/workers.go`; `runReconciliationAuto` `app/reconcile.go:62-74`).
- **Manual:** weekly manual run triggered from `/admin/reconciliation` (CSRF-protected), audited as `admin.reconciliation.ran`. Evidence: `app/reconcile.go:41-58`.
- **Persistence:** `reconciliations` + `reconciliation_items` (migration 031); the admin page shows recent runs and the latest run's discrepancies.
- **Paper trail:** run type (`auto`/`manual`), creator, counts, discrepancy count, and status (`clean`/`discrepancies`) are stored. Evidence: `store_reconcile.go:16-27`.

### 11.4 Implementation status

Implemented. The analysis notes reconciliation is best-effort — it detects and persists discrepancies; it does not automatically correct them.

---

## 12. KYC, Compliance & Risk

### 12.1 KYC tier ladder

- Tiers **L0** (unverified) → **L1** (channel + email confirmed) → **L2** (identity on file) → **L3** (NIN/BVN verified) → **L4** (enhanced due diligence). Evidence: README:176-186; migration 026.
- Advancement must be **adjacent** and gated on matching evidence: `channel_confirmed`, `identity_on_file`, `nin_or_bvn_verified`, `edd_completed`. Downgrades can drop to any lower tier.
- A sanctions/PEP screening decision of `strong`/`blocked` halts advancement until `clear`/`manually_cleared`.
- Enforced in the store (`AdvanceKYCTier`/`AdvanceKYCTierTo`/`DowngradeKYCTier`); every transition, screening, and review is audited. Evidence: README:186.
- Compliance manages the ladder and manual review queue at `/admin/kyc` (admin/compliance roles). Evidence: `app/admin.go`.
- **Status:** Implemented. **Note (Unclear):** the analysis did not establish a documented demo path for advancing beyond L3 (EDD evidence exists in code; README:184 lists it, the demo narrative stops at L3).

### 12.2 Identity verification (NIN/BVN)

- Implemented through the `IdentityVerifier` port. The `simulated` provider is deterministic: a NIN starting with `1` or a BVN starting with `2` verifies; `8…` mismatches; `9…` is not found. A live vendor (Smile ID / Youverify / Dojah / Prembly) plugs in behind the same interface. Evidence: README:190.
- **Status:** Implemented with a simulated provider.

### 12.3 Sanctions / PEP screening

- Implemented through the `SanctionsScreener` port. The `simulated` provider returns `strong` for a name containing a sanctions token ("Sanctioned"/"Drug Lord"), `possible` for a PEP token ("Senator"/"Minister"), else `clear`. A live OFAC/vendor list plugs in behind the same interface. Evidence: README:188.
- Screening runs at onboarding before the L2 promotion; a background worker re-screens profiles every `KYC_RESCREEN_PERIOD` (default 90 days); a `strong`/`blocked` rescreen downgrades to L1 and opens a review case. Evidence: README:188; `rescreen` command.
- **Status:** Implemented with a simulated provider.

### 12.4 ML/FT risk scoring

- `internal/kyc.ScoreRisk` aggregates scored risk observations (`risk_events`) with the tier and last screening decision into a 0–100 score and a `low`/`medium`/`high` band. Evidence: README:192; migration 027.
- Any `RecordRiskEvent` recomputes the band transactionally and audits `kyc.risk_scored`; rescreen worker and `recompute-risk` command refresh it. Evidence: README:192.
- **Status:** Implemented.

### 12.5 Transaction monitoring (AML)

- Rules (evidence `kyc/monitor.go:82-197`): **velocity** (more than N settled payments per window), **structuring** (several payments just under a threshold), **round amounts** (clean multiples), and **high-risk counterparty categories**.
- Defaults: velocity window 24h/limit 10; structuring 24h/count 3/floor ₦40,000/ceil ₦100,000; round step/min ₦10,000; high-risk categories `Gambling,Forex,Crypto`. Evidence: `.env.example:91-100`.
- Alerts: severities `low`/`medium`/`high` mapping to risk-score contributions 15/35/60 (`kyc/monitor.go:32-40`). Medium/high alerts open a `monitoring` manual review case.
- Runs as a periodic 15-minute job (`app/workers.go`) and as an event consumer on `payment.succeeded` (`service/eventbus.go:98-132`).
- **Status:** Implemented.

### 12.6 Regulatory reporting

- STR, CTR, and PEP reports are generated from audited store data and exported as CSV. CTR uses `ReportCTRThresholdKobo`. Evidence: `app/reports.go:38-90`; `internal/reports`.
- **Status:** Implemented as CSV file exports. **Not established:** an automated submission pipeline to regulators.

### 12.7 Allowance ladder (KYC/KYB tier ceilings)

- **Purpose.** The flat global cap has been layered with a DB-driven allowance ladder. Every money-in (customer payment) and money-out (merchant settlement payout, individual-pay disbursement) movement against a subject is validated against that subject's tier ceilings before its payment/payout row is created, so a rejected reservation stops creation atomically. Evidence: `service/payments.go` (`CreateDraftForProvider`), `service/settlement.go` (`RequestPayout`), `service/conversation_individual_pay.go`.
- **Ceilings.** Per `(account type, tier, direction)` row in `kyc_tier_limits`, in kobo: single, daily, and monthly, with `single ≤ daily ≤ monthly`. CBN-aligned defaults (migration 053), equal for money-in and money-out: individuals L0 single ₦20k / daily ₦20k / monthly ₦100k; L1 ₦50k/₦50k/₦300k; L2 ₦200k/₦200k/₦500k; L3 ₦1m/₦1m/₦10m; L4 ₦5m/₦5m/₦50m. Businesses B0 ₦200k/₦200k/₦1m; B1 ₦1m/₦1m/₦5m; B2 ₦5m/₦5m/₦50m; B3 ₦10m/₦10m/₦100m. Administrators edit any row live in the console (see below).
- **Usage accounting.** `allowance_usage` is append-only, keyed by `transaction_ref` for idempotent re-application (a replay is a no-op). Daily/monthly rollups sum on the **Lagos (WAT, UTC+1) calendar** (`kyc.DayWindowStart`/`kyc.MonthWindowStart`). Reservations for the same subject+direction are serialized with `pg_advisory_xact_lock`, so concurrent payments cannot each pass validation.
- **Release.** A money-in reservation is released centrally when a payment lands in `failed`/`abandoned`/`expired`/`refunded` (`store/store_payments.go` `transitionPayment`). Payout reservations are keyed `payout:<batchNo>` and released only when the payout is reversed (`store_settlements.go` `ReversePayout`); a failed payout keeps its reservation because a retry is the same intent.
- **Business KYB ladder.** `business_kyb_profiles` holds each merchant's position on B0–B3 (the KYB twin of `kyc_profiles`). Advancement is adjacent and evidence-gated (`business_docs_verified` → `business_bank_verified` → `business_edd_completed`); past B0 it requires a non-blocked sanctions/PEP decision. Existing merchants were grandfathered to B0/approved with `["registration_approved"]` evidence. A blocked **business** rescreen (same periodic job that serves individuals) forces the merchant back to B0 and immediately lowers the ceilings `ReserveAllowance` enforces. Evidence: `internal/app/cli.go` (`RescreenDue`), `RecordKYBScreening`, `BusinessKYBProfilesDueForRescreen`.
- **Customer-facing visibility.** WhatsApp menu *My limits* shows tier, per-payment/day/month ceilings, and used/remaining for today and the month; *KYB status & limits* does the same for merchants. Allowance rejections are rewritten into actionable copy ("complete more verification…", upgrade prompt) by `conversation_allowances.go`.
- **Administration.** `/admin/kyb` (admin/compliance roles) lists every business profile with review actions and advance-to-next-tier forms, and renders every `kyc_tier_limits` row as an inline editor. Changes are audited (`admin.tier_limits.updated`, `admin.kyb.reviewed`; tier moves audit as `kyb.tier_advanced`/`kyb.tier_downgraded`). Evidence: `app/admin_kyb.go`.
- **Status:** Implemented, gated on Postgres (migration 053).

### 12.8 Transaction limits (global platform bounds)

- `PAYMENT_MIN_KOBO=10000` and `PAYMENT_MAX_KOBO=10000000` (₦100–₦100,000) remain the **absolute** platform floor and ceiling independent of tier ceilings. An amount is first constrained to this window and then to the subject's tier allowance. Evidence: `.env.example:102-103`; README:402.
- **Status:** Implemented.

### 12.9 Compliance summary by status

**Implemented:** KYC ladder, KYB ladder for merchants, screening decisions, allowance ceilings with reservation enforcement, risk scoring, transaction monitoring rules, review queues, DSR (Section 20 workflow), legal holds, retention, SIEM, chat guard, RBAC, STR/CTR/PEP file reports, global transaction limits.

**Partially implemented:** Identity and sanctions screening (simulated providers; live vendor plug-in required), email confirmation (SMTP optional, chat demo mode).

**Scaffolded / Planned:** No live regulatory submission pipeline.

**Not established by the analysis:** any regulatory certification (CBN/NDPR/PCI claims). The README explicitly notes the build is not a licensed payment processor.

---

## 13. Security

### 13.1 Authentication

- **Admins & merchants:** password + TOTP two-step login; enrollment shows QR; secrets encrypted AES-256-GCM; disabled accounts rejected. Evidence: README:109-129.
- **Customers:** channel-identity based.
- **Partner API:** HMAC-SHA256 over `METHOD\nPATH\nTIMESTAMP\nBODY`, 5-minute timestamp window, key doubles as signing secret; only the SHA-256 hash of the key is stored. Evidence: README:318-338; `app/api_keys.go:91-131`.
- **Implementation status:** Implemented.

### 13.2 Authorization

- RBAC middleware on every admin route (`app/auth.go`); CSRF on every privileged POST. Evidence: README:129; `app.go`.

### 13.3 Encryption

- **At rest:** Chat payloads (inbound messages + outbox) and admin/merchant session CSRF tokens are sealed with AES-256-GCM (`internal/crypto`), version-prefixed `enc:v1:`; migration 024 converts chat payload columns. Evidence: README:152-160.
- **In transit:** TLS terminated by Caddy (`compose.yaml:79-95`); PostgreSQL with SSL (`compose.yaml:6`).
- **Session tokens** stored only as SHA-256 hashes; cookies secure/HTTP-only/same-site in production. Evidence: README:437.

### 13.4 Secrets & credentials

- API keys and webhook secrets are shown exactly once and stored sealed/hashed; rate-limited login endpoints. Evidence: README:318, 383; `.env.example:12-37`.
- Provider error strings are redacted before persistence/logging (`internal/redact`): keys, bearer tokens, card numbers, phone numbers, emails, credentials in URLs are masked. Evidence: README:150.

### 13.5 Webhook verification

- WhatsApp `X-Hub-Signature-256` (HMAC-SHA256, raw body) — `whatsapp/client.go:51-66`.
- Interswitch outbound webhook (`X-Interswitch-Signature`, HMAC-SHA512 with dashboard webhook secret; hex-encoded; required) — `internal/providers/interswitch/client.go`.
- Telegram secret token — `app/webhooks.go`.
- SMS shared secret (`X-SMS-Webhook-Secret` or `X-Xego-SMS-Secret`) — `app/webhooks.go`.
- VTPass webhook secret (header only, never query parameter) — `app/webhooks.go`.

### 13.6 Rate limiting

- Fixed one-minute per-IP windows: webhooks 120/min, public 60/min, scan 30/min, Partner API 300/min per key, admin console 120/min per IP on all `/admin/*` routes. Evidence: README:131-140; `.env.example:114-118`; `app.go`.
- Redis-backed when `REDIS_URL` set; in-memory per-process otherwise; **fails open** on Redis outage (an unavailable cache never blocks payments). Evidence: README:140.

### 13.7 Sensitive data handling

- Card numbers, CVV, PIN, OTP, balances, and reusable authorization data are never stored. Webhook bodies are read once for signature verification; only normalized fields are queued. Evidence: README:435-436.
- Chat guard blocks card/PIN/CVV/OTP material and records only redacted copies for compliance review. Evidence: README:435; `app/chatguard.go`.

### 13.8 Audit logging & security monitoring

- Hash-chained `audit_logs` (SHA-256 of previous row; append-only trigger). Evidence: README:142-144; migration 023.
- SIEM unified event log of domain events + privileged actions with CSV/JSON export. Evidence: `app/siem.go`.
- Structured JSON logging with request correlation IDs (`X-Request-ID`). Evidence: README:146-148.

### 13.9 Not established

- No evidence of an automated security-monitoring/alerting pipeline beyond SIEM/audit; no documented penetration-testing or certification results.

---

## 14. Messaging & Notifications

### 14.1 WhatsApp

- **Triggering events:** conversational replies, payment status updates, checkout URLs, receipt images, templates outside the service window.
- **Delivery mechanism:** WhatsApp Cloud API (`graph.facebook.com/{version}/{phone}/messages`), text/interactive/list/CTA-URL/template/image. Evidence: `whatsapp/client.go:122-229`.
- **Templates:** a utility template named by `WHATSAPP_STATUS_TEMPLATE` with four body parameters (status, amount, merchant, receipt URL). Evidence: README:218-222.
- **Retry/failure:** outbound failures surface as errors to the conversation worker, which retries the inbound message (`app/workers.go`). No separate outbound retry queue was established.

### 14.2 Telegram

- **Triggering events:** conversational replies via the same engine, through `messengerFor`.
- **Delivery:** Telegram Bot API.
- **Status:** Implemented.

### 14.3 Email

- **Triggering events:** merchant-registration email confirmation (6-digit OTP). Standard user onboarding does not require email OTP. Evidence: README:90-92.
- **Delivery:** SMTP (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`); chat-delivered demo code option for rehearsal; refused in production. Evidence: `.env.example:20-27`; README:107.
- **Status:** Implemented; SMTP is Configured Only (empty defaults) and delivery is optional in practice.

### 14.4 SMS

- **Triggering events:** inbound data-order commands.
- **Delivery mechanism:** the reply text is returned in the webhook response (MVP). **No outbound SMS sender exists**; README:268 explicitly notes a live sender can be connected later without changing the order lifecycle.
- **Status:** Partially Implemented.

### 14.5 Internal notifications & push

- **Merchant payment notification:** on `payment.succeeded`, a notification consumer claims the merchant-notification slot atomically and notifies the merchant (chat). Evidence: `service/eventbus.go:66-92`.
- **Merchant webhooks:** signed HTTP deliveries (see Section 6.6). Evidence: `service/merchant_webhooks.go`.
- **Push notifications:** not established by the analysis.

---

## 15. Merchant & Partner Capabilities

### 15.1 Merchant onboarding

- Email-confirmed registration request → admin approval → payable merchant record + owner link + invoice features unlocked. Evidence: README:94; `app/admin.go`.
- **Status:** Implemented.

### 15.2 API access & authentication

- Partner API keys minted in the merchant dashboard; shown exactly once; SHA-256 hash stored; HMAC-signed requests with 5-minute timestamp window; per-key rate limit 300/min. Evidence: README:318-338.
- **Status:** Implemented.

### 15.3 Partner API operations

- Payment create / status / verify; invoice create / status; checkout create / status; balance; settlements cut / status / payout / payout-reverse; payout status; settlement-account create / list; refund; refund status; disputes list / detail. Routes: `app/api_keys.go`. See Section 18.
- **Status:** Implemented.

### 15.4 Webhooks

- Configuration in merchant dashboard; callback URL + signing secret (shown once, sealed at rest); HMAC-SHA256 signature; retry with exponential backoff; dead-lettered as `failed`. Evidence: README:381-394; `service/merchant_webhooks.go`.
- **Status:** Implemented.

### 15.5 Settlement & reporting

- Merchants cut batches, request payouts, reverse payouts, manage settlement accounts, and read balances via the Partner API; the admin console adds approval/disable and payout retry/reverse. Evidence: README:360-368.
- **Status:** Implemented.

### 15.6 Merchant configuration

- Merchant console: scanner, invoices, payments, settings (webhook URL/secret rotation, profile, TOTP), services catalogue with per-service payments and phone whitelists. Evidence: `app/merchant.go`.
- **Status:** Implemented.

### 15.7 Not established

- No merchant KYC/KYB flow beyond registration approval; no merchant-to-merchant payouts.

---

## 16. Administration & Operations

### 16.1 Admin console

All admin routes require login; RBAC middleware gates sensitive actions. Routes (evidence `app/admin.go`, `app.go` routes function):

| Area | Routes / Actions |
| ---- | ---------------- |
| Metrics | `/admin/metrics` |
| Users | `/admin/users` (masked) |
| Merchants | list; approve registrations (admin/compliance); set password (admin/support); set payment terms (admin) |
| Payments | `/admin/payments` |
| Data orders | `/admin/data-orders` |
| Thrift | `/admin/thrift`; complete simulated payout (admin) |
| Accepted numbers | view/update demo invoice allow-list (admin) |
| Scanning | list; create services/readers; phone whitelists (admin) |
| Webhooks | `/admin/webhooks` (VTPass callback records) |
| Audit | `/admin/audit` (admin/compliance), hash-chain integrity |
| Reports | STR/CTR/PEP view + downloads (admin/compliance) |
| Data subjects | dashboard, lookup, export, requests, resolve (admin/compliance) |
| Archive | `/admin/archive` (immutable archived records) (admin/compliance) |
| Legal holds | list (admin/compliance); add/remove (admin) |
| Ledger | journal, balances, chain verification (admin/compliance); reversals (admin) |
| Reconciliation | runs + discrepancies (admin/compliance); manual run (admin/compliance) |
| Chat guard | blocked-attempt log (admin/compliance) |
| KYC | profiles, tiers, screening, review queue approve/reject (admin/compliance) |
| Monitoring | resolve transaction alerts (admin/compliance) |
| Admins | create, role change, enable/disable, password reset (admin) |
| Settlements | accounts approve/disable (admin); payout retry/reverse (admin) |
| Refunds | list all (admin/compliance); fail pending refund (admin) |
| Disputes | list all (admin/compliance); resolve won/lost (admin) |
| SIEM | event log + CSV/JSON export (admin/compliance) |
| Analytics | dashboard + CSV/JSON export (admin/compliance) |

### 16.2 Operational controls & approvals

- **Maker-checker (refunds):** Refunds use a two-step maker-checker flow. Merchants or operators call `RequestRefund` which creates a refund with `approval_status='pending_approval'` (migration 046). A **different** admin must call `ApproveRefund` to execute the refund; `FailRefund` blocks `pending_approval` refunds. All role gates enforced; audit-logged. Evidence: `store_refunds.go`; `app/refunds.go`.
- **Other sensitive actions** (payout reverse, ledger reversal, dispute resolution) remain single-actor with audit logging.
- All privileged actions are audited (actor, IP, action, resource, details) with hash-chained rows. Evidence: README:144.

### 16.3 CLI operations

`reconcile` (Interswitch re-verify), `reconcile3` (three-way), `settle <merchant-id> <batch-no>`, `refund <merchant-id> <payment-reference>`, `retain` (retention purge), `rescreen`, `recompute-risk`, `monitor` (transaction monitoring), `reports <dir>` (STR/CTR/PEP export), `sync-vtpass-data-plans`, `health`. Evidence: `cmd/demo/main.go:82-134`.

---

## 17. External Integrations

| Integration | Purpose | Xego Component | Direction | Status |
| ----------- | ------- | -------------- | --------- | ------ |
| Interswitch Web Checkout | Card payment initialize (form redirect), requery, outbound webhook | `internal/providers/interswitch/client.go` | Outbound (API) + Inbound (webhook) | Real HTTP, **test-key only** |
| WhatsApp Cloud API | Customer chat, checkout links, templates, images | `internal/providers/whatsapp/client.go` | Bidirectional | Real HTTP |
| Telegram Bot API | Customer chat | `internal/app` webhook + bot client | Bidirectional | Real HTTP |
| SMTP | Merchant-registration email confirmation | `internal/providers/email` | Outbound | Real (Configured Only; empty defaults) |
| VTPass | Data plan catalogue + fulfilment | `app/webhooks.go`; `sync-vtpass-data-plans`; vtpass provider | Bidirectional (webhook + API) | Sandbox HTTP |
| Payout rail | Settlement payouts | `internal/providers/payout/simulated.go` | Outbound | **Simulated** |
| Refund rail | Refunds | `internal/providers/refund/simulated.go` | Outbound | **Simulated** |
| Data fulfilment | Mobile data orders | `internal/providers/data` | Outbound | Simulated default (`DATA_PROVIDER=simulated`) |
| Identity (NIN/BVN) | KYC L3 | `internal/providers/identity` | Outbound | **Simulated** |
| Sanctions/PEP screening | KYC screening | `internal/providers/screening` | Outbound | **Simulated** |
| Redis | Distributed rate limiting | `internal/ratelimit` | Internal | Configured Only (empty `REDIS_URL` → in-memory) |
| Kafka | Event bus | `internal/bus/kafka` | Internal | Configured Only (default `EVENT_BUS=memory`) |
| S3-compatible storage (OCI/AWS/MinIO) | Backup shipping | `deploy/backup.sh` (rclone) | Outbound | Deployment |
| Caddy | TLS termination / reverse proxy | `compose.yaml` | — | Deployment |

**Not present in the analysis:** any AI provider, any live bank/NIP provider, any live identity/screening vendor wiring, push-notification provider.

---

## 18. API & Interface Capabilities

### 18.1 Partner API (`/api/v1`, HMAC-signed)

All routes require the HMAC-signed headers (see Section 13.1). Errors use the envelope `{"error":{"code":"...","message":"..."}}`. Evidence: README:379; `app/api_keys.go`.

| Group | Endpoint | Purpose | Auth | Status |
| ----- | -------- | ------- | ---- | ------ |
| Payments | `POST /payments` | Initiate card payment (idempotent on `reference`) | API key HMAC | Implemented |
| | `GET /payments/{reference}` | Payment state | API key HMAC | Implemented |
| | `POST /payments/{reference}/verify` | Re-check against gateway | API key HMAC | Implemented |
| | `POST /payments/{reference}/refund` | Full refund | API key HMAC | Implemented |
| Invoices | `POST /invoices` | Create invoice from line items (idempotent on `reference`) | API key HMAC | Implemented |
| | `GET /invoices/{reference}` | Invoice state incl. `amount_paid` | API key HMAC | Implemented |
| Checkouts | `POST /checkouts` | Mint request-money link | API key HMAC | Implemented |
| | `GET /checkouts/{reference}` | Checkout state | API key HMAC | Implemented |
| Balance | `GET /balance` | Ledger-derived settlement position | API key HMAC | Implemented |
| Settlements | `POST /settlements` | Cut batch (idempotent on `batch_no`) | API key HMAC | Implemented |
| | `GET /settlements/{batch_no}` | Batch state | API key HMAC | Implemented |
| | `POST /settlements/{batch_no}/payout` | Request payout | API key HMAC | Implemented |
| | `POST /settlements/{batch_no}/payout/reverse` | Reverse payout, reopen batch | API key HMAC | Implemented |
| Payouts | `GET /payouts/{reference}` | Payout state | API key HMAC | Implemented |
| Settlement accounts | `POST /settlement-accounts` | Register payout destination | API key HMAC | Implemented |
| | `GET /settlement-accounts` | List accounts | API key HMAC | Implemented |
| Refunds | `GET /refunds/{id}` | Refund status | API key HMAC | Implemented |
| Disputes | `GET /disputes` | Merchant's disputes | API key HMAC | Implemented |
| | `GET /disputes/{id}` | Dispute detail | API key HMAC | Implemented |

Side effects / events: payment creation writes payment rows; verification transitions state and emits `payment.succeeded`/`payment.failed`; refund emits `payment.refunded`; settlement/payout emit `settlement.batch.*` and `payout.*`; disputes emit `payment.disputed`. All events flow through the transactional outbox.

### 18.2 Webhook endpoints (inbound)

| Route | Source | Verification | Status |
| ----- | ------ | ------------ | ------ |
| `GET /webhooks/whatsapp` | Meta verification | Verify token | Implemented |
| `POST /webhooks/whatsapp` | Meta | `X-Hub-Signature-256` | Implemented |
| `POST /webhooks/telegram` | Telegram | `X-Telegram-Bot-Api-Secret-Token` | Implemented |
| `POST /webhooks/sms` | SMS provider | Shared secret | Implemented |
| `POST /webhooks/interswitch` | Interswitch | Redirect notification (HMAC-SHA512 optional); outcome via requery | Implemented |
| `POST /webhooks/vtpass` | VTPass | `X-VTPass-Webhook-Secret` (header) | Implemented |

### 18.3 Public web pages

`/payments/return` (checkout callback), `/checkout/{token}` (+ `/pay`), `/link/{token}` (+ `/resolve`), `/receipts/{token}` (+ scan QR), `/invoices/{reference}`, `/thrift/{name}`, `/scan/{token}`, `/api/readers/scan` (scanner API, reader key, scan rate limit). Evidence: `app/checkout.go`; routes in `app.go`.

### 18.4 Admin / merchant console interfaces

Documented in Section 16.1 and 15.6 (HTML/htmx, cookie sessions).

---

## 19. Events & Asynchronous Processing

### 19.1 Event backbone

- **Transactional outbox:** producers write business facts into `business_event_outbox` inside their own transaction (migrations 033, 045). A publisher drains it (1s ticker), publishes to the bus, retries on failure, and marks complete. At-least-once semantics; consumers must be idempotent. Evidence: `service/eventbus.go:34-50`; idempotency-key dedup (migration 045).
- **Bus:** `EVENT_BUS=memory` (default, Kafka-compatible in-memory) or `kafka` (segmentio/kafka-go, requires `KAFKA_BROKERS`). Evidence: `.env.example:120-127`.

### 19.2 Event/worker table

| Event/Job | Producer | Consumer | Purpose | Status |
| --------- | -------- | -------- | ------- | ------ |
| `payment.succeeded` | Payment verification | `xego.notifications` | Merchant notification (idempotent slot claim) | Implemented |
| `payment.succeeded` | Payment verification | `xego.compliance` | Per-payment transaction monitoring | Implemented |
| `payment.succeeded` | Payment verification | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| `payment.failed` | Payment failure | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| `settlement.batch.created` / `processed` | Settlement service | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| `payout.succeeded` / `failed` | Settlement dispatcher | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| `payment.refunded` | `RefundPayment` | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| `payment.disputed` | `CreateDispute` | `xego.merchant_webhooks` | Queue signed merchant delivery | Implemented |
| Inbound messages (2s) | Channel webhooks | `conversation.Handle` | Drive chatbot | Implemented |
| Interswitch webhooks (2s) | Interswitch outbound webhook (`TRANSACTION.COMPLETED`) | `VerifyAndApply` | Apply verification idempotently | Implemented |
| Multi-provider webhooks (2s) | All gateway webhooks | `processGatewayWebhooks` | Drain + verify per registered provider | Implemented |
| Data fulfilments (2s) | Order payments | Data fulfilment worker | Mark orders fulfilled/failed | Implemented |
| Outbound message outbox (2s) | Services | Messenger send | Deliver chat responses | Implemented |
| Merchant webhook delivery (2s) | `merchant_webhook_deliveries` | `MerchantWebhookDeliverer` | POST signed deliveries | Implemented |
| Checkout expiry (2s) | Checkouts | `expireCheckouts` | Close expired request-money links | Implemented |
| Event publisher (1s) | Outbox | `EventPublisher.Drain` | Publish to bus | Implemented |
| Payment reconcile (1m) | Payments | `payments.Reconcile` | Re-verify pending with provider | Implemented |
| Three-way reconciliation (24h) | Reconciliation | `RunReconciliation` | Auto daily run; emits `reconciliation.discrepancy` business event on discrepancies | Implemented |
| Retention purge (24h) | Retention | `PurgeExpiredData` | Archive-then-delete per policy | Implemented |
| KYC rescreen (24h) | Rescreen | `RescreenDue` | Re-run screening per period | Implemented |
| Transaction monitor (15m) | Monitor | `MonitorTransactions` | Full-scan alerting | Implemented |
| Settlement dispatcher (10s) | Queued payouts | `runSettlementDispatcher` | Dispatch payouts (min/max/daily cap controls) | Implemented |

### 19.3 Retries & dead-letter handling

- Inbound messages / webhooks: claimed with attempt counters, retried on error, completed on success. Evidence: `app/workers.go`.
- Business events: `RetryBusinessEvent` on publish failure; consumers idempotent. Evidence: `service/eventbus.go:41-47`.
- Merchant webhooks: exponential backoff (2, 4, 8, 16, 32 minutes) then dead-lettered as `failed` in `merchant_webhook_deliveries`. Evidence: README:394. Admin surface at `/admin/dead-letter` with replay buttons for both failed webhook deliveries and business events.

---

## 20. Data Architecture

### 20.1 Primary database — PostgreSQL

- **Technology:** PostgreSQL 15+ (local dev) / 17 (Docker). Evidence: README:28; `compose.yaml:3`.
- **Embedded migrations:** 48 SQL files under `internal/store/migrations`, embedded via `//go:embed` (store.go:21). The complete migration inventory:
  - Foundations: 001_init (users, merchants, conversation sessions, payments, payment events, webhook deliveries, inbound messages, message outbox, admin sessions), 002_seed_merchants, 003_user_verification, 004_bank_transfer_simulation, 005_production_customer_copy.
  - Channels/catalog: 006_telegram_channel_support, 007_catalog_picker_support (user_merchant_recents), 008_data_sell_sms (data networks/plans), 009_catalog_ux_expansion.
  - Merchant & commerce: 010_email_confirmation, 011_merchant_registrations, 012_merchant_invoices, 013_thrift_contributions, 014_receipt_scanning, 015_thrift_name_as_identifier, 016_merchant_settings, 017_merchant_password_reset_tokens, 018_pg_trgm, 019_merchant_services, 020_custom_fields.
  - Security & compliance: 021_totp, 022_rbac, 023_audit_logs, 024_encrypt_at_rest, 025_retention_archive, 026_kyc_ladder, 027_risk_band, 028_transaction_monitoring, 029_data_subject_rights.
   - Financial core: 030_ledger, 031_reconciliation, 032_chat_guard, 033_business_event_outbox, 034_consent_auto_grant, 035_checkout_token, 036_merchant_api_keys, 037_payments_api_columns, 038_merchant_webhooks, 039_checkouts, 040_ledger_merchant, 041_settlements, 042_refunds_disputes, 043_settlement_fees, 044_financial_immutability, 045_idempotency_dedup, 046_refund_maker_checker, 047_merchant_notification_prefs, 048_perf_indexes.
   - Split payments & payouts: 052_split_payments (`payment_splits` + `user_payout_destinations` tables; new ledger accounts `2300_user_payable`, `2300_xego_payable`).
- **Major domains / entities:** users (with whatsapp/telegram identity columns), merchants + owners, payments + payment_events, conversation_sessions, inbound_messages, message_outbox, merchant_webhook_deliveries, checkouts, invoices (+items/payments), thrift (groups/members/cycles/contributions/payouts), data (networks/plans/orders), settlements (accounts/batches/lines/payouts), refunds, disputes, ledger_entries, reconciliations + items, audit_logs, archive_ledger, legal_holds, kyc_profiles, customer_verifications, screening_results, risk_events, manual_review_cases, transaction_alerts, consent_records, data_subject_requests, siem_event_log, chat_guard_events, admin/merchant sessions, api_keys, payment_splits, user_payout_destinations.
- **Relationships/invariants (established):** ledger entries hash-chained + append-only; audit log hash-chained; refunds FK to payments; settlements line↔payment; payout FK to batch; immutability triggers on payments/refunds/payouts (044); idempotency unique index (045); outbox dedup index (045); refund approval columns (046); merchant notification prefs (047); performance indexes on hot paths (048).
- **Caveat:** bodies of migrations 009–029, 031–039, 041–043 were not line-read in the analysis; names are authoritative from filenames, column-level detail for those is unverified.

### 20.2 Cache

- **Redis** is optional, used only for distributed rate limiting. Empty `REDIS_URL` → per-process in-memory limiter. No other caching is established. Evidence: `.env.example:111-118`; README:140.

### 20.3 Object / file storage

- **No dedicated object/file storage** is established for documents, images, or audio. Receipts are tokenized web pages (`/receipts/{token}`); WhatsApp images are uploaded to Meta's media API on send (not stored by Xego). Evidence: `whatsapp/client.go:210-276`.
- Backup objects go to an S3-compatible bucket via rclone (deployment). Evidence: README:196-209.

### 20.4 Messaging infrastructure

- **Queues:** relational queues via outbox tables (`inbound_messages`, `message_outbox`, `business_event_outbox`, `merchant_webhook_deliveries`) — no separate queue broker required.
- **Event bus:** in-memory (default) or Kafka (`EVENT_BUS`, `KAFKA_BROKERS`). Evidence: `.env.example:120-127`.
- **Workers:** ticker-based goroutines inside the single binary (`app/workers.go`).

---

## 21. End-to-End Workflows

### Workflow: Customer onboarding (WhatsApp)

- **Trigger:** Customer sends a message to the WhatsApp number or Telegram bot.
- **Actors:** Customer, conversational engine, store.
- **Process:**
  1. Webhook received and signature-verified.
  2. Message normalized and enqueued.
  3. Drain worker invokes `conversation.Handle`.
  4. Bot collects name and email.
  5. Customer confirms the WhatsApp number / Telegram account.
- **Data created:** user row (whatsapp/telegram identity), conversation session, consent records.
- **External systems:** WhatsApp Cloud API / Telegram Bot API.
- **Events:** none financial.
- **Success path:** `onboarding_complete`; user can pay, buy data, pay invoices.
- **Failure path:** retries on message processing error; channel verification failure blocks onboarding.
- **Status:** Implemented.

### Workflow: Merchant registration & approval

- **Trigger:** User chooses **Register merchant** in chat.
- **Actors:** User, email/SMTP (or chat demo), admin.
- **Process:** 1) email OTP issued; 2) user confirms OTP; 3) user submits business name/category/description; 4) request stored as `awaiting_approval`; 5) admin approves in `/admin/merchants`; 6) merchant record created, owner linked, invoice features unlocked.
- **Data created:** merchant registration request, merchant, merchant_owners, consent records.
- **External systems:** SMTP (optional; chat demo code otherwise).
- **Success/failure:** approval unlocks merchant features; rejection/disabled blocks them. Audit-logged.
- **Status:** Implemented.

### Workflow: Conversational card payment

- **Trigger:** Customer chooses **Make payment**, selects merchant, enters amount (₦100–₦100,000), chooses **Card checkout**.
- **Actors:** Customer, engine, PaymentService, Interswitch, background workers.
- **Process:**
  1. Conversation captures merchant, amount, channel.
  2. Draft payment created (`awaiting_confirmation` → `initialized`/`pending`).
  3. Interswitch Web Checkout redirect form prepared; the platform page auto-submits to Interswitch `/collections/w/pay` (card only).
  4. Customer completes the card flow on the Interswitch hosted page.
  5. Callback `/payments/return` or the outbound webhook (`TRANSACTION.COMPLETED`) triggers `VerifyAndApply` — an authoritative Interswitch requery (`gettransaction.json`) of reference/amount/currency/test-mode.
  6. On confirmation: payment → `succeeded`; ledger money-in pair; `payment.succeeded` into outbox.
  7. Consumers: merchant notification; transaction monitoring; merchant webhook queued.
- **Data created/updated:** payment + payment_events, ledger_entries, business_event_outbox, merchant_webhook_deliveries, transaction_alerts (if rules fire).
- **External systems:** Interswitch.
- **Events:** `payment.succeeded` (to notifications/compliance/merchant_webhooks groups).
- **Success path:** chat reports success; receipt URL shows same status/provider.
- **Failure path:** verification fails → `failed` + `payment.failed`; webhook/notification retried; duplicate webhook has no double effect (idempotent).
- **Status:** Implemented.

### Workflow: Conversational bank-transfer payment

- **Trigger:** Customer chooses **Bank transfer**.
- **Actors:** Customer, engine, store.
- **Process:** 1) payment method selected; 2) review with DVA fee; 3) customer completes the payment on the Interswitch hosted checkout; 4) payment → `succeeded` only after server-side verification.
- **Data:** payment, bank_transfer_simulations (`user_confirmed`), ledger pair, events.
- **Status:** Implemented (rail simulated).

### Workflow: Invoice generation & split payment

- **Trigger:** Merchant (after approval) chooses **Generate invoice**; must use an allowed demo number.
- **Actors:** Merchant, customer, PaymentService.
- **Process:** 1) line items, quantities, unit prices, delivery fee; 2) invoice link at `/invoices/{reference}`; 3) customer sends `PAY XG-INV-...`; 4) full or partial/split amount; 5) card or bank payment; 6) invoice stays partially paid until total equals invoice total; each contribution has its own receipt.
- **Data:** invoice, invoice items, invoice payments, payments, receipts.
- **Status:** Implemented.

### Workflow: Thrift group

- **Trigger:** User completes individual profile (email OTP, legal name, DOB, address, occupation — KYC `approved_simulated`), chooses **Create thrift**.
- **Actors:** Creator, members, admin.
- **Process:** 1) name, fixed contribution, weekly/monthly, 2–12 members; 2) invite code (`JOIN XG-THRIFT-...`); 3) `ACTIVATE XG-THRIFT-...` + payout rotation order; 4) members `CONTRIBUTE XG-THRIFT-...` and pay via card/bank; 5) admin completes simulated payout; 6) next cycle until every member paid.
- **Data:** thrift_groups, thrift_members, cycles, contributions, payouts; ledger 6100_thrift_pool.
- **Status:** Implemented (payouts simulated).

### Workflow: Mobile data purchase

- **Trigger:** Chat **Buy Data** or SMS `DATA <NETWORK> <PLAN> <PHONE>`.
- **Actors:** Customer, DataService, fulfilment provider.
- **Process:** 1) select network/plan (paged/searchable in chat; SMS code); 2) beneficiary phone; 3) pay (card/bank); 4) order → `paid` → `fulfilling` → `fulfilled` (simulated or VTPass); 5) SMS reply returns request code + checkout URL.
- **Data:** data_orders, payments.
- **External systems:** VTPass (sandbox) or simulator.
- **Status:** Implemented (fulfilment simulated by default).

### Workflow: Settlement & payout

- **Trigger:** Merchant calls `POST /settlements` (or operator tooling) with a `batch_no`.
- **Actors:** Merchant (API), SettlementService, dispatcher, simulated rail, admin.
- **Process:** 1) cut: freeze succeeded un-batched payments; ledger 3100→3200; fee entry; 2) batch `open`; 3) `POST .../payout` → payout `queued`; 4) 10s dispatcher → `processing` → rail call; 5) `completed` (bank code ≠ 011) or `failed` (011 declines; retry up to 3); 6) ledger money-out on 3200; 7) `payout.succeeded/failed` → merchant webhook.
- **Failure/override:** reverse payout → reopen batch → funds back to 3100 → fresh payout.
- **Status:** Implemented (rail simulated).

### Workflow: Refund

- **Trigger:** `POST /api/v1/payments/{reference}/refund` or operator action.
- **Actors:** Merchant (API), admin, RefundService, simulated rail.
- **Process (atomic, `RefundPayment`):** lock payment → validate succeeded / no active refund / batch state → remove open-batch line + recompute totals → insert refund `pending` → payment → `refunded` (+ event) → ledger reversal → `payment.refunded` → commit → rail (simulated) completes or fails.
- **Data:** refunds, payment_events, ledger_entries (reversal), outbox.
- **Status:** Implemented (rail simulated).

### Workflow: Dispute

- **Trigger:** Provider-initiated complaint recorded by `CreateDispute`.
- **Actors:** Admin, DisputeService.
- **Process:** dispute `open` → `payment.disputed` event → merchant webhook; admin resolves `won`/`lost` (or expiry).
- **Status:** Implemented.

### Workflow: DSR erasure

- **Trigger:** Compliance creates an erasure request (`/admin/data-subjects`).
- **Actors:** Compliance, store.
- **Process:** 1) request created → 30-day cooling-off legal hold; 2) on completion: full data snapshot archived to append-only `archive_ledger`; 3) hold released; 4) user row cascade-deleted; 5) audited.
- **Status:** Implemented.

### Workflow: Three-way reconciliation

- **Trigger:** Daily ticker (`auto`) or manual `/admin/reconciliation/run` (`manual`).
- **Actors:** System, compliance officer.
- **Process:** compare succeeded payments vs ledger money-in (+ payout money-out leg); persist every discrepancy with amounts; run recorded.
- **Status:** Implemented.

### Workflow: Individual pay (customer-to-customer payout)

- **Trigger:** Customer chooses **Pay individual** from the payment menu.
- **Actors:** Customer, conversational engine, store.
- **Prerequisites:** Customer must have KYC Level 2 (identity on file) verified.
- **Process (8-step WhatsApp conversation):**
  1. Enter recipient phone number.
  2. Enter payout amount (₦100–₦100,000).
  3. Select recipient bank (browsable/searchable list of NIP-enabled banks).
  4. Enter recipient account number; system validates via bank API.
  5. Review summary (recipient name, bank, account, amount, ₦100 NIP fee, total).
  6. Confirm — draft payment created.
  7. Customer pays via bank transfer (completed on the Interswitch checkout).
  8. On confirmation: payment → `succeeded`; split posting applies (`CustomerFloat → UserPayable` + `CustomerFloat → XegoPayable`); payout record created in `user_payout_destinations`.
- **Fee model:** Collection fee deducted from sender (sender pays amount + collection fee). NIP flat fee ₦100 deducted from payout amount (recipient receives amount − ₦100). Bank transfer only (no card option for individual pay).
- **Data created/updated:** payment + payment_events, ledger_entries (split posting), business_event_outbox, user_payout_destinations, payout record.
- **Success path:** Chat reports success; receipt shows payout details.
- **Failure path:** Verification fails → `failed` + `payment.failed`; webhook retried; duplicate webhook has no double effect (idempotent).
- **Status:** Implemented. Evidence: `internal/service/conversation_individual_pay.go`.

### Account recovery

- Merchant password reset via tokens (migration 017) is established; TOTP can be reset by admins/merchants from settings pages. Evidence: README:111. No other recovery flow established.

---

## 22. Implementation Status

| Capability | Status | Evidence / Notes |
| ---------- | ------ | ---------------- |
| WhatsApp / Telegram chat channels | Implemented | `whatsapp/client.go`; `app/webhooks.go` |
| SMS data-command channel | Partially Implemented | reply-in-webhook MVP; README:268 |
| Rule-based conversational engine | Implemented | `conversation.go` + `conversation_*.go` flow files |
| Conversational AI / NLP | Not implemented | no AI code; not claimed in README |
| Card checkout (Interswitch) | Implemented | `providers/interswitch/client.go`; requery-gated; test-key only |
| Automatic provider routing | Implemented | `service/provider_router.go`; EWMA latency + health-based failover |
| Per-payment fee model | Implemented | `service/fees.go`; card/DVA/bank transfer/NIP configurable caps |
| Payment split application | Implemented | `store/store_split_payments.go`; migration 052; ledger posting at collection |
| Individual pay (customer payout) | Implemented | `service/conversation_individual_pay.go`; 8-step WhatsApp flow; bank transfer only |
| Bank-transfer path | Implemented (rail simulated) | `bank_transfer_simulations`; README:22 |
| Partner API (payments/invoices/checkouts/settlements/payouts/refunds/disputes) | Implemented | `app/api_keys.go` |
| Merchant webhooks | Implemented | `merchant_webhooks.go` |
| Invoices (split/partial) | Implemented | migration 012; README:415-420 |
| Request-money links / checkouts | Implemented | migrations 035/039; `app/checkout.go` |
| QR receipt scanning / merchant services | Implemented | migration 014/019; `app/checkout.go` |
| Thrift savings | Implemented | migrations 013/015; simulated payouts |
| Mobile data purchase | Implemented (fulfilment simulated) | migration 008; `service/data.go` |
| Double-entry hash-chained ledger | Implemented | `store_ledger.go`; migrations 030/040 |
| Ledger balance SQL | Unclear | call sites established; aggregation not line-verified |
| Settlements / batches | Implemented | `store_settlements.go`; migration 041 |
| Payouts | Implemented (rail simulated) | README:368; `app/workers.go` |
| Settlement fees | Implemented | migration 043; `SETTLEMENT_FEE_BPS` |
| Refunds | Implemented (rail simulated) | `store_refunds.go:66-181` |
| Partial refunds | Not implemented | full-only per `store_refunds.go:81` |
| Disputes | Implemented | `store_refunds.go:334-382` |
| Three-way reconciliation | Implemented | `store_reconcile.go`; `app/reconcile.go` |
| KYC ladder L0–L4 | Implemented | migration 026; README:176-186 |
| Sanctions/PEP screening | Implemented (provider simulated) | README:188 |
| NIN/BVN verification | Implemented (provider simulated) | README:190 |
| ML/FT risk scoring | Implemented | `kyc.ScoreRisk`; migration 027 |
| Transaction monitoring | Implemented | `kyc/monitor.go`; migration 028 |
| STR/CTR/PEP reports | Implemented (file exports) | `app/reports.go` |
| DSR (access/erasure/consent) | Implemented | `store_dsr.go`; migration 029 |
| Legal holds + retention | Implemented | `store_dsr.go:154-159`; README:162-172 |
| Hash-chained audit log | Implemented | migration 023; README:142-144 |
| SIEM + export | Implemented | `app/siem.go` |
| Chat guard | Implemented | `app/chatguard.go`; migration 032 |
| RBAC admin roles | Implemented | migration 022; `app/auth.go` |
| TOTP two-factor auth | Implemented | migration 021; README:109-118 |
| Encryption at rest | Implemented | migration 024; README:152-160 |
| Rate limiting | Implemented (Redis optional) | `app.go` (middleware); fails open |
| Transactional outbox + idempotency | Implemented | migrations 033/045; `service/eventbus.go` |
| Kafka event bus | Configured Only | `EVENT_BUS=memory` default; `KAFKA_BROKERS` empty |
| In-memory event bus | Implemented | default |
| Redis | Configured Only | empty `REDIS_URL` → in-memory |
| SMTP email | Configured Only (implemented feature) | empty defaults; chat demo mode |
| VTPass data fulfilment | Implemented (sandbox) | `app/webhooks.go`; `sync-vtpass-data-plans` |
| Payout/refund/data/identity/screening rails | Mocked/Simulated | deterministic simulators behind ports |
| Maker-checker approvals | Not implemented | role gates only; no two-person control |
| Regulatory certification / licensed processor | Not established / explicitly not production-ready | README:443-444 |

---

## 23. Testing & Quality

Per the repository analysis:

- **Unit tests:** provider HTTP clients against `httptest.Server` fakes (interswitch, telegram, vtpass); WhatsApp client test is pure unit; simulators (data, identity, screening) unit-tested.
- **Integration tests:** require real PostgreSQL (`TEST_DATABASE_URL`), Redis (`TEST_REDIS_URL`), Kafka (`KAFKA_BROKERS`). The store integration suite covers ledger and settlements/refunds.
- **Financial tests:** invariants verified by tests include ledger debits == credits, duplicate webhook produces no double effect, and monotonic state transitions. Each invariant was traced to production code paths in the analysis.
- **Webhook tests:** covered via provider wire-contract fakes.
- **Compliance/security/E2E tests:** no dedicated suites were established in the analysis. The README provides a **manual acceptance script** (README:396-432) covering conversational payment, invoice, thrift, data, SMS, and admin inspection — this is manual, not automated.
- **CI:** none established. `make test`/`go test ./...` and `go vet` exist; the VPS rebuild script optionally runs tests and vet (`RUN_TESTS`, `RUN_VET`). Evidence: `Makefile:7-10`; README:82-86.
- **Known testing gap (from analysis):** the refund-in-scheduled-batch and payout-reverse→rebatch paths lack an explicitly identified integration test.

---

## 24. Deployment & Infrastructure

### 24.1 Containers & services (Docker Compose)

| Service | Image/Role | Notes |
| ------- | ---------- | ----- |
| `db` | postgres:17 | SSL enabled, `wal_level=replica`, WAL archiving via `deploy/pg-archive-wal.sh`; healthcheck |
| `backup` | `deploy/Dockerfile.backup` | One-shot `pg_basebackup` + rclone ship to S3-compatible bucket; runs after `db` healthy |
| `migrate` | same app image | Runs `migrate` against the database |
| `app` | app image | `server` command, healthcheck `/demo health` |
| `caddy` | caddy:2.10-alpine | TLS termination/reverse proxy, publishes 80/443 |

Evidence: `compose.yaml` (full). Networking: single private network; only Caddy exposes public ports.

### 24.2 Native VPS deployment

- `deploy/rebuild-vps.sh` pulls code, downloads modules, optionally runs tests + vet, builds binary, installs to `/opt/whatsapp-payment/whatsapp-payment-demo`, runs migrations against `/etc/whatsapp-payment.env`, restarts the systemd service. Evidence: README:73-86.
- Oracle Free Tier guidance: reserved public IP → `*.sslip.io` hostname, Caddy auto-TLS, ports 80/443 (TCP+UDP). Evidence: README:59-71.

### 24.3 Configuration surface

`.env.example` defines: app/env/public host, logging, DB password, admin bootstrap (bcrypt hash), email/SMTP, TOTP key, data-encryption key, Interswitch Web Checkout test credentials, WhatsApp credentials/template, Telegram, SMS, data provider (simulated/VTPass), identity/screening providers, monitoring rules, payment limits, retention, rate limits, Redis, event bus/Kafka, invoice allow-list, backup/rclone. Production-refusal guards exist for demo email mode, missing TOTP key, and missing data key.

### 24.4 Monitoring

- Health endpoints `/health/live`, `/health/ready` (structured per-component status with overall `ok`/`degraded`; used by compose healthchecks). Evidence: `app.go`; `compose.yaml:72`.
- Structured JSON logging with request correlation IDs. Evidence: README:146-148.
- **Not established:** metrics/alerting infrastructure beyond admin metrics page; no observability stack (Prometheus/Grafana) in the analysis.

### 24.5 CI/CD

- GitHub Actions CI pipeline: `go test`, `go vet`, `gosec`, `govulncheck` with PostgreSQL service container. Evidence: `.github/workflows/ci.yml`.

---

## 25. Known Gaps & Technical Debt

### Product gaps

- **No partial refunds** — refunds are full-payment only (`store_refunds.go:81`).
- **SMS is an MVP** — replies ride the webhook response; no outbound SMS sender (README:268).
- **No AI/conversational intelligence** beyond rule-based flows.
- **No live payout/refund rails** — simulated rails must be replaced for live money.
- **Identity and screening are simulated** — live vendor plug-in required.
- **KYC demo path stops at L3** — no documented demo flow for L4/EDD.

### Engineering gaps (resolved)

- **Monolith files** — `store.go` split into 10 domain files (core ~320 lines); `app.go` split into 7 handler files (core ~750 lines); `conversation.go` split into 10 flow files (core ~380 lines).
- **Store interface seam** — consumer-defined narrow interfaces in `internal/service` only; App HTTP handlers keep `*store.Store`.
- **Simulator relocation** — `SimulatedPayoutProvider` and `SimulatedRefundProvider` moved from `internal/service` to `internal/providers/payout/` and `internal/providers/refund/` respectively.
- **Typed errors** — sentinel errors `ErrNothingToSettle` and `ErrPaymentInSettlementBatch` in `internal/store/errors.go`; `errors.Is` replaces `strings.Contains` in `app/settlements.go` and `app/refunds.go`.
- **CSV/JSON error handling** — all `_ = wr.Write(...)` and `_ = enc.Encode(...)` instances now check errors after flush/encode with structured logging.
- **README architecture diagram** — updated to reflect split packages and provider layer.

### Security gaps

- **Fails-open rate limiting** on Redis outage (documented as intentional — an availability trade-off, not an oversight).
- No automated security-monitoring/alerting pipeline beyond SIEM/audit; no scan evidence for security headers/CSP.

### Financial-system gaps

- **Ledger balance SQL unverified** at line level (analysis status: Unclear).
- **Single-writer assumption** on the ledger tail; multi-replica correctness without Redis/sequencing is not established.
- **Outbox ordering guarantees** not explicit; correctness relies on store-side state checks rather than event ordering.

### Compliance gaps

- **No regulatory submission pipeline** — STR/CTR/PEP are file exports only.
- **No regulatory certification claims** — README explicitly states the build is not a licensed payment processor.
- **No merchant KYB** beyond registration approval.

### Infrastructure gaps

- **No observability stack** (metrics/alerting) beyond health endpoints and structured logs.
- **Single PostgreSQL** is the system of record for queues, outbox, and accounting; Kafka is optional and off by default.

### Testing gaps

- No automated compliance/security/E2E suites established; acceptance is manual (README:396-432).
- Refund-in-scheduled-batch path has integration coverage; payout-reverse→rebatch is tested via `payout-reverse-firebatch` integration test.

---

## 26. Current Platform Capability Summary

### Fully Implemented

- WhatsApp and Telegram conversational checkout (card + bank-transfer paths)
- Hosted card checkout with verification-driven success (Interswitch Web Checkout, test-key)
- Automatic provider routing with EWMA latency and health-based failover
- Per-payment fee model (card, DVA, bank transfer) with configurable caps and breakpoints
- Payment split application at collection time (platform fee + merchant receivable + individual payout)
- Conversational individual pay (customer-to-customer payout via bank transfer, 8-step WhatsApp flow)
- Partner API: payments, invoices, checkouts, balance, settlements, payouts, refunds, disputes
- Signed, durable, retried merchant webhooks
- Invoices with split/partial payments; request-money links; QR receipt scanning
- Thrift groups; mobile data ordering
- Double-entry, append-only, hash-chained ledger; ledger reversals; chain verification
- Settlements, settlement fees, payouts, payout reversal, settlement accounts
- Full refunds with maker-checker approval flow; atomic ledger reversal; disputes with admin resolution
- Three-way reconciliation (auto daily + manual)
- KYC tier ladder; risk scoring; transaction monitoring; screening decisions
- STR/CTR/PEP CSV reports; SIEM; analytics; hash-chained audit log; chat guard
- RBAC admin console; TOTP two-factor auth; encryption at rest; rate limiting (admin 120/min); CSRF
- DSR (consent/access/erasure), legal holds, retention purge with archive
- Transactional outbox with idempotency; in-memory event bus; background workers
- Dead-letter admin surface (`/admin/dead-letter` with replay); access logging; structured readiness probe
- Production guard (blocks simulated provider in `APP_ENV=production`); GitHub Actions CI
- Merchant notification preferences (opt-in per event type); session hardening (role-change invalidation, configurable TTL)
- Email confirmation (SMTP or chat demo); VTPass sandbox data fulfilment
- Deployment: Docker Compose (PostgreSQL 17 + SSL + WAL archive + backups + Caddy TLS), native VPS rebuild script

### Partially Implemented

- SMS channel (data commands only; reply-in-webhook MVP; no outbound sender)
- Identity verification and sanctions/PEP screening (simulated providers; live vendor plug-in required)
- Email delivery (SMTP optional; chat demo mode)
- VTPass data fulfilment (sandbox only)
- Ledger balance calculation (call sites established; SQL not line-verified)

### Mocked / Simulated (by design, behind real interfaces)

- Payout rail; refund rail; data fulfilment; NIN/BVN identity; sanctions/PEP screening

### Configured Only

- Kafka event bus (off unless `KAFKA_BROKERS` set); Redis (off unless `REDIS_URL` set); SMTP (empty defaults)

### Not Implemented / Not Established

- Conversational AI / NLP
- Partial refunds; peer-to-peer transfers
- Live bank/payout/refund rails
- Regulatory submission pipeline; regulatory certification
- Observability stack
- Production readiness for live money (explicitly disclaimed in README)

---

## 27. Documentation Principles Applied

- **Terminology preserved** — statuses and component names match the repository and analysis (`IMPLEMENTED`, `PARTIALLY IMPLEMENTED`, `MOCKED/SIMULATED`, `CONFIGURED ONLY`, `NOT ESTABLISHED`).
- **Implementation status preserved** — partial features are never described as "supported"; simulated rails are always labeled simulated; configured-only infrastructure is labeled as such.
- **Uncertainty preserved** — where the analysis says "Unclear" (ledger balance SQL) or "not established" (regulatory submission), this document retains that. Resolved gaps (dead-letter UI, maker-checker) are updated to reflect current implementation.
- **Relationships explained** — components are described in terms of how they interact (payment → ledger → settlement → reconciliation; outbox → bus → consumers), not merely listed.
- **Product vs technical capabilities distinguished** — each user-visible feature names the technical components that enable it.
- **Accuracy over speculation** — whenever a claim could not be grounded, the document states that the analysis does not establish it rather than inferring.

---

## 28. Code Evidence (Representative)

```text
Path:
internal/store/store_refunds.go

Component:
Store.RefundPayment

Purpose:
Atomic full refund: lock payment, validate preconditions and batch state, remove
open-batch settlement line, transition payment to refunded, post ledger reversal,
emit payment.refunded — all in one transaction (store_refunds.go:66-181).
```

```text
Path:
internal/store/store_ledger.go

Component:
Store.appendLedgerEntryTx / postLedgerPair / ledgerHash

Purpose:
Append-only double-entry journal: balanced debit/credit pairs, SHA-256 hash chain,
serialized tail writes, mutation rejection (store_ledger.go:55-124; migrations 030/040).
```

```text
Path:
internal/providers/interswitch/client.go

Component:
Client.Initialize / Verify / ValidateWebhook

Purpose:
Real Interswitch Web Checkout integration: hosted form redirect checkout,
authoritative server-side requery verification (gettransaction.json),
redirect-notification handling (internal/providers/interswitch/client.go).
```

```text
Path:
internal/service/eventbus.go

Component:
EventPublisher.Drain

Purpose:
Transactional outbox → bus publisher: claim 50, publish, retry on failure, complete
(service/eventbus.go:34-50).
```

```text
Path:
internal/kyc/monitor.go

Component:
RunTransactionMonitor

Purpose:
Behavioural transaction monitoring: velocity, structuring, round amounts,
high-risk counterparty rules with severity-to-risk-score mapping
(kyc/monitor.go:82-197).
```

```text
Path:
internal/app/app.go + admin.go + auth.go + checkout.go + cli.go + merchant.go + webhooks.go + workers.go

Component:
App.routes / runWorkers / startEventConsumers

Purpose:
Full HTTP surface (Partner API, admin/merchant consoles, webhooks), ticker-driven
background workers, and event consumer wiring.
```

---

## 29. Final Standard

This document is the internal technical/product specification for **Xego as it exists in the repository today**. It is grounded in the completed repository analysis; every capability described is traceable to code, migrations, configuration, or explicit README statements. Where the analysis could not establish a capability or detail, that is stated rather than assumed. It is not a roadmap and does not describe future Xego.