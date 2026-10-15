---
title: XEGO CODEBASE COMPLIANCE REMEDIATION BACKLOG
subtitle: Mapped to ISO/IEC 27001:2022, CBN AML/CFT, NDPR 2019 and PCI DSS
author: Xego Compliance Program
date: July 2026
---

# XEGO CODEBASE COMPLIANCE REMEDIATION BACKLOG

| | |
|---|---|
| **Document Owner** | CISO / Engineering |
| **Approved By** | CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | On material change or annually |
| **Regulatory Basis** | ISO/IEC 27001:2022; CBN AML/CFT Regulation 2009; NDPR 2019; PCI DSS |

---

## 1. Purpose

Prioritized engineering backlog of every confirmed codebase gap against the
Xego compliance program. Each item maps the current state (with file:line
evidence) to a target state, the regulatory basis, severity, roadmap phase and
owning department. This is a working engineering artifact; it sits alongside
the Codebase Changes sheet in the compliance workbook, which this document
cross-references (items C1-C30 appear in both).

## 2. Legend

| Severity | Meaning |
|---|---|
| P0 | Blocking for production money movement or certification; remediate now |
| P1 | Required before go-live / within current phase |
| P2 | Planned hardening; no immediate blocker |

| Phase | Timing |
|---|---|
| Phase 1 | Months 0-3 (foundation: access, audit, crypto, secrets, retention) |
| Phase 2 | Months 3-6 (KYC/AML ladder, monitoring, reporting, data rights) |
| Phase 3 | Months 6-12 (ledger, reconciliation, hardening) |

## 3. Backlog (severity-sorted, by domain)

### 3.1 KYC / AML / CFT

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| ~~C9~~ | KYC approval is hard-coded — **closed**: the Tier-1 profile is a recorded customer declaration (`customer_declaration` evidence; migration 091 drops the `kyc_status` column and rewrites the `approved_simulated` evidence rows), and the verification, ladder, screening, risk and review tables the fix column asks for all exist (C10–C14) | `approved_simulated` on insert/update (store.go:2012,2018); schema default (013_thrift_contributions.sql:11) | Add customer_verifications, kyc_profiles, screening_results, risk_events, authenticators, trusted_devices, manual_review_cases tables | CBN CIP/CDD | P0 | 1 | Engineering |
| C10 | No L0-L4 KYC tier ladder | Binary gate only (conversation.go:708,3674); verification tops at email_confirmed (store.go:597-764) | L0-L4 ladder with controlled transitions, approval workflow and audit | CBN CIP/CDD | P0 | 2 | Engineering |
| C11 | No NIN/BVN identity verification | None; all self-declared, no provider port (ports.go:47-118) | Integrate Smile ID / Youverify / Dojah / Prembly with liveness + document checks | CBN CIP verification | P0 | 2 | Engineering |
| C12 | No sanctions/PEP screening | Zero screening code anywhere | Screen at onboarding + periodic rescreen; decision states clear/possible/strong/blocked/manually_cleared | CBN OFAC | P0 | 2 | Compliance |
| C13 | No ML/FT customer risk scoring | No scoring model | Low/medium/high scoring driving CDD vs EDD and monitoring | CBN risk-based approach | P0 | 2 | Compliance |
| C14 | No transaction monitoring engine | Only flat min/max bounds (config.go:120-121) | Velocity, structuring, round amounts, high-risk counterparties; alert + case management | CBN suspicious activity monitoring | P0 | 2 | Compliance |
| C15 | No STR/CTR/PEP report generators | No reporting code | Extract regulator-format reports from audited payment/ledger data | MLPA 2004 reporting | P1 | 2 | Compliance |
### 3.2 Access control & authentication

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C1 | No MFA for admin/merchant | bcrypt + session cookie only; single admin account (config.go:84-85) | TOTP/passkey required on login; authenticator secrets encrypted | ISO 8.5 | P0 | 1 | Security |
| C2 | No RBAC | requireAdmin/requireMerchant middleware only (app.go:1631-1645,1662-1678) | Role model + permission checks; API-key scoping per merchant | ISO 8.2, 8.3, 5.15 | P0 | 1 | Security |
| C3 | Rate limiting only on 2 login endpoints | loginLimiter in-memory (app.go:1894-1931) on /admin/login, /merchant/login | Rate limit webhooks, /payments/return, /receipts, /invoices, /scan, /api/readers/scan | ISO 8.20 | P0 | 1 | Security |
| C21 | OTP codes weak hash + resend bypass | Unsalted SHA-256 (conversation.go:3978); resend resets attempt counter (conversation.go:622,755); demo in-chat path (config.go:87) | bcrypt/argon2 with salt; resend cooldown; disable in-chat delivery in production | PCI 8.3, 8.1.6; ISO 8.5 | P0 | 1 | Security |
| C25 | Rate limiting not distributed | In-memory map resets on restart; no shared store across replicas | Redis-backed limiter shared across replicas | ISO 8.5; PCI 8.1.6 | P1 | 1 | Engineering |

