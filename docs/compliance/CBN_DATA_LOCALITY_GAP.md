# CBN data locality / host-locally obligation

**Source:** CBN Circular PSS/DIR/PUB/CIR/001/004 (15 June 2026), signed by the
Director of Payments System Supervision.

**Operative clause (verbatim):**

> "All Financial Institutions and participants facilitating payments within
> Nigeria shall ensure that payment transaction data generated within Nigeria
> are stored and managed in Nigeria in accordance with data protection laws
> and regulations applicable in Nigeria."

**Deadline:** 1 January 2027.

**Why this exists:** the CBN's stated rationale is supervisory access during
fraud and financial-crime inquiries (no need to go through foreign legal
processes), latency and retrieval cost, keeping infrastructure spend in naira
rather than foreign currency, and broader data-sovereignty posture.

**In scope:** deposit money banks, microfinance banks, payment service banks,
mobile money operators, payment terminal service providers, switching /
processing companies, super agents, and "all other licensed payment operators."
A Faya merchant wallet + disbursement stack sits inside that perimeter.

---

## Gap entry

| Field | Value |
| --- | --- |
| Gap id | **G16** |
| Title | **Host Nigerian payment transaction data locally** |
| Source | CBN Circular PSS/DIR/PUB/CIR/001/004 (15 June 2026) |
| Obligation | All payment transaction data generated within Nigeria must be stored and managed in Nigeria, in line with the NDPA 2023. |
| Scope of "payment transaction data" | Ledger/primary stores, application and server logs, in-flight queues, read replicas and backups, fraud/analytics stores, and disaster-recovery copies — i.e. the whole data lifecycle, not only the primary database. |
| Deadline | 1 January 2027 |
| Current status | **Partial — depends on infra decision.** The codebase is already written to keep its operational state inside its own Postgres + local app servers; the open questions are where backups, WAL archive and DR copies actually land, and whether any queue/analytics/log path currently points offshore. |
| Evidence check that closes it | A current data-flow map that names every in-scope store and where each one lives today; confirmation that backups / WAL archive / DR copies stay in-country (or a documented decision that a local zone / local rack in a Nigerian Tier III data centre satisfies the requirement, with counsel sign-off on the unsettled interpretation); NDPA alignment note; and a breach-signal rule for any new offshore dependency for in-scope data. |

This obligation intersects the existing **AML 5-year retention floor (G6)**.
Retention is about *how long* records are kept; data locality is about *where*
they are kept. Both can be true at once, and the same `deploy/backup.sh` +
`deploy/pg-archive-wal.sh` + rclone story is the relevant control for the
backup/replica/DR slice of G16.

---

## One-page host-locally compliance note

### 1. The obligation (plain English)

Every piece of payment transaction data that originates in Nigeria must be
stored and managed inside Nigeria, and it must be managed in a way that also
satisfies the Nigeria Data Protection Act 2023. This is not only the primary
database. It is the full data lifecycle: the ledger, the app and server logs,
any in-flight message queue, read replicas and backups, fraud/analytics stores,
and disaster-recovery copies.

### 2. What "payment transaction data" means for Faya, concretely

The in-scope stores and streams are the ones that carry Nigerian payment facts:
wallet/ledger state, payment and settlement/payout records, standing-credit and
bulk-disbursement state, refunds, disputes, the audit log, the retention archive
ledger, conversation/session state tied to a payment, and the operational
telemetry that describes those payments (alerts, screening decisions, reminder
outcomes, synthetic-check results).

Things that are *not* obviously Nigerian payment transaction data — and which
should still be confirmed — include outbound provider webhook logs that are only
confirmation metadata, AI prompt/response text that does not embed payment facts,
and global aggregated analytics that has been fully de-identified and does not
reconstruct a Nigerian payment event.

### 3. Where the data lives today, as described in the current deploy story

The current Faya deploy posture is already locally oriented in the operational
sense:

- **Primary database:** PostgreSQL 17 on the app host (Docker Compose) or on a
  native VPS (`deploy/rebuild-vps.sh`). The app host is the writer and owns the
  schema.
- **WAL archiving:** `deploy/pg-archive-wal.sh` is installed as PostgreSQL's
  `archive_command`, writing WAL segments to a local archive directory
  (`$BACKUP_DIR/wal`).
