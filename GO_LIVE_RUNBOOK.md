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

## 2. Production guard (hard boot gate — there is no override)

There is no environment variable that relaxes production validation. Two
layers run inside `internal/config.Load` before the app starts serving, and
a failing check aborts startup with the offending key in the log:

1. **Provider allowlists (every environment).** Simulated money/identity
   rails no longer exist in the loader: `PAYOUT_PROVIDER`,
   `REFUND_PROVIDER` and `BANK_TRANSFER_MODE` must be `interswitch`;
   `AI_PROVIDER` must be `openai`; `DATA_PROVIDER` must be `vtpass` or
   `interswitch`; `IDENTITY_PROVIDER` must be `ninbvnportal`;
   `SCREENING_PROVIDER` must be `http`. An unknown or `simulated` value
   fails `Load()` in development too. (`PAYMENT_SCREENING_PROVIDER` and
   `LIVENESS_PROVIDER` are the inverse — `simulated` only, until their
   live adapters ship.)
2. **`APP_ENV=production` requirements** (`config.go` production block plus
   `validateProductionRails`): required secrets (`DATABASE_URL`,
   `ADMIN_PASSWORD_HASH`, `DATA_ENCRYPTION_KEY`, the `WHATSAPP_*` set,
   `TOTP_ENCRYPTION_KEY` because TOTP defaults on), full interswitch
   checkout credentials with `INTERSWITCH_CHECKOUT_MODE=LIVE`, live
   transfer vars (`INTERSWITCH_TRANSFER_BASE_URL`,
   `INTERSWITCH_SOURCE_ACCOUNT` — payout/refund default to interswitch),
   `VTPASS_*` keys (data defaults to vtpass), `AI_MAX_REQUESTS_PER_MINUTE
   > 0` when AI is enabled, `SMS_WEBHOOK_SECRET` when SMS is enabled,
   per-channel secrets for any enabled Telegram/Instagram/TikTok channel,
   a public HTTPS `BASE_URL`, API bases for any `*_PROVIDER=http` adapter,
   and a ban on the demo shortcuts (`EMAIL_DEMO_CODE_IN_CHAT`,
   `LINK_DEMO_CODE_IN_CHAT`).

`ALLOW_SIMULATED_RAILS_IN_PROD` is **not a variable** — the loader never
read it, `.env.example` must not mention it (`TestExampleEnvTemplateLoads`
fails if it reappears), and an old `.env` carrying it is ignored rather
than honored. Finding it anywhere means stale config from a retired
template; delete the line instead of trusting it.

