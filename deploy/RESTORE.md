# Xego Backup & Point-in-Time Recovery Runbook

Scope: base backup + WAL archiving to an S3-compatible bucket (OCI Object
Storage, AWS S3, MinIO) with rclone. Targets: RPO < 1h (`BACKUP_RPO`),
RTO < 15 min for a single-region restore. Default retention 30 days
(`BACKUP_RETENTION_DAYS`).

## Architecture

```text
PostgreSQL --archive_mode=on--> BACKUP_DIR/wal (local disk, pg-archive-wal.sh)
        |
        +-- deploy/backup.sh (scheduled) --pg_basebackup--> BACKUP_DIR/base
        |
deploy/backup.sh --rclone sync---> <bucket>/<prefix>/{base,wal}
```

- `deploy/pg-archive-wal.sh` — `archive_command`; idempotently copies each WAL
  segment into the local archive directory. Requires `archive_mode=on`.
- `deploy/backup.sh` — takes a `pg_basebackup` (tar, gzip, streaming WAL),
  ships base + WAL to object storage with rclone, prunes expired base backups,
  and can validate the newest backup (`--restore-check`).

## One-time setup

1. Create the rclone remote. Example for OCI Object Storage:

   ```bash
   sudo rclone --config /etc/xego-backup/rclone.conf config
   # Name it "xegobackup", type "s3", provider "Other" / OCI-compatible,
   # endpoint + access key + secret. Create the bucket.
   ```

2. Enable WAL archiving on PostgreSQL:

   ```conf
   wal_level = replica
   archive_mode = on
   archive_command = '/opt/whatsapp-payment/pg-archive-wal.sh %p %f'
   ```

   Restart PostgreSQL and confirm `pg_stat_archiver` shows successful
   `last_archived_wal`. For Docker Compose this is already wired into the `db`
   service command.

3. Run the first backup:

   ```bash
   sudo -u postgres BACKUP_BUCKET=my-bucket PGPASSWORD=... \
     bash deploy/backup.sh --restore-check
   ```

   The `--restore-check` flag unpacks the fresh base backup and runs
   `pg_controldata` to prove it restores cleanly.

## Scheduling

Native VPS (systemd timer), e.g. daily base backup every 24h:

```ini
# /etc/systemd/system/xego-backup.service
[Unit]
Description=Xego PostgreSQL backup
After=postgresql.service

[Service]
Type=oneshot
EnvironmentFile=/etc/whatsapp-payment.env
ExecStart=/opt/whatsapp-payment/deploy/backup.sh

# /etc/systemd/system/xego-backup.timer
[Unit]
Description=Run Xego backup daily

[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now xego-backup.timer
sudo systemctl list-timers xego-backup
```

Docker Compose one-shot (manual or via your scheduler):

```bash
BACKUP_BUCKET=my-bucket docker compose run --rm backup
```

## Point-in-time restore (PITR)

1. Create a scratch directory and download the newest base backup:

   ```bash
   sudo mkdir -p /var/lib/postgresql/pgrestore
   sudo rclone --config /etc/xego-backup/rclone.conf \
     copy "xegobackup:my-bucket/whatsapp-payment/base" /tmp/xego-restore
   # pick the newest <stamp>/base.tar.gz
   ```

2. Unpack and configure recovery:

   ```bash
   sudo mkdir -p /var/lib/postgresql/pgrestore
   sudo tar -xzf /tmp/xego-restore/<stamp>/base.tar.gz -C /var/lib/postgresql/pgrestore
   sudo chown -R postgres:postgres /var/lib/postgresql/pgrestore

   # Tell PostgreSQL to replay archived WAL up to a target time.
   echo "restore_command = 'rclone --config /etc/xego-backup/rclone.conf copyto xegobackup:my-bucket/whatsapp-payment/wal/%f %p'"
   echo "recovery_target_time = '2026-08-10 12:00:00+00'"
   ```

   To restore to the very latest available state, omit `recovery_target_time`
   and set `recovery_target = 'immediate'` (end of archived WAL). Create a
   `recovery.signal` file in the data directory to put the cluster in
   recovery mode.

3. Start the restored cluster on a separate port against the unpacked data
   directory and verify:

   ```bash
   sudo -u postgres /usr/lib/postgresql/17/bin/postgres \
     -D /var/lib/postgresql/pgrestore -p 5433
   psql -p 5433 -c "select count(*) from payments;"
   ```

4. Swap into production only after verification, per the standard
   `pg_ctl promote` + failover procedure.

## Validation & monitoring

- `pg_stat_archiver` — `archived_count` grows, `last_failed_archive` empty.
- `deploy/backup.sh --restore-check` — prove the newest backup restores.
- RPO check: ensure the base-backup interval is within `BACKUP_RPO` and WAL
  sync keeps the gap under it. With a 1h RPO, schedule base backups at most
  daily and verify WAL segments ship continuously.
- Retention: expired base backups are pruned; WAL older than the oldest base
  backup is not needed for PITR but keep `BACKUP_RETENTION_DAYS` of WAL for
  point-in-time windows.

## Security

- The bucket is private; access keys live only in the rclone config
  (`RCLONE_CONFIG`, mounted read-only into the compose `backup` service).
- `BACKUP_DIR` WAL segments are chmod 0600 and owned by the DB user.
- Treat the base/WAL backups as sensitive (they contain customer PII); enable
  server-side encryption on the bucket and restrict who can read
  `/etc/xego-backup/rclone.conf`.