- **Base backups + offsite shipping:** `deploy/backup.sh` runs `pg_basebackup`
  locally, then ships base + WAL to an S3-compatible bucket via rclone
  (`BACKUP_BUCKET`, `BACKUP_PREFIX`, `RCLONE_REMOTE`).
- **Offsite destination:** the runbook and README describe an S3-compatible
  bucket drawn from OCI Object Storage, AWS S3, or MinIO. **This is the key
  locality question.** If that bucket is in a foreign region, the backup/WAL
  copy is foreign-hosted payment transaction data.
- **Disaster recovery:** Phase-1 DR is point-in-time recovery from the local
  WAL archive + offsite base/WAL backups, plus a manual-promote async streaming
  standby on the second app server (`deploy/setup-standby.sh`). The standby is
  another local app host, so the hot standby is in-country by construction;
  PITR depends on where the backups/WAL actually landed.
- **Release-time backup gate:** `deploy/release.sh` takes a full base backup
  before migrate on every release, and rolls back to the previous image if the
  new revision never becomes ready.
- **Backup health visibility:** there is a Backup & PITR health panel on
  `/admin/metrics` with fresh/stale/never states for base, WAL ship and drill,
  using `BACKUP_BASE_STALE_AFTER`, `WAL_SHIP_STALE_AFTER` and
  `RESTORE_DRILL_STALE_AFTER`.

So the operational architecture is already *hosted locally first* and then
*synced offsite*. Data locality turns on the offsite destination.

### 4. What still needs to be decided or documented before 1 January 2027

1. **Where the S3-compatible bucket lives.** If it is a foreign region, that is
   the offshore dependency. The fix is either a Nigerian-region bucket (if the
   provider offers one and the CBN accepts it) or an in-country object store /
   colocation target. If a hyperscaler local zone / local rack in a Nigerian
   Tier III data centre is the chosen path, that interpretation should be
   documented as a decision, not left implicit.
2. **WAL archive residence.** `pg-archive-wal.sh` writes locally first, and
   `backup.sh --wal-ship` syncs that local archive offsite. The local archive is
   already in-country; the sync target is the same locality question as the
   backup bucket.
3. **DR copy residence.** Any DR copy that is not the local streaming standby
   (for example a cold DR region, or a restore into a non-Nigerian environment
   for testing) must also be assessed. The manual-promote standby is
   in-country by design, which is a good start.
4. **Logs/queues/analytics.** Confirm whether any log aggregation, queue, or
   analytics store currently ships Nigerian payment data offshore. The current
   codebase does not appear to depend on an external queue for the payment path,
   which is favourable, but the claim should be checked against whatever
   observability or logging target is actually used.
5. **NDPA alignment.** The circular explicitly ties local management to
   Nigerian data-protection law. The same retention, access-control, vendor-DPA,
   and encryption posture that satisfies NDPA is part of "managed in Nigeria in
   accordance with data protection laws." This note should cross-reference the
   NDPA work rather than duplicate it.

### 5. Open interpretation question

The circular has not yet clarified, at least in the secondary sources read for
this note, whether an international cloud provider operating a local availability
zone or local rack inside a Nigerian Tier III data centre counts as compliant
infrastructure. That distinction materially affects cost and migration effort. It
should be treated as **pending interpretation**, and the platform's decision
(counsel opinion, provider confirmation, or both) should be recorded explicitly
rather than assumed.

### 6. Proof package an auditor or the CBN would expect

- A current data-flow map for in-scope payment data, naming each store/stream
  and its residence.
- A statement on backup/WAL/DR destination and why it is, or is not, Nigerian-
  hosted.
- Backup & PITR health evidence that the pipeline is alive (the admin panel
  green states are the least-bad first proof).
- The weekly PITR restore-drill record, which proves the backups actually
  restore the payments relation.
- The NDPA alignment note and any vendor DPAs covering in-country processors.
- A change-control note: adding a new offshore dependency for in-scope data is a
  compliance-relevant change.

### 7. Breach signal

Any new offshore dependency for Nigerian payment transaction data — a foreign
backup bucket, an offshore log/queue sink, a DR copy that lives abroad, or a new
analytics store that reconstructs payment facts from abroad — is a direct breach
signal against this obligation. The safest default is to treat any new external
storage target for in-scope data as locality-sensitive by default, and to route
the decision through compliance before it lands in the deploy story.

---

