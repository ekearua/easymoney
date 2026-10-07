# Xego Go-Live Runbook

Checklist and commands for taking a Xego deployment from demo/staging to
live-money operation. It complements `README.md` (deployment topology, provider
setup) and `XEGO_PLATFORM_DOCUMENTATION.md` (feature/technical reference); this
file is the operations gate itself.

The build is **not** a licensed payment processor; complete the obligations in
README → *Production limitations* before live-money use.

---

## 1. Pre-flight decisions

| Decision | Default | Notes |
| --- | --- | --- |
| Primary payment provider | `PAYMENT_PROVIDER=interswitch` | `simulated` is always rejected in `APP_ENV=production`. |
| Collection mode | `INTERSWITCH_CHECKOUT_MODE=LIVE` | Required in production; `TEST` is rejected by the rail guard. |
| Settlement/refund rails | `PAYOUT_PROVIDER`, `REFUND_PROVIDER` | Live → `interswitch`; see §3.5. |
| Data fulfilment | `DATA_PROVIDER` | `vtpass` (live) or a screened provider; not `simulated`. |
| Identity | `IDENTITY_PROVIDER`, `SCREENING_PROVIDER` | Live providers; see §3.6. |
| AI | `AI_PROVIDER=openai` with `AI_MAX_REQUESTS_PER_MINUTE > 0` | `simulated` AI is rejected in production when `AI_ENABLED=true`. |
| SMS | `SMS_PROVIDER=http|webhook` with a secret | Notifications only; not rail-guarded. |

---

## 2. Production guard reconciliation (`ALLOW_SIMULATED_RAILS_IN_PROD`)

`ALLOW_SIMULATED_RAILS_IN_PROD` is the only escape hatch that runs simulated
rails inside `APP_ENV=production`. It must **not** be present (or must be
`false`) in a live deployment.

- Default when unset/`false`: `internal/config/config.go` → `validateProductionRails`
  rejects simulated `PAYOUT_PROVIDER`, `REFUND_PROVIDER`, `DATA_PROVIDER`,
  `IDENTITY_PROVIDER`, `SCREENING_PROVIDER`, and `BANK_TRANSFER_MODE=simulate`,
  and requires `INTERSWITCH_CHECKOUT_MODE=LIVE` plus (for interswitch payout/
  refund) `INTERSWITCH_TRANSFER_BASE_URL` and `INTERSWITCH_SOURCE_ACCOUNT`.
- Scope: it covers only the secondary rails. `PAYMENT_PROVIDER=simulated` and
  `AI_PROVIDER=simulated` are **never** overridable in production.
- The `ALLOW_SIMULATED_RAILS_IN_PROD=true` line in `.env.example` documents the
  staging/demo template; a live `.env` must remove it.
- Verification (must be clean before go-live):

  ```bash
  grep -n ALLOW_SIMULATED_RAILS_IN_PROD .env   # expect: no output
  APP_ENV=production go test ./internal/config/ -run Production -count=1
  ```

---

## 3. Provisioning checklist

### 3.1 Interswitch LIVE checkout
- [ ] LIVE credentials: `INTERSWITCH_CLIENT_ID`, `INTERSWITCH_CLIENT_SECRET`,
      `INTERSWITCH_MERCHANT_CODE`, `INTERSWITCH_PAY_ITEM_ID`.
- [ ] `INTERSWITCH_CHECKOUT_MODE=LIVE`.
- [ ] `INTERSWITCH_BASE_URL` = the provisioned live host (webpay surface), not
      `sandbox.interswitchng.com`.
- [ ] `INTERSWITCH_WEBHOOK_SECRET` (required in production) and the webhook URL
      registered at Interswitch pointing at `{BASE_URL}/webhooks/interswitch`.
- [ ] `INTERSWITCH_SOURCE_ACCOUNT` funding account (also used by VTU).