### 3.3 Audit & logging

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C4 | No admin-action audit trail | No audit_logs table; privileged actions unlogged (app.go:819-980,1042-1144) | audit_logs table + hash-chaining + middleware on privileged actions | ISO 8.15; PCI 10 | P0 | 1 | Security |
| C26 | Event logs lack actor/IP and tamper-evidence | payment_events plain editable rows, source literals only (store.go:3570-3806) | Actor + IP columns; hash chain/signature; append-only enforcement | PCI 10.3, 10.5; ISO 8.15 | P0 | 1 | Security |
| C5 | No structured logging / correlation IDs | ~26 sparse slog calls; no correlation IDs; PII risk | JSON schema, correlation IDs, PII redaction, 1-year retention | ISO 8.15-8.16 | P0 | 1 | Engineering |
| C30 | Provider error strings stored verbatim | Error text saved to webhook_deliveries.error_message / last_error (store.go:3965,3989) | Redact/allowlist error fields; log masking policy | ISO 8.16 | P1 | 1 | Engineering |

### 3.4 Data protection & encryption

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C7 | No encryption at rest | PII plaintext: users (001:5-7), profiles (013:7-10), invoices (012:27-28), chat payloads (001:80-81), csrf tokens (001:108); pgcrypto unused (001:1) | Column/volume encryption with KMS-managed keys | ISO 8.24; NDPR | P0 | 1 | Security |
| C22 | No TLS to DB; SMTP not enforced | sslmode=disable (compose.yaml:23,35); smtp.PlainAuth (smtp.go:36) | sslmode=require/verify-full; STARTTLS with tls.Config | ISO 8.24; PCI 4.1 | P0 | 1 | Security |
| C20 | No data-subject rights (access/erasure) | No export/erasure endpoints; no consent records | /me data export; erasure flow with 30-day legal hold; consent timestamps | NDPR 3.1; ISO 8.10 | P1 | 2 | Compliance |
| C18 | No automated chat content guard | Help text only (conversation.go:3557) | Block/sanitize card/PIN/OTP data in chat; log attempts | PCI; CBN consumer protection | P1 | 2 | Engineering |

### 3.5 Retention, backups & database ops

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C8 | Purge worker unbatched, narrow coverage | One transaction over 10 full-table deletes (store.go:4158-4200); daily ticker (app.go:275) | Batched deletes with LIMIT/keyset; extend to profiles/invoices/thrift; purge audit trail | ISO 8.10; NDPR | P1 | 1 | Engineering |
| C29 | 90-day hard-delete conflicts with record keeping | payments deleted at cutoff (store.go:4189); no legal hold | Archive to append-only ledger; legal-hold flag and list; align with MLPA retention | ISO 8.10; MLPA | P1 | 1 | Compliance |
| C23 | No backups / WAL / PITR | No pg_dump/WAL/restore in code or compose; RTO/RPO not in config | Base backup + WAL archive to S3/GCS; restore drills; RTO/RPO configuration | ISO 8.13, 5.30 | P1 | 1 | Ops |
| C24 | Migration runner order risk | Lexical sort (store.go:549); no advisory lock; no down-migrations | Numeric zero-padded sort; advisory lock around apply; down/rollback support | ISO 8.32 | P1 | 1 | Engineering |

### 3.6 Secrets & hardening

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C6 | Secrets in repo/env | Plaintext secret files on disk; secrets committed in git history | Docker secrets/KMS; rotation; no secrets in repo or env | ISO 8.24 | P0 | 1 | Security |
| C27 | Secrets recoverable from git history | Secret files were committed in early commits | Rotate all keys/tokens; rewrite history (filter-repo); verify no residual blobs | ISO 8.24 | P0 | 1 | Security |
| C28 | Webhook secret passed in URL | VTPass secret as ?secret= query param (app.go:617) | Move secret to header; avoid logging URLs | ISO 8.24 | P1 | 1 | Security |
| C19 | HTTP server hardening | Partial timeouts | ReadHeaderTimeout 5s, Read 15s, Write 45s, Idle 60s, MaxHeaderBytes 1MB, shutdown 15s | ISO 8.6 | P1 | 3 | Engineering |

### 3.7 Ledger & reconciliation

| ID | Gap | Current State (file:line) | Recommended Fix | Basis | Sev | Phase | Owner |
|---|---|---|---|---|---|---|---|
| C16 | No append-only ledger | Payments state machine only; no journal | Double-entry append-only journal; reversals via offsetting entries | CBN record-keeping | P0 | 3 | Engineering |
| C17 | Three-way reconciliation partial | Paystack reconcile command only (cmd/demo reconcile) | Internal vs ledger vs bank automated daily + weekly manual | CBN paper trail; PCI | P0 | 3 | Finance |

## 4. Confirmed-Good (no remediation required)

The following controls were verified in code and do not need new backlog items:

- **SQL injection**: all queries use pgx parameterized statements; only safe allowlisted dynamic fragments (store.go:2524-2553, 3129-3146).
- **Password hashing**: bcrypt DefaultCost for admin and merchant passwords (app.go:827,943,1612).
- **Session/token hashing**: session tokens, reset tokens and reader API keys stored as SHA-256 of high-entropy random values (store.go:4108-4129, 4309-4333).
- **Webhook signature verification**: Paystack/SMS/VTPass secrets validated before processing (app.go:455,495,549,615-634).
- **Edge TLS**: Caddy terminates HTTPS with HSTS (deploy/Caddyfile).
- **Bounded workers**: all claim loops use FOR UPDATE SKIP LOCKED with LIMIT (store.go:3506-3547, 3938-3976, 3175-3221).
- **Template safety**: html/template auto-escaping with a maskPII template function (app.go:1875-1887).

## 5. Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval; sourced from codebase audit July 2026 |