- Verification (must be clean before go-live):

  ```bash
  grep -n ALLOW_SIMULATED_RAILS_IN_PROD .env .env.example  # expect: no output
  APP_ENV=production go test ./internal/config/ -run Production -count=1
  # definitive check: boot with the live .env — a missing/invalid key exits
  # before serving, naming the offender (migrate runs first in compose):
  #   docker compose up -d && docker compose logs migrate app
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
      `AI_API_KEY`.
- [ ] `SMS_ENABLED=true`, `SMS_PROVIDER=http|webhook`, `SMS_API_BASE`,
      `SMS_WEBHOOK_SECRET`.
- [ ] `BASE_URL` is a public HTTPS URL.

### 3.7 Infrastructure resilience (pre-deploy prerequisites)

These four are the gap between "backups configured" and "recoverable". Each
has a file that implements it and a dashboard row that proves it keeps
working; all three must be green on `/admin/metrics` before go-live.

Backups run either as compose services (Docker topology) or as systemd
timers (native VPS). The compose stack ships a long-running
`backup-scheduler` service that drives the WAL ship, daily base backup and
weekly drill with no host cron — that is the recommended path. The systemd
units below are the native-VPS equivalent and run as the `postgres` user.

**Host install (systemd path).** The units call
`/opt/whatsapp-payment/deploy/*.sh`, so install the tree and the environment
file before copying any unit:

```bash
sudo mkdir -p /opt/whatsapp-payment
sudo cp -r deploy /opt/whatsapp-payment/
sudo chmod +x /opt/whatsapp-payment/deploy/*.sh
sudo install -m 600 /dev/null /etc/whatsapp-payment.env   # then populate
```

Populate `/etc/whatsapp-payment.env` (root-owned, 0600) with the host-side
values — deliberately not part of `.env.example`:

```sh
PGHOST=localhost
PGPORT=5432
PGUSER=whatsapp_payment
PGPASSWORD=...
PGDATABASE=whatsapp_payment
BACKUP_BUCKET=my-bucket
BACKUP_PREFIX=whatsapp-payment
BACKUP_DIR=/var/lib/xego-backup
RCLONE_REMOTE=xegobackup
RCLONE_CONFIG=/etc/xego-backup/rclone.conf
BACKUP_RETENTION_DAYS=30
BACKUP_RPO=1h
BACKUP_RTO=15m
```

Object-storage credentials live in `rclone.conf`. Copy the template before
starting anything that mounts it — a missing bind source makes Docker create
a *directory* and the scheduler fails at `up`:

```bash
cp deploy/rclone.conf.example deploy/rclone.conf   # then fill in
```

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

      On server 2, set `PRIMARY_DB_HOST` in `.env` to server 1's
      private-network IP before starting the edge stack. Server 1 publishes
      PostgreSQL on 5432 (`PRIMARY_DB_PORT` overrides it in `compose.yaml`)
      so the edge app can reach it cross-host; PostgreSQL terminates TLS
      with its self-signed certificate, so the edge connects with
      `sslmode=require` (encrypted, unverified). **Firewall rule
      (mandatory): allow 5432/tcp from server 2's IP only**, and nothing
      else. Do not expose it to the open internet, and prefer a private
      network (VPC peering / WireGuard) when the provider offers one. The
      edge replica connects directly to PostgreSQL rather than through the
      primary's PgBouncer — fine for one replica (30 pool connections
      against the default `max_connections=100`), but revisit if the edge
      grows.

- [ ] **Async streaming standby** for PostgreSQL (see §4.1): initialise on
      the second server, confirm `status` reports streaming, and rehearse
      the promote procedure once in staging.

---

## 4. Deployment & boot verification

Docker topology (`compose.yaml`): Postgres 17 (SSL, WAL archiving) → `migrate`
one-shot → `app` (`server` on `:8080`) → Caddy (TLS). Native VPS alternative:
`bash deploy/rebuild-vps.sh` against `/etc/whatsapp-payment.env`.

**First install / local build** — no registry involved, compose builds
`xego-app:local`:

```bash
docker compose up -d --build            # migrate runs first, app waits on it
docker compose ps                       # app healthy from /demo health
curl -fsS https://$PUBLIC_HOST/health/ready
```

**Releases** run the published image instead of building on the host — see
§4.3.

Migrations are embedded (`internal/store/migrations/*.sql`) and applied in
filename order automatically on boot and via `make migrate`. `deploy/release.sh`
takes a full base backup *before* it applies them: the schema is forward-only, so
that backup is the only way back (§9).

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
   primary and confirm both of these: the app knows it is unready —
   `docker compose exec app /demo health` exits non-zero with the
   database ping error (the same probe the container healthcheck runs;
   the app image is distroless and publishes no port to the host, so the
   check runs inside the compose network. On the native VPS topology the
   app binds the host, and `curl -sS localhost:8080/health/ready` prints
   `503 {"ready":false,"status":{"database":"<error>"}}` — note: no `-f`,
   which would swallow the 503 body this step exists to observe) — and
   the edge through Caddy no longer serves 200 (the probe marked the
   upstream down, so requests get Caddy's own 503 rather than a stale
   200). The edge must not pretend to be healthy while writes fail.
4. **Restore and re-verify** (app back up → 200 on both edges) before
   closing the check.

### 4.3 Releases: tag → image → rollout

A release is a version tag. `.github/workflows/release.yml` runs on `v*`:

1. **verify** — the release policy gate. Every rule is enforced in the workflow,
   not merely documented, and a tag that fails any of them never produces an
   image:
   - the tag must point at a commit that is an ancestor of `main`, so releases
     are cut from main and never from a branch tip;
   - that commit must already have a green `ci.yml` run from its push to main
     (the job waits for the run, then refuses on
     `failure`/`cancelled`/`timed_out`/`startup_failure` rather than proceeding
     while CI is red);
   - `go vet` and the non-gated test suite pass on the tagged commit (the
     Postgres-backed suite is `ci.yml`'s job, on every push);
   - `deploy/release_test.sh` passes — the harness that drives the real release
     script against a recording docker shim (§6).
2. **build** — builds `Dockerfile`, pushes to GHCR as
   `ghcr.io/ekearua/easymoney:<tag>`, alongside a `major.minor` alias, the long
   commit SHA, an SBOM and a build-provenance attestation, then **signs the
   image by digest with keyless cosign** — the signature is bound to this
   workflow's OIDC identity, so no long-lived signing key exists to leak.
3. **deploy** — *opt-in*. Runs `deploy/release.sh <tag>` on every host in
   `DEPLOY_HOSTS` over the `production` Environment (add required reviewers to
   turn the rollout into an approval). Disabled until `DEPLOY_ENABLED=true`, so
   pushing a tag only publishes.

**Verifying a published image.** The signature is keyless, so it is verified
against the workflow identity that produced it rather than a public key. Use
the digest, not the tag — a tag can be repointed, a digest cannot:

```bash
IMAGE=ghcr.io/ekearua/easymoney
TAG=v1.4.0
DIGEST=$(docker buildx imagetools inspect "$IMAGE:$TAG" --format '{{.Manifest.Digest}}')
cosign verify \
  --certificate-identity-regexp '^https://github.com/ekearua/easymoney/.github/workflows/release.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$IMAGE@$DIGEST"
```

Run that before pinning `APP_IMAGE` by hand, and after any manual rollout.

Repository configuration the deploy job expects:

| Kind | Name | Purpose |
| --- | --- | --- |
| variable | `DEPLOY_ENABLED` | `true` to let a tag deploy; unset = publish only |
| variable | `DEPLOY_HOSTS` | space-separated hosts, e.g. `10.0.0.11 10.0.0.12` |
| variable | `DEPLOY_USER` | ssh user (default `deploy`) |
| variable | `DEPLOY_PATH` | repo checkout on the host (default `/opt/whatsapp-payment`) |
| secret | `DEPLOY_SSH_KEY` | private key authorized on those hosts |
| secret | `DEPLOY_KNOWN_HOSTS` | pinned `known_hosts` entries for those hosts |

**Manual rollout / rerun** — identical to what the workflow does:

```bash
cd /opt/whatsapp-payment
git fetch --tags --force --prune origin
git checkout --detach refs/tags/v1.4.0
bash deploy/release.sh v1.4.0
```

The host's `.env` carries `APP_IMAGE` (the tag to run); compose `app` and
`migrate` use `${APP_IMAGE}` when set and fall back to the local build when it
is not. `APP_IMAGE_TOKEN` (a GHCR PAT with `read:packages`) is only needed while
the package is private.

What `deploy/release.sh` enforces, in order:

1. pull the image — a failure here changes nothing on the host;
2. full base backup **before** the `migrate` one-shot;
3. `migrate`;
4. recreate the `app` replicas from the new image and wait until every replica
   reports healthy;
5. recreate `caddy` so its `app:8080` upstream is re-resolved to the new replica
   IPs (the `caddy_data` volume keeps the certificates);
6. probe `/health/ready` through ingress — `PUBLIC_HEALTH_URL`, or derived from
   `PUBLIC_HOST` as `https://$PUBLIC_HOST/health/ready`;
7. run the post-deploy **synthetic checks** — `demo synthetic pipeline` in a
   throwaway container from the same image, which asserts the deployed revision
   can reach the database, is running against the schema it expects, resolves
   today's newest payment by *reference and receipt token*, and is actually
   draining the outbox (`SYNTHETIC_OUTBOX_MAX_AGE`, default 30m). Skipped with
   `RELEASE_SKIP_SYNTHETIC=1`;
8. **on any failure in 4–7, restore the previously running image automatically**
   and exit non-zero, so the last good revision keeps serving.

Run the checks by hand at any time:

```bash
docker compose run --rm --no-deps app synthetic pipeline   # exit code is the result
```

Every stage is recorded in `synthetic_checks` (one row per stage per run) and
surfaced on `/admin/metrics` as the last run plus a 7-day pass rate. The stages
are **read-only** over the payments domain by design: a check that fabricated a
payment in a live ledger would write fake money into an audited store, which is
an AML problem rather than a test. The write-side equivalent — a payment in the
rail's own sandbox — is a separate capability, not something a post-deploy smoke
test should do silently. To make a run gate a rollout by hand, re-run
`deploy/release.sh <tag>`; it rolls the image back when the checks fail.

The last-good release is recorded in `deploy/.release-state` (git-ignored), and
`release.sh` reads the image actually running before it trusts any file.

Edge host: the same script against the edge stack —

```bash
COMPOSE_FILES="-f compose.edge.yaml" bash deploy/release.sh v1.4.0
```

It detects services, so there it pulls and recreates `app` and `caddy` only;
there is no `migrate` or `backup` service on the edge by design (§4.1, §4.2).

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

### Release path harness (no Docker required)

`deploy/release_test.sh` tests the CD script itself. It puts a recording `docker`
shim first on `PATH` and runs the real `deploy/release.sh` against a throwaway
project root, so no daemon, registry or database is needed:

```bash
bash deploy/release_test.sh
```

The shim reports container health *per image*, which makes "the new image never
becomes ready" a first-class case rather than a comment. Six cases are covered:
the happy path (including the pull → base-backup → migrate → app → caddy
ordering), the readiness-gate rollback, a migrate failure, a pull failure, a
backup failure, and the edge stack (no `migrate`/`backup` services). It runs in
`ci.yml` on every push and in the release workflow's `verify` job.

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

- **Code — automatic.** `deploy/release.sh` restores the previous image itself
  when the new revision never becomes ready (§4.3), so a bad release fails the
  pipeline and leaves the last good revision serving. By hand:
  `APP_IMAGE=<last-good-tag> docker compose up -d --no-build --no-deps app caddy`
  — or re-run `bash deploy/release.sh <last-good-tag>`.
- **Code — by tag.** Re-deploy the previous tag; `deploy/.release-state` records
  the image that was serving before the current one, so the target is never
  guessed.
- **Schema — not reversed.** Migrations are forward-only. A code rollback does
  **not** undo the new revision's migrations (each file is applied once). A
  release whose migration was destructive is recovered with point-in-time
  recovery from the WAL/base backups (§3.7, backups section in README) — which
  is why `release.sh` takes a base backup before every migrate.
- **Config.** Restore the previous `.env` and `docker compose up -d`; images are
  unchanged, so only the app restarts.
- **Data.** Point-in-time recovery from the PostgreSQL WAL/backups.