### 3.2 Transfer rails (payouts, refunds, name enquiry)
- [ ] `INTERSWITCH_TRANSFER_BASE_URL` = provisioned live Quickteller v5 host.
- [ ] QA-provisioned credential pair that the v5 `passport/oauth/token` host
      accepts. Interswitch's public sandbox answers OAuth `401 Bad credentials`
      for the v5 rails even when the same pair works for web-checkout/requery
      (see §5 smoke results); a live transfer rail therefore requires QA
      provisioning and, if the token host differs, `INTERSWITCH_TOKEN_URL`.
- [ ] `INTERSWITCH_SOURCE_ACCOUNT`, `INTERSWITCH_TERMINAL_ID`,
      `INTERSWITCH_INITIATING_ENTITY_CODE`, sender identity vars.

### 3.3 Channels
- [ ] WhatsApp: `WHATSAPP_VERIFY_TOKEN`, `WHATSAPP_APP_SECRET`,
      `WHATSAPP_ACCESS_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID`, `WHATSAPP_GRAPH_VERSION`
      (all required in production), callback URL registered at Meta, approved
      message templates + link policy.
- [ ] Telegram: `TELEGRAM_ENABLED`, `TELEGRAM_BOT_TOKEN`, webhook URL registered
      via `setWebhook`.
- [ ] Instagram: `INSTAGRAM_ENABLED`, app secret/access token, page, graph version.
- [ ] TikTok: `TIKTOK_ENABLED`, app secret/access token, `TIKTOK_API_BASE`.

### 3.4 Operations secrets
- [ ] `DATA_ENCRYPTION_KEY` (required in production) — generate with
      `go run ./cmd/demo random-data-key`; keep in the vault.
- [ ] `TOTP_ENCRYPTION_KEY` (required because `TOTP_ENABLED=true` in production)
      — generate with `go run ./cmd/demo random-totp-key`.
- [ ] `ADMIN_PASSWORD_HASH`, `DATABASE_URL` (`sslmode=require`), `POSTGRES_PASSWORD`.
- [ ] `EMAIL_CONFIRMATION_ENABLED=true` + SMTP creds for merchant registration.

### 3.5 Provider rails
- [ ] `PAYOUT_PROVIDER=interswitch`, `REFUND_PROVIDER=interswitch`.
- [ ] `DATA_PROVIDER` (e.g. `vtpass`) + `VTPASS_API_KEY`/`PUBLIC_KEY`/`SECRET_KEY`.
- [ ] `IDENTITY_PROVIDER`, `SCREENING_PROVIDER` live values + keys.

### 3.6 AI & SMS
- [ ] `AI_ENABLED=true`, `AI_PROVIDER=openai`, `AI_MAX_REQUESTS_PER_MINUTE > 0`,
      `OPENAI_API_KEY`.
- [ ] `SMS_ENABLED=true`, `SMS_PROVIDER=http|webhook`, `SMS_API_BASE`,
      `SMS_WEBHOOK_SECRET`.
- [ ] `BASE_URL` is a public HTTPS URL.

### 3.7 Infrastructure resilience (pre-deploy prerequisites)

These four are the gap between "backups configured" and "recoverable". Each
has a file that implements it and a dashboard row that proves it keeps
working; all three must be green on `/admin/metrics` before go-live.

- [ ] **WAL-shipping split.** The daily base backup alone cannot honor
      `BACKUP_RPO=1h` — WAL only leaves the box when `deploy/backup.sh` runs.
      Install the 15-minute WAL timer so offsite WAL freshness is independent
      of the daily run:

      ```bash
      sudo cp deploy/systemd/xego-backup*.service deploy/systemd/xego-backup*.timer /etc/systemd/system/
      sudo systemctl daemon-reload
      sudo systemctl enable --now xego-backup.timer xego-backup-wal-ship.timer
      systemctl list-timers xego-backup*
      ```

- [ ] **Weekly PITR restore drill.** `deploy/backup.sh --restore-drill`
      replays archived WAL onto the newest base backup and verifies the
      `payments` relation — the only proof the backup actually restores.
      Schedule it and keep the first run's elapsed time against
      `BACKUP_RTO` (the journal line `restore drill: PITR succeeded` is the
      RTO measurement):

      ```bash
      sudo cp deploy/systemd/xego-restore-drill.* /etc/systemd/system/
      sudo systemctl enable --now xego-restore-drill.timer
      ```

