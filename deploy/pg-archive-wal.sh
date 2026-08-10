#!/usr/bin/env bash
# Xego PostgreSQL archive_command: copy one WAL segment into the local archive
# directory. The scheduler (deploy/backup.sh) later ships the directory to the
# S3-compatible bucket and prunes expired segments.
#
# Wire into postgresql.conf as:
#   archive_mode = on
#   archive_command = '/opt/whatsapp-payment/pg-archive-wal.sh %p %f'
#
# The script is idempotent: re-archiving an existing segment succeeds so the
# postmaster does not reschedule or complain about duplicates.
set -Eeuo pipefail

ARCHIVE_DIR="${BACKUP_DIR:-/var/lib/xego-backup}/wal"

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <source-path> <segment-name>" >&2
  exit 1
fi

SRC="$1"
SEG="$2"

mkdir -p "$ARCHIVE_DIR"

if [[ -f "$ARCHIVE_DIR/$SEG" ]]; then
  exit 0
fi

cp "$SRC" "$ARCHIVE_DIR/$SEG"
chmod 0600 "$ARCHIVE_DIR/$SEG"
