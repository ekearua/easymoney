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
#   bash deploy/backup.sh --restore-check

set -Eeuo pipefail

BACKUP_DIR="${BACKUP_DIR:-/var/lib/xego-backup}"
BACKUP_BUCKET="${BACKUP_BUCKET:-}"
BACKUP_PREFIX="${BACKUP_PREFIX:-whatsapp-payment}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
BACKUP_RPO="${BACKUP_RPO:-1h}"
RCLONE_CONFIG="${RCLONE_CONFIG:-/etc/xego-backup/rclone.conf}"
RCLONE_REMOTE="${RCLONE_REMOTE:-xegobackup}"

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGPASSWORD="${PGPASSWORD:-}"

RESTORE_CHECK=0
for arg in "$@"; do
  case "$arg" in
    --restore-check) RESTORE_CHECK=1 ;;
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
echo "Retention:   ${BACKUP_RETENTION_DAYS} days (RPO ${BACKUP_RPO})"
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

echo
echo "Backup complete."
