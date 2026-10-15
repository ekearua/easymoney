# Xego Action Checklist (your manual tasks)

Everything here requires a human (secrets in dashboards, infra changes) or is
an explicit decision. Code items are tracked separately in
`docs/payments/Remediation_Backlog.md` and `reports/Payment_System_Implementation_Plan.md`.

## Tier 0 — done, with follow-ups

- [ ] **Rotate secrets** — the deleted files (`Whatsapp Business Token.txt`,
      `whatsapp hash.txt`, `db_password.txt`) were on GitHub history
      (`1a36801`, `d43874a`). Rotate ALL of:
  - [ ] Meta WhatsApp system-user access token + app secret
  - [ ] Paystack secret key (`sk_test_...`)
  - [ ] PostgreSQL password (`POSTGRES_PASSWORD` in `.env` + live DB)
  - [ ] VTPass API/secret keys, `VTPASS_WEBHOOK_SECRET`
  - [ ] Telegram bot token + webhook secret, SMS webhook secret
  - [ ] `WHATSAPP_VERIFY_TOKEN`
- [x] Secret files deleted from disk, `.gitignore` covers them
- [x] History rewrite skipped (your choice) — rotate is now the only remedy

## Tier 0 — deploy/infra follow-ups

- [ ] **VTPass dashboard**: remove `?secret=` from the configured callback URL
      (`https://<host>/webhooks/vtpass` — header only now)
- [ ] **Docker deploy**: `docker compose up -d db` with the new image
      (`postgres:17`, self-signed cert auto-generated on first boot);
      confirm `migrate` + `app` connect with `sslmode=require`
- [ ] **Native VPS** (`deploy/rebuild-vps.sh` host): update
      `/etc/whatsapp-payment.env` `DATABASE_URL` to add `sslmode=require`
      (or `verify-full` with a real CA) once that host's Postgres has TLS
- [ ] **SMTP**: confirm provider supports STARTTLS (Gmail/Postmark/Mailgun do);
      plaintext delivery is now refused
- [ ] **Verify email OTP flow**: existing DB rows still hold old SHA-256 code
      hashes; they expire within `EMAIL_VERIFICATION_TTL` (10m) — test a fresh
      RESEND after the new build is live
- [ ] **Commit decision**: Tier 0 + C1-C5/C26/C30 changes are uncommitted
      (VTPass header, bcrypt OTP, cooldown, prod guard, compose TLS, SMTP
      STARTTLS, TOTP MFA, RBAC, rate limiting, audit log, logging + redaction,
      tests)

## Tier 1 — what's next (code, in suggested order)

**C1 MFA (TOTP) + C2 RBAC + C3/C25 rate limiting + C4/C26 audit log — implemented, pending deployment checks:**

- [x] C1  MFA (TOTP) — admin + merchant login (2-step, enrollment w/ QR,
      encrypted-at-rest secrets, disable/reset buttons; migration 021)
- [x] C2  RBAC — admin_users table + roles (admin/compliance/support/readonly),
      role middleware on all admin routes, /admin/admins management UI,
      per-admin sessions + TOTP; bootstrap from ADMIN_EMAIL/hash (migration 022)
- [x] C3/C25 rate limiting — fixed-window per-IP limits on webhooks
      (120/min), public pages (60/min), scanner API (30/min); Redis-backed
      with in-memory fallback + fail-open (internal/ratelimit)
- [x] C4/C26 tamper-evident audit log — audit_logs table, SHA-256 hash chain,
      actor (admin/merchant/system) + IP columns, append-only trigger,
      /admin/audit page with chain verification (admin+compliance), audit
      writes on all privileged handlers (migration 023)
- [x] C5/C30 structured logging + redaction — JSON/text logger (LOG_FORMAT)
      with per-request correlation IDs (internal/logging, request middleware
      on every route); provider error strings redacted at every persistence
      boundary (webhook_deliveries, message_outbox, inbound_messages,
      sms_requests, data_orders) via internal/redact (secrets/PAN/phone/email/
      URL-credential masking + truncation); all request-scoped slog calls now
      context-aware
- [ ] C5/C30 deploy check: set `LOG_FORMAT=json` + `LOG_LEVEL` in env; route
      stdout to log rotation with ~1 year retention; confirm each log record
      has request_id and no PII/tokens in error fields