- [ ] **Backup-health alerts visible and green.** Confirm `/admin/metrics`
      shows the *Backup & PITR health* panel with fresh ages for base, WAL
      ship and drill (thresholds `BACKUP_BASE_STALE_AFTER`,
      `WAL_SHIP_STALE_AFTER`, `RESTORE_DRILL_STALE_AFTER` in `.env`). A
      panel that reads `never` means the scheduler never recorded a
      heartbeat — fix before launch, not after.

- [ ] **Dual-Caddy ingress.** Run the edge stack on the second app server so
      no single host owns the public endpoint:

      ```bash
      # server 1 (full stack):  docker compose up -d --build
      # server 2 (edge):        docker compose -f compose.edge.yaml up -d --build
      ```

      Attach a **floating IP** to whichever host passes health checks
      (recommended — instant failover, one certificate), or publish both
      hosts behind **health-checked DNS failover** if the provider has no
      floating IPs. `deploy/Caddyfile` is identical on both hosts and
      actively probes `/health/ready`, so a replica that lost its database
      is ejected instead of receiving traffic. Verify: stop the app on the
      active host and confirm the other answers within the health interval.

- [ ] **Async streaming standby** for PostgreSQL (see §4.1): initialise on
      the second server, confirm `status` reports streaming, and rehearse
      the promote procedure once in staging.

---

## 4. Deployment & boot verification

Docker topology (`compose.yaml`): Postgres 17 (SSL, WAL archiving) → `migrate`
one-shot → `app` (`server` on `:8080`) → Caddy (TLS). Native VPS alternative:
`bash deploy/rebuild-vps.sh` against `/etc/whatsapp-payment.env`.

```bash
docker compose up -d --build            # migrate runs first, app waits on it
docker compose ps                       # app healthy from /demo health
curl -fsS https://$PUBLIC_HOST/health
```

Migrations are embedded (`internal/store/migrations/*.sql`) and applied in
filename order automatically on boot and via `make migrate`. Back up the
database before every upgrade (`make backup`-equivalent or read the backups
section in README).

### 4.1 Database standby: setup & manual promote

The primary's PostgreSQL is the single largest availability gap in the
Phase-1 topology (§3.7). An async streaming standby on the second app server
cuts failover from a ~15-minute PITR restore to a ~1-minute promote.

**Setup (standby host):**

```bash
PRIMARY_HOST=<primary-ip> PGPASSWORD=<replication-role> \
  bash deploy/setup-standby.sh init
# start the cluster on STANDBY_DATA_DIR (hot_standby=on is the PG default)
bash deploy/setup-standby.sh status   # expect: standby (streaming), lag bytes
```

**Manual promote (primary is down):**

```bash
bash deploy/setup-standby.sh promote   # pg_ctl promote -w -t 60
```

Then, in order:

1. **Repoint the writers.** Update `DATABASE_URL` (or PgBouncer's
   `DATABASE_URL` if it fronts the standby) to the promoted host and
   `docker compose up -d` the app. The app has no built-in DNS failover —
   the repoint is the failover.
2. **Verify reads/writes.** `curl -fsS https://$PUBLIC_HOST/health/ready`
   must report `ready: true`; run `make reconcile` to confirm the ledger
   still balances across the cutover.
3. **Re-seed the old primary** (when it returns) as a standby of the *new*
   primary: `pg_rewind` if timelines allow, otherwise a fresh
   `setup-standby.sh init` after wiping its data dir. Until then it must
   NOT be restarted as a writer — split-brain writes are unrecoverable
   without the WAL you are about to recopy.
4. **Record the incident** and re-run §4.1's status check on the rebuilt
   standby before closing it.

Promotion is deliberately manual at Phase 1: automatic failover (Patroni /
pg_auto_failover) adds a failure mode the current team must operate at 3am.
Revisit at Phase 2 when the topology grows a dedicated DB server.

