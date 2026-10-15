#!/usr/bin/env bash
# Async streaming standby for the Phase-1 topology (pre-deploy resilience
# prerequisite): a hot standby of the primary PostgreSQL on the second app
# server, so a primary-host failure has a warm failover target a minute away
# instead of a 15-minute PITR restore.
#
# Usage (on the standby host):
#   PRIMARY_HOST=db1.internal PGPASSWORD=... \
#     bash deploy/setup-standby.sh init        # pg_basebackup -R + standby.signal
#   bash deploy/setup-standby.sh status        # is it streaming? lag?
#   bash deploy/setup-standby.sh promote       # MANUAL FAILOVER (see below)
#
# Promote procedure (documented in docs/runbooks/GO_LIVE_RUNBOOK.md §4.1):
#   1. promote                              -> standby becomes primary
#   2. repoint PgBouncer/app DATABASE_URL   -> point at the promoted host
#   3. rebuild the old primary as standby  -> pg_rewind or fresh basebackup
# This script performs step 1 only; steps 2-3 are operator actions with
# environment-specific hosts, so they are runbook, not code.
#
# Requirements: pg_basebackup/pg_ctl/psql on PATH, network access to the
# primary's replication port, and a replication-capable role (the primary's
# POSTGRES_USER works; it is superuser in the container bootstrap).

set -Eeuo pipefail

STANDBY_DATA_DIR="${STANDBY_DATA_DIR:-/var/lib/postgresql/standby}"
STANDBY_PORT="${STANDBY_PORT:-5432}"
PRIMARY_HOST="${PRIMARY_HOST:-}"
PRIMARY_PORT="${PRIMARY_PORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGPASSWORD="${PGPASSWORD:-}"

cmd="${1:-status}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

case "$cmd" in
  init)
    require_command pg_basebackup
    if [[ -z "$PRIMARY_HOST" ]]; then
      echo "PRIMARY_HOST is required (the primary's address to copy from)" >&2
      exit 1
    fi
    if [[ -z "$PGPASSWORD" ]]; then
      echo "PGPASSWORD is required (replication-capable role)" >&2
      exit 1
    fi
    if [[ -e "$STANDBY_DATA_DIR/PG_VERSION" ]]; then
      echo "standby data dir already initialised at $STANDBY_DATA_DIR" >&2
      echo "use 'status' to check it, or remove the directory to re-init" >&2
      exit 1
    fi
    mkdir -p "$STANDBY_DATA_DIR"
    echo "== pg_basebackup from $PRIMARY_HOST:$PRIMARY_PORT (streaming WAL) =="
    # -R writes standby.signal + primary_conninfo (host/port/user) so the
    # started cluster attaches as a replica with no hand-edited config.
    PGPASSWORD="$PGPASSWORD" pg_basebackup \
      -h "$PRIMARY_HOST" -p "$PRIMARY_PORT" -U "$PGUSER" \
      -D "$STANDBY_DATA_DIR" \
      -R -X stream -P
    echo "== standby initialised at $STANDBY_DATA_DIR =="
    echo "Start it (hot_standby is on by default in postgresql.conf) and"
    echo "verify with: $0 status"
    ;;

  status)
    require_command psql
    if [[ ! -e "$STANDBY_DATA_DIR/PG_VERSION" ]]; then
      echo "not initialised: no data dir at $STANDBY_DATA_DIR (run 'init')"
      exit 1
    fi
    # pg_is_in_recovery distinguishes standby (t) from promoted primary (f);
    # pg_last_wal_replay_lsn vs pg_last_wal_receive_lsn shows apply lag.
    psql -h 127.0.0.1 -p "$STANDBY_PORT" -U "$PGUSER" -d postgres -tAc \
      "SELECT CASE WHEN pg_is_in_recovery() THEN 'standby (streaming)' ELSE 'primary (not in recovery)' END,
              COALESCE(pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 0) || ' bytes apply lag'"
    ;;

  promote)
    require_command pg_ctl
    if [[ ! -e "$STANDBY_DATA_DIR/PG_VERSION" ]]; then
      echo "not initialised: no data dir at $STANDBY_DATA_DIR" >&2
      exit 1
    fi
    echo "== promoting standby to primary =="
    # pg_ctl promote is the documented manual-failover path; afterwards the
    # operator repoints the app/PgBouncer at this host (runbook §4.1).
    pg_ctl -D "$STANDBY_DATA_DIR" promote -w -t 60
    echo "promoted. Next (manual): repoint DATABASE_URL / PgBouncer at this"
    echo "host, then rebuild the old primary as a new standby (pg_rewind or"
    echo "fresh pg_basebackup) once it is back."
    ;;

  *)
    echo "usage: $0 {init|status|promote}" >&2
    exit 1
    ;;
esac