- [ ] C4/C26 deploy check: `migrate` creates audit_logs + trigger; verify
      entries at /admin/audit after each privileged action; retention covered
      by C8/C29 purge work
- [ ] C1/C2 deploy checks: set `TOTP_ENABLED=true` +
      `TOTP_ENCRYPTION_KEY` (generate via `go run ./cmd/demo random-totp-key`,
      64 hex chars = 32 bytes) in `.env` and `deploy/rebuild-vps.sh` env;
      run `migrate` (creates admin_users + bootstrap admin); add operators at
      `/admin/admins`; each admin enrolls TOTP on first login
- [ ] C3/C25 deploy check: set `REDIS_URL` in `.env`/VPS env when running
      multiple replicas (single-instance works without it)
- [ ] C5/C30 deploy check: set `LOG_FORMAT=json` + `LOG_LEVEL` in env; route
      stdout to log rotation with ~1 year retention; confirm each log record
      has request_id and no PII/tokens in error fields
- [x] C7  encryption at rest — AES-256-GCM envelope (internal/crypto) under
      DATA_ENCRYPTION_KEY (64 hex = 32 bytes; `go run ./cmd/demo random-data-key`);
      chat payloads (inbound_messages, message_outbox) + admin/merchant session
      CSRF tokens sealed at the store boundary (migration 024 converts payload
      jsonb->text); EncryptLegacyAtRest re-seals legacy plaintext rows in batches
      on `migrate` + server start; login/payment flows unchanged; searchable PII
      (users.email etc.) stays plaintext -> cover with volume encryption (C7b)
- [ ] C7b deploy check: generate + set `DATA_ENCRYPTION_KEY` (64 hex chars) in
      `.env`/`deploy/rebuild-vps.sh` env; run `migrate` once (converts columns +
      re-encrypts legacy rows); confirm DB rows start with `enc:v1:`; add
      PostgreSQL volume-level encryption at rest + encrypted backups for the
      remaining plaintext PII columns
- [x] C8/C29 batched purge + legal holds + archive-to-ledger — `PurgeBefore`
      rewritten (store.go) to run bounded keyset batches (LIMIT 500) across
      sessions/tokens, chat/webhook/sms rows, payments, invoices, thrift and
      audit; financial records snapshotted to append-only `archive_ledger`
      (hash-chained, mutation-rejected) before hard delete; audit chain
      re-rooted after purge; `legal_holds` table (user/payment/invoice/
      thrift_group, optional expiry) freezes subjects from retention;
      `/admin/legal-holds` and `/admin/archive` pages; migration 025; docs in
      README
- [ ] C8/C29 deploy check: run `migrate` (migration 025), set
      `RETENTION_PERIOD` (default 90d) as needed, confirm `retain` worker
      batches without table locks and archive_ledger grows; add a legal hold at
      `/admin/legal-holds` and verify the held subject survives the next purge
- [x] C24 migration runner advisory lock — `Migrate` (store.go) now acquires a
      session-level `pg_advisory_lock` on a dedicated connection and runs all
      pending embedded migrations on it, so concurrent replicas / overlapping
      `migrate` invocations serialize instead of racing
- [x] C23 backups/WAL/PITR — `deploy/backup.sh` pg_basebackup + WAL archive to
      S3 via rclone (base/WAL), `deploy/pg-archive-wal.sh` archive_command,
      `deploy/RESTORE.md` runbook, compose `db` (archive_mode) + `backup`
      service; added `BACKUP_RTO` config (compose + .env.example) and full
      `--restore-drill` PITR mode (WAL replay + payments row-count verify)
- [x] C28 VTPass webhook secret — validated from `X-VTPass-Webhook-Secret`
      header only (app.go, `receiveVTPassWebhook`); never accepted via a
      `?secret=` URL query param, so callback URLs carry no secret and are safe
      to log; extracted `vtpassWebhookSecretValid` helper + locked by
      `TestVTPassWebhookSecretUsesHeaderOnly` (ISO 8.24)
