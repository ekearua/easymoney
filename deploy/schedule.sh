#!/usr/bin/env bash
# Xego backup scheduler — long-running container entrypoint for the
# `backup-scheduler` compose service.
#
# One process drives all three recurring backup jobs off an epoch-aligned
# tick so the schedule cannot drift (unlike a naive `sleep 900` loop), and
# serialises runs with flock so a slow backup never overlaps its successor
# (or a manual `docker compose run --rm backup`):
#   - every 15 min (900 s, aligned to the epoch): WAL-only offsite ship
#   - daily, first tick at/after 02:30 UTC: full pg_basebackup + ship
#   - Monday, first tick at/after 03:00 UTC: full PITR restore drill
#
# Companion to deploy/backup.sh (same image); the one-shot `backup` service
# stays available for manual runs.

set -Eeuo pipefail

INTERVAL="${SCHED_INTERVAL:-900}"
BACKUP_DIR="${BACKUP_DIR:-/var/lib/xego-backup}"
BACKUP="${BACKUP:-/opt/xego-backup/backup.sh}"
LOCK="$BACKUP_DIR/.schedule.lock"

mkdir -p "$BACKUP_DIR"

log() { printf '[schedule] %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

run() {
  local label="$1"
  shift
  if flock -n "$LOCK" "$@"; then
    log "$label: ok"
  else
    log "$label: skipped (another backup run holds the lock)"
  fi
}

sleep_to_next_tick() {
  local now next
  now=$(date -u +%s)
  next=$(( (now / INTERVAL + 1) * INTERVAL ))
  sleep $(( next - now ))
}

last_base_day=""
last_drill_day=""

log "scheduler started (interval ${INTERVAL}s)"
while :; do
  sleep_to_next_tick

  run "wal-ship" "$BACKUP" --wal-ship

  today=$(date -u +%Y-%m-%d)
  hhmm=$(date -u +%H%M)
  dow=$(date -u +%u)

  if [ "$hhmm" -ge 230 ] && [ "$today" != "$last_base_day" ]; then
    run "base backup" "$BACKUP"
    last_base_day="$today"
  fi

  if [ "$dow" = "1" ] && [ "$hhmm" -ge 300 ] && [ "$today" != "$last_drill_day" ]; then
    run "restore drill" "$BACKUP" --restore-drill
    last_drill_day="$today"
  fi
done