### 4.2 Dual-Caddy ingress verification

Run after §3.7's dual-Caddy setup. The failover claim is only real once
both directions have been observed:

1. **Both edges serve.** `curl -fsSI https://$PUBLIC_HOST/health/ready`
   twice with the floating IP attached to each host (or DNS resolving to
   each host); both must answer 200 — the certificate is renewed
   independently per host, so a first-visit 403 from Let's Encrypt means
   HTTP-01 challenges are being blocked on that host (check ports 80/443).
2. **Health checks eject the broken side.** `docker compose stop app` on
   the *active* host, then confirm within `health_interval` (10s) +
   `fail_duration` (30s) that traffic moves: the Caddyfile probes
   `/health/ready`, which returns 503 the moment the database ping fails,
   and the passive checker marks the upstream down after `max_fails`.
   With a floating IP, move it manually (or via the provider's health
   check) and re-test; with DNS failover, confirm the record flips within
   its TTL.
3. **Database loss is visible, not silent.** Stop PostgreSQL on the
   primary and confirm both of these: the app answered directly
   (`curl -fsS localhost:8080/health/ready`) reports
   `503 {"ready":false,"status":{"database":"<error>"}}`, and the edge
   through Caddy no longer serves 200 (the probe marked the upstream down,
   so requests get Caddy's own 503 rather than a stale 200). The edge must
   not pretend to be healthy while writes fail.
4. **Restore and re-verify** (app back up → 200 on both edges) before
   closing the check.

---

## 5. Sandbox smoke tests (pre-go-live, against TEST credentials)

All smokes are read-only or use a generated, never-funded reference, and are
gated on `INTERSWITCH_SANDBOX_SMOKE=1` so a plain `go test ./...` never touches
the network. Run them from a checkout with the sandbox `.env` loaded:

```bash
source .env 2>/dev/null                  # export INTERSWITCH_* sandbox values
export INTERSWITCH_SANDBOX_SMOKE=1 INTERSWITCH_CHECKOUT_MODE=TEST

# Web Checkout renders a live payment page (no Z4/Z5 rejection).
go test ./internal/providers/interswitch/ -run TestSandboxCheckoutPageSmoke -v

# Server-side requery (the call that confirms payments) accepts the
# InterswitchAuth signature — a broken signature here means nothing confirms.
go test ./internal/providers/interswitch/ -run TestSandboxRequerySmoke -v
```

Recorded results (2026-09-14, merchant `MX286980`, `sandbox.interswitchng.com`):

| Smoke | Result | Notes |
| --- | --- | --- |
| `TestSandboxCheckoutPageSmoke` | PASS | Rendered checkout page (8686 bytes), no responseCode. |
| `TestSandboxRequerySmoke` | PASS | Missing-ref requery answered `status "pending"`; HMAC signature accepted. |
| `TestSandboxTransferRailSmoke` | BLOCKED | v5 OAuth on the public sandbox returns `401 Bad credentials` even with a working web-checkout pair; needs QA-provisioned creds + `INTERSWITCH_TOKEN_URL`. |
| `TestSandboxNameEnquirySmoke` | SKIP | Needs `INTERSWITCH_ENQUIRY_ACCOUNT`/`INTERSWITCH_ENQUIRY_BANK` for a real probe. |

The transfer-rail block is a sandbox-side provisioning gap, **not** a code
defect. It must be closed before settlement payouts/refunds go live.

---

## 6. PostgreSQL integration tests (local)

DB-backed tests skip unless `TEST_DATABASE_URL` is set, so a plain
`go test ./...` is always offline-safe. Suites must be run **per package**:
each package derives or truncates its own database, and the URL/keyword format
each expects differs. Do not combine packages in one `go test` invocation —
suites race on the shared Postgres (truncations wipe another package's data and
wallet balances) and the DSN formats are mutually incompatible.

| Package | Env form | Notes |
| --- | --- | --- |
| `./internal/store/` | URL: `postgres://user:pass@localhost:5432/db?sslmode=disable` | Suites truncate shared tables; run alone. |
| `./internal/service/` | Keyword: `host=localhost port=5432 user=... password=... sslmode=disable` | Suite derives `<user>_service` by appending a `database=` keyword; a URL/query-string env breaks its DSN parse. |
| `./internal/app/` | URL (same as store) | Bulk-payment tests derive `<db>_bulk_app`, dropped/recreated each run. |

```powershell
# store (URL form, truncates the shared DB):
$env:TEST_DATABASE_URL='postgres://postgres:pass@localhost:5432/bulk_e2e?sslmode=disable'
go test ./internal/store/ -count=1

# service (keyword form; no URL, no database=):
$env:TEST_DATABASE_URL='host=localhost port=5432 user=postgres password=pass sslmode=disable'
go test ./internal/service/ -count=1

# app (URL form; bulk tests self-isolate into <db>_bulk_app):
$env:TEST_DATABASE_URL='postgres://postgres:pass@localhost:5432/bulk_e2e?sslmode=disable'
go test ./internal/app/ -count=1
```

### Bulk location payments coverage

- `internal/service/bulk_payments_integration_test.go` —
  `TestBulkPaymentsEndToEnd` drives a real batch through both stages: recipient
  snapshot, affordability block, settlement-cut funding, stage-1 location
  round-trip, geo scoring, Tier-2 KYC gate, debit-per-success payout, and the
  idempotent payout replay. Uses its own `<db>_bulk_service` database (accepts
  either env form above).
- `internal/app/bulk_location_test.go` — public `/bulk/loc` page and capture
  endpoint validation (`TestBulkLocationCaptureValidatesPayload`,
  `TestBulkLocationPageGrantsGeolocation`, `TestBulkLocationCaptureEndToEnd`).
- `internal/app/bulk_merchant_handlers_test.go` — CSRF rejection, coordinate
  validation, and per-merchant scoping of centers/recipients/batches
  (`TestMerchantBulkMutationsRejectBadCSRF`,
  `TestMerchantBulkCenterCreateValidatesCoordinates`,
  `TestMerchantBulkHandlersScopeToMerchant`).
- `internal/app/bulk_worker_test.go` — `runBulkJobs` idle/unknown-kind guards
  and a real two-stage dispatch (`TestBulkWorkerSkipsWhenIdle`,
  `TestBulkWorkerRunsBothStages`).

Bulk tests seed batches through `repository.CreateBulkBatch` (not the service)
so they never depend on a funded wallet, and run against dedicated per-package
databases so earlier runs or other suites cannot leak state into assertions.

---

## 7. First live payment (smoke by hand)

1. Confirm guards are armed (§2) and the binary starts in
   `APP_ENV=production` with a clean `go vet`/`go test ./...`.
2. Start a tiny real checkout (e.g. `PAYMENT_PROVIDER=interswitch`,
   `INTERSWITCH_CHECKOUT_MODE=LIVE`) and complete an N↔NGN card payment on the
   live host.
3. Watch the confirmation pipeline: pay-redirect webhook →
   `gettransaction.json` requery → ledger double-entry →
   merchant webhook/POST. Use `go run ./cmd/demo monitor` and `make reconcile`.
4. Run `make reconcile` and confirm the payment matches with no exception row.

## 8. Post-launch operations

- Daily: `make reconcile`, review exception rows, run `make retain` for the
  configured retention policy, check the backup job and WAL archiving.
- Weekly/drift: review `go run ./cmd/demo monitor`, admin audit log, rate-limit
  hits, and `go run ./cmd/demo reports` for payment/settlement totals vs the
  gateway ledger.
- Change/incident: any rail, AI, or channel-config change re-runs §2, §5, §7.

## 9. Rollback

- Config rollback: restore the previous `.env` and restart the app container
  (`docker compose up -d`); migrations already applied are not rolled back.
- Code rollback: re-deploy the previous image/tag; the new code's migration
  files are only applied if not yet applied.
- Data: point-in-time recovery from the PostgreSQL WAL/backups (see backups
  section in README).