#!/usr/bin/env bash
# Xego PostgreSQL backup + point-in-time-recovery scheduler.
#
# Takes a pg_basebackup full backup, ships the local WAL archive to an
# S3-compatible bucket via rclone (OCI Object Storage, AWS S3, MinIO, ...),
# and prunes remote backups older than BACKUP_RETENTION_DAYS.
#
# Model: PostgreSQL writes WAL into BACKUP_DIR/wal via the archive_command in
# deploy/pg-archive-wal.sh. This script runs on a schedule (cron / systemd
# timer / compose one-shot) and:
#   1. pg_basebackup -> BACKUP_DIR/base
#   2. rclone sync BACKUP_DIR/{base,wal} -> BACKUP_BUCKET/BACKUP_PREFIX
#   3. prune remote objects older than BACKUP_RETENTION_DAYS
#   4. optionally validate the newest backup with --restore-check
#
# Requirements: pg_basebackup, rclone, BACKUP env vars (see .env.example).
#
# Optional overrides:
#   BACKUP_DIR=/var/lib/xego-backup BACKUP_RETENTION_DAYS=30
#   BACKUP_BUCKET=my-bucket BACKUP_PREFIX=whatsapp-payment
#   RCLONE_CONFIG=/etc/xego-backup/rclone.conf RCLONE_REMOTE=xegobackup
#   BACKUP_RPO=1h BACKUP_RTO=15m
#   bash deploy/backup.sh --restore-check
#   bash deploy/backup.sh --restore-drill        (full PITR drill on NEWEST backup)
#   bash deploy/backup.sh --restore-drill --drill-stamp=20260812T020000Z  (specific base)

set -Eeuo pipefail

BACKUP_DIR="${BACKUP_DIR:-/var/lib/xego-backup}"
BACKUP_BUCKET="${BACKUP_BUCKET:-}"
BACKUP_PREFIX="${BACKUP_PREFIX:-whatsapp-payment}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
BACKUP_RPO="${BACKUP_RPO:-1h}"
BACKUP_RTO="${BACKUP_RTO:-15m}"
RCLONE_CONFIG="${RCLONE_CONFIG:-/etc/xego-backup/rclone.conf}"
RCLONE_REMOTE="${RCLONE_REMOTE:-xegobackup}"

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGPASSWORD="${PGPASSWORD:-}"

RESTORE_CHECK=0
RESTORE_DRILL=0
DRILL_STAMP=""
for arg in "$@"; do
  case "$arg" in
    --restore-check) RESTORE_CHECK=1 ;;
    --restore-drill) RESTORE_DRILL=1 ;;
    --drill-stamp=*) DRILL_STAMP="${arg#--drill-stamp=}" ;;
  esac
done

RESTORE_DIR="/tmp/xego-restore-check"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

fail() {
  echo "backup failed: $*" >&2
  exit 1
}

require_command pg_basebackup
require_command rclone

if [[ -z "$BACKUP_BUCKET" ]]; then
  fail "BACKUP_BUCKET is not set"
fi
if [[ -z "$PGPASSWORD" ]]; then
  fail "PGPASSWORD is not set (replication-capable role required)"
fi
if [[ ! -f "$RCLONE_CONFIG" ]]; then
  fail "rclone config not found: $RCLONE_CONFIG (create with: rclone config)"
fi

REMOTE_ROOT="${RCLONE_REMOTE}:${BACKUP_BUCKET}/${BACKUP_PREFIX}"
BASE_REMOTE="${REMOTE_ROOT}/base"
WAL_REMOTE="${REMOTE_ROOT}/wal"
RCLONE=("rclone" "--config" "$RCLONE_CONFIG")

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BASE_LOCAL="$BACKUP_DIR/base"
WAL_LOCAL="$BACKUP_DIR/wal"

echo "== Xego PostgreSQL backup =="
echo "Server:      $PGHOST:$PGPORT (user $PGUSER)"
echo "Target:      $REMOTE_ROOT"
echo "Retention:   ${BACKUP_RETENTION_DAYS} days (RPO ${BACKUP_RPO}, target RTO ${BACKUP_RTO})"
echo

if [[ ! -d "$WAL_LOCAL" ]]; then
  echo "Local WAL archive is empty ($WAL_LOCAL). Ensure archive_mode=on and the"
  echo "archive_command in deploy/pg-archive-wal.sh are configured before the"
  echo "first backup, otherwise the base backup cannot be replayed to PITR."
fi

echo "== Step 1: pg_basebackup (full) =="
rm -rf "$BASE_LOCAL"
mkdir -p "$BASE_LOCAL"
PGPASSWORD="$PGPASSWORD" pg_basebackup \
  -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" \
  -D "$BASE_LOCAL" \
  -Ft -z -X stream \
  || fail "pg_basebackup failed"

echo "== Step 2: ship base backup and WAL to object storage =="
"${RCLONE[@]}" copyto "$BASE_LOCAL/base.tar.gz" "$BASE_REMOTE/$STAMP/base.tar.gz" \
  || fail "upload of base backup failed"
"${RCLONE[@]}" sync "$WAL_LOCAL" "$WAL_REMOTE" \
  || fail "sync of WAL archive failed"

echo "== Step 3: prune remote backups older than ${BACKUP_RETENTION_DAYS} days =="
if command -v date >/dev/null 2>&1; then
  CUTOFF="$(date -u -d "${BACKUP_RETENTION_DAYS} days ago" +%Y%m%dT%H%M%SZ)"
  for base in $("${RCLONE[@]}" lsf "$BASE_REMOTE" 2>/dev/null); do
    stamp="${base%/}"
    if [[ "$stamp" < "$CUTOFF" ]]; then
      echo "pruning expired base backup: $stamp"
      "${RCLONE[@]}" purge "$BASE_REMOTE/$stamp" || true
    fi
  done