- [x] C19 HTTP server hardening — `newHTTPServer` (app.go) sets ReadHeaderTimeout
      5s, ReadTimeout 15s, WriteTimeout 45s, IdleTimeout 60s, MaxHeaderBytes 1MB,
      shutdown 15s; `/admin/chat-guard` attempt log; locked by TestServerHardening
      (ISO 8.6)

## Tier 2 / 3 — later phases

- [x] C15 STR/CTR/PEP report generators — `internal/reports` (pure CSV/JSON
      encoders), `Store.STRReport/CTRReport/PEPReport` (store_reports.go),
      `App.GenerateReports` + CLI `reports` command writing `reports/exports/`,
      and `/admin/reports` page + CSV downloads for admin/compliance roles
- [x] C9 customer-identity ladder tables — `kyc_profiles`, `customer_verifications`,
      `screening_results`, `risk_events`, `authenticators`, `trusted_devices`,
      `manual_review_cases` (migration 026)
- [x] C10 L0-L4 KYC tier ladder — `internal/kyc` band logic + `Store.AdvanceKYCTier`/
      `DowngradeKYCTier` (store.go), simulated onboarding promotion to L3 on verified
      payment success
- [x] C11 NIN/BVN identity verification — `internal/providers/identity` simulator +
      `ports.IdentityVerifier` (ports.go), identity-gated L3 promotion
- [x] C12 sanctions/PEP screening — `internal/providers/screening` simulator +
      `ports.SanctionsScreener`, screening enforced on the ladder (flagged/unverified on
      match, `screening_results` persisted)
- [x] C13 ML/FT customer risk scoring — `risk_score`/`risk_band` on `kyc_profiles`
      (migration 027), recomputed by `internal/kyc.ScoreRisk` via `RecomputeRiskScore`
- [x] C14 transaction monitoring — `transaction_alerts` (migration 028) + `manual_review_cases`
      `monitoring` case type; `internal/kyc.RunTransactionMonitor` raising rule+severity
      alerts that flow into the compliance queue
- [x] C16 append-only ledger — `ledger_entries` (migration 030) double-entry journal,
      hash-chained + append-only trigger; money-in on payment success (Paystack +
      bank transfer), allocations on invoice / thrift contribution / data-order
      fulfilment, thrift payout disbursement; `PostLedgerReversal` offsetting
      entries linked by `reversal_of`; `/admin/ledger` page with balances + chain
      verification + reversal form (store_ledger.go, app/ledger.go)
- [x] C17 three-way reconciliation — `reconciliations` + `reconciliation_items`
      (migration 031) snapshot runs comparing internal payments vs C16 ledger
      money-in postings vs the simulated bank rail; `RunReconciliation`/
      `ListReconciliationRuns`/`ReconciliationItemsByRun` (store_reconcile.go);
      `/admin/reconciliation` page + manual run button, 24h auto ticker, and
      CLI `reconcile3` command (store_reconcile.go, app/reconcile.go)
- [x] C18 chat content guard — `internal/chatguard` Luhn-verified card detection
      + keyword-scoped PIN/CVV/OTP detection with credential redaction
      (chatguard.go); guard hooked into `ConversationService.Handle` refusing
      blocked messages + `/admin/chat-guard` blocked-attempt log
      (`chat_guard_events`, migration 032; store_chatguard.go, app/chatguard.go);
      `TestChatGuardEventPersistence` + chatguard unit tests
- [x] C20 data-subject rights — `consent_records` + `data_subject_requests`
      (migration 029), `ExportUserData` JSON snapshot, 30-day erasure
      cooling-off legal hold with archive-then-delete, `/admin/data-subjects`
      queue + export + resolve handlers (store_dsr.go, app/dsr.go)
- [x] Phase 3 service extraction + Kafka — transactional business-event outbox
      (`business_event_outbox`, migration 033; consent auto-grant trigger,
      migration 034); Kafka-compatible event-bus port with in-memory
      (`internal/bus/memory`) and real Kafka (`internal/bus/kafka`,
      segmentio/kafka-go) transports selected by `EVENT_BUS`; `EventPublisher`
      drains outboxed payment facts onto the bus; merchant-notification and
      per-payment compliance-monitoring consumers replace the inline calls;
      `merchant_notified_at` idempotency claim; tests in store + service

_Last updated: 2026-08-13_