## Mapping against the current deploy / runbook posture

This is a factual check between the circular's requirement and what the current
code/deploy story actually says, not a promise that everything is done.

| In-scope data category | Where it lives today (per current docs) | Locality verdict today | What must be true by 1 Jan 2027 |
| --- | --- | --- | --- |
| Primary Postgres (ledger, payments, wallets, KYC, audit, etc.) | App host, local Postgres 17 (Compose or native VPS) | **Compliant by construction** — the writer host is the Nigerian app server | Keep it there; do not introduce an offshore primary or primary replica |,
| WAL archive (local) | Local `$BACKUP_DIR/wal` via `deploy/pg-archive-wal.sh` | **Compliant by construction** — local directory | Keep archive_mode + archive_command configured; ensure the local archive is not silently shipped elsewhere |,
| WAL ship target (offsite) | S3-compatible bucket via rclone (`BACKUP_BUCKET`) | **Locality depends on bucket region** — the docs say OCI Object Storage / AWS S3 / MinIO, but do not pin a Nigerian region | Bucket must be in-country, or the interpretation decision must be documented with counsel/provider sign-off |,
| Base backups (offsite) | Same S3-compatible bucket, same rclone target | **Same locality question as WAL ship** | Same as above |,
| Disaster recovery (cold) | PITR from local WAL + offsite base/WAL backups | **Depends on backup/WAL destination**; the local streaming standby is in-country | DR copies must not quietly live abroad; if cold DR is used, its residence must be named |,
| Hot standby / failover | Async streaming standby on the second app server (`deploy/setup-standby.sh`) | **Compliant by construction** — second local app host | Keep the standby in-country; document manual-promote as the Phase-1 failover |,
| Release-time base backup | `deploy/release.sh` runs `compose run --rm backup` before migrate | **Same locality question as backups** | Same as backup bucket |,
| Application/server logs | Not pinned in the current deploy story to a named external sink in the docs read for this note | **Unverified — confirm** | If logs containing payment facts are shipped anywhere, confirm that destination is in-country or out of scope |,
| In-flight queues | Not present as a dependency in the current payment path docs; Kafka is explicitly deferred to a later phase, not day 1 | **Favourable, but confirm** | Deferring Kafka is fine; if any queue is added for payment data, it inherits this obligation |,
| Fraud/analytics/risk stores | In-store risk events and alerts; no named offshore analytics sink in the docs read for this note | **Favourable, but confirm** | Any analytics store that reconstructs Nigerian payment facts must be assessed |,
| Retention archive ledger | Local Postgres `archive_ledger` | **Compliant by construction** | Keep it local; the archive is append-only and hash-chained, which is good for both retention and tamper evidence |,
| Admin/metrics data | Local Postgres (`backup_runs`, `audit_chain_checks`, etc.) | **Compliant by construction** | Keep local |,
| Provider webhook logs / confirmation metadata | Stored locally where recorded; the circular's coverage of "logs" should be read against what is actually logged | **Confirm scope** | If logs contain payment facts, treat them as in-scope |,
| Vendor/provider-hosted data | Identity, screening, data-fulfilment, payout/refund providers hold their own data; this obligation is about *platform* storage and management of Nigerian payment transaction data | **Out of platform control, but in scope for vendor review** | Vendor DPAs and provider locality should be confirmed as part of vendor onboarding |,

**The single most important open item** is the offsite backup/WAL destination.
Everything else in the current posture is either already local-first by design, or
needs a confirmation that no hidden offshore sink exists. The circular does not
say "no copies may ever leave Nigeria"; it says payment transaction data must be
*stored and managed in Nigeria*. That is the phrase to design and document against.

---

## Notes for the next pass

- This note should be reconciled with the existing **AML retention floor (G6)**
  so locality and retention are not described as independent silos when they
  share the same backup/restoration story.
- The NDPA 2023 references in the spec should be pointed at this note where the
  circular itself says "in accordance with data protection laws and regulations
  applicable in Nigeria."
- If there is a real infrastructure decision here — Nigerian bucket vs local
  colocation vs a hyperscaler local zone/rack — that decision belongs in the
  deploy/runbook posture document and should be cited from this gap entry.
- The unsettled local-zone interpretation is not something to paper over. The
  honest place for it is a documented decision with a named owner and a date,
  not a footnote that quietly assumes "cloud is cloud."