fi

echo "== Step 4: restore check (--restore-check only) =="
if [[ "$RESTORE_CHECK" == "1" ]]; then
  rm -rf "$RESTORE_DIR"
  mkdir -p "$RESTORE_DIR"
  "${RCLONE[@]}" copy "$BASE_REMOTE/$STAMP/base.tar.gz" "$RESTORE_DIR"
  tar -xzf "$RESTORE_DIR/base.tar.gz" -C "$RESTORE_DIR"
  if command -v pg_controldata >/dev/null 2>&1; then
    pg_controldata "$RESTORE_DIR" >/dev/null || fail "restore check: pg_controldata failed"
  else
    echo "pg_controldata not found; skipping cluster validation (install postgresql-client tools)"
  fi
  echo "restore check: backup restores cleanly to $RESTORE_DIR"
fi

# restore-drill: full point-in-time recovery drill against the newest (or
# --drill-stamp=) base backup. This is the C23 recovery drill: unpack the base
# backup, replay archived WAL from object storage via restore_command, then
# verify that the payments relation is present and reports a row count. It is
# the read-out-of-the-box proof that the backup+WAL can actually rebuild the
# database to the latest state.
echo "== Step 5: PITR restore drill (--restore-drill only) =="
if [[ "$RESTORE_DRILL" == "1" ]]; then
  DRILL_DATABASE="${DRILL_DATABASE:-whatsapp_payment}"
  DRILL_PORT="${DRILL_PORT:-55433}"
  DRILL_SCRIPT_DIR="${DRILL_DIR:-/tmp/xego-restore-drill}"
  DRILL_USER="${DRILL_USER:-postgres}"

  for tool in pg_ctl psql; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      fail "restore drill requires $tool on the drill host"
    fi
  done

  if [[ -z "$DRILL_STAMP" ]]; then
    DRILL_STAMP="$("${RCLONE[@]}" lsf "$BASE_REMOTE" 2>/dev/null | sort | tail -n 1 | tr -d '/')"
  fi
  if [[ -z "$DRILL_STAMP" ]]; then
    fail "restore drill: no base backups found under $BASE_REMOTE"
  fi
  echo "Drill source: $BASE_REMOTE/$DRILL_STAMP"

  rm -rf "$DRILL_SCRIPT_DIR"
  mkdir -p "$DRILL_SCRIPT_DIR"
  "${RCLONE[@]}" copy "$BASE_REMOTE/$DRILL_STAMP/base.tar.gz" "$DRILL_SCRIPT_DIR/"
  tar -xzf "$DRILL_SCRIPT_DIR/base.tar.gz" -C "$DRILL_SCRIPT_DIR"
  rm -f "$DRILL_SCRIPT_DIR/base.tar.gz"

  # Recovery config: restore_command pulls archived WAL from object storage.
  touch "$DRILL_SCRIPT_DIR/recovery.signal"
  cat > "$DRILL_SCRIPT_DIR/xego-restore-wal.sh" <<EOF
#!/bin/sh
exec "${RCLONE[@]}" copyto "$WAL_REMOTE/$2" "$1"
EOF
  chmod +x "$DRILL_SCRIPT_DIR/xego-restore-wal.sh"

  # Trust-local drill auth so the recovery connection cannot be blocked by the
  # archived cluster's pg_hba (roles may not exist outside the source VPS).
  cat > "$DRILL_SCRIPT_DIR/drill-pg-hba.conf" <<EOF
local all all trust
host all all 127.0.0.1/32 trust
host all all ::1/128 trust
EOF

  DRILL_LOG="$DRILL_SCRIPT_DIR/postgres.log"
  if pg_ctl -D "$DRILL_SCRIPT_DIR" \
    -o "-p $DRILL_PORT -c listen_addresses=127.0.0.1 -c unix_socket_directories='$DRILL_SCRIPT_DIR' -c hba_file='$DRILL_SCRIPT_DIR/drill-pg-hba.conf' -c restore_command='$DRILL_SCRIPT_DIR/xego-restore-wal.sh %p %f'" \
    -l "$DRILL_LOG" -w start >/dev/null 2>&1; then
    echo "recovery cluster started on 127.0.0.1:$DRILL_PORT"
  else
    fail "restore drill: pg_ctl start failed (see $DRILL_LOG); run as the postgres-capable user, not root"
  fi

  # Wait until the payments relation is queryable (WAL replay caught up), then
  # read out a row count as the drill's data verification.
  DRILL_VERIFIED=""
  for _ in $(seq 1 60); do
    DRILL_VERIFIED="$(psql -h "$DRILL_SCRIPT_DIR" -p "$DRILL_PORT" -U "$DRILL_USER" -d "$DRILL_DATABASE" -tAc "select count(*) from pg_class where relname='payments';" 2>/dev/null || true)"
    if [[ "$DRILL_VERIFIED" == "1" ]]; then
      break
    fi
    sleep 2
  done
  DRILL_ROWS="$(psql -h "$DRILL_SCRIPT_DIR" -p "$DRILL_PORT" -U "$DRILL_USER" -d "$DRILL_DATABASE" -tAc "select count(*) from payments;" 2>/dev/null || true)"
  pg_ctl -D "$DRILL_SCRIPT_DIR" -m fast -t 20 stop >/dev/null 2>&1 || true

  if [[ "$DRILL_VERIFIED" != "1" ]]; then
    fail "restore drill: payments relation not found after WAL replay (see $DRILL_LOG)"
  fi
  echo "restore drill: PITR succeeded, payments rows = ${DRILL_ROWS:-0}"
  rm -rf "$DRILL_SCRIPT_DIR"
fi

echo
echo "Backup complete."
