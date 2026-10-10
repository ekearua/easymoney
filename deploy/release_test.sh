#!/usr/bin/env bash
# release_test.sh — hermetic tests for deploy/release.sh.
#
# release.sh orchestrates `docker compose`; this harness puts a recording shim
# named `docker` first on PATH and runs the real script against a throwaway
# project root, so the whole release flow — pull, backup-before-migrate, the
# readiness gate, the automatic image rollback and the edge-stack variant — is
# exercised without a Docker daemon, a registry or a database.
#
# It is deliberately a shell harness rather than a Go test: the artifact under
# test is a shell script, and the failure modes that matter (ordering of
# side-effects, the exit code of a gated rollout, which image a rollback
# restores) are exactly what the recorded call log shows.
#
#   bash deploy/release_test.sh
#
# Exit status is non-zero if any case fails. The shim reports health per image,
# so "the new image never becomes ready" is a first-class scenario rather than
# a comment.

set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="${HERE}/release.sh"
[ -f "${SRC}" ] || { echo "release.sh not found next to ${0}" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

BIN="${WORK}/bin"
PROJ="${WORK}/proj"
LOG="${WORK}/calls.log"
mkdir -p "${BIN}" "${PROJ}/deploy"
cp "${SRC}" "${PROJ}/deploy/release.sh"
chmod +x "${PROJ}/deploy/release.sh"

# A controlled project root, so the harness never reads the real repo's .env.
printf 'PUBLIC_HOST=release-test.invalid\n' >"${PROJ}/.env"

REPO="ghcr.io/ekearua/easymoney"
PREV_IMAGE="${REPO}:v1.3.0"
NEW_TAG="v1.4.0"
NEW_IMAGE="${REPO}:${NEW_TAG}"

# ---------------------------------------------------------------- the shim --

cat >"${BIN}/docker" <<'SHIM'
#!/usr/bin/env bash
# Recording stub for docker/docker-compose, driven by SHIM_* env vars.
set -Eeuo pipefail
LOG="${SHIM_LOG:?SHIM_LOG must be set}"
printf 'docker %s\n' "$*" >>"${LOG}"

if [ "${1:-}" = "inspect" ]; then
  fmt=""
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      --format) fmt="${2:-}"; shift 2 ;;
      *) shift ;;
    esac
  done
  case "${fmt}" in
    # release.sh asks what the running container was created from...
    *Config.Image*) printf '%s\n' "${SHIM_CURRENT_IMAGE:-}" ;;
    # ...and separately what its health is. Health is a function of the image
    # currently pinned, which is what makes "the new image never reports
    # healthy" reproducible.
    *)
      if [ -n "${SHIM_UNHEALTHY_IMAGE:-}" ] && [ "${APP_IMAGE:-}" = "${SHIM_UNHEALTHY_IMAGE}" ]; then
        printf 'starting\n'
      else
        printf 'healthy\n'
      fi
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = "login" ]; then
  printf '  login=%s\n' "${2:-}" >>"${LOG}"
  exit 0
fi

if [ "${1:-}" = "compose" ]; then shift; fi
while [ "${1:-}" = "-f" ] || [ "${1:-}" = "--file" ]; do shift 2; done

sub="${1:-}"; shift || true
case "${sub}" in
  version) exit 0 ;;
  config)
    case "${1:-}" in
      --services) printf '%s\n' ${SHIM_SERVICES} ;;
    esac
    exit 0
    ;;
  ps)
    printf '%s\n' ${SHIM_APP_IDS}
    exit 0
    ;;
  pull)
    printf '  pull targets=%s image=%s\n' "$*" "${APP_IMAGE:-}" >>"${LOG}"
    [ "${SHIM_PULL_FAIL:-0}" = "1" ] && exit 1
    exit 0
    ;;
  run)
    # `run [flags] <service> [command...]` — the service is the first token
    # that is not a flag, so both `run --rm backup` and
    # `run --rm --no-deps app synthetic pipeline` resolve correctly.
    svc=""
    for arg in "$@"; do
      case "${arg}" in -*) continue ;; esac
      svc="${arg}"; break
    done
    printf '  run=%s image=%s\n' "${svc}" "${APP_IMAGE:-}" >>"${LOG}"
    case "${svc}" in
      migrate) [ "${SHIM_MIGRATE_FAIL:-0}" = "1" ] && exit 1 ;;
      backup) [ "${SHIM_BACKUP_FAIL:-0}" = "1" ] && exit 1 ;;
      app)
        # Fail the synthetic stage only for the named image, so a rollback to a
        # healthy previous revision can still pass its own check.
        if [ -n "${SHIM_SYNTHETIC_FAIL_IMAGE:-}" ] && [ "${APP_IMAGE:-}" = "${SHIM_SYNTHETIC_FAIL_IMAGE}" ]; then
          exit 1
        fi
        ;;
    esac
    exit 0
    ;;
  up)
    printf '  up=%s image=%s\n' "${*: -1}" "${APP_IMAGE:-}" >>"${LOG}"
    exit 0
    ;;
  *) exit 0 ;;
esac
SHIM
chmod +x "${BIN}/docker"

# ------------------------------------------------------------- test runner --

PASS=0
FAIL=0
CURRENT=""

fail() {
  echo "  FAIL: $*" >&2
  FAIL=$((FAIL + 1))
}

ok() { PASS=$((PASS + 1)); }

# run_release <tag> — resets the clock-free fixtures and runs the real script.
run_release() {
  : >"${LOG}"
  rm -f "${PROJ}/deploy/.release-state"
  set +e
  (
    cd "${PROJ}"
    PATH="${BIN}:${PATH}" \
    RELEASE_SKIP_INGRESS_CHECK=1 \
    RELEASE_HEALTH_TIMEOUT=2 \
    RELEASE_HEALTH_INTERVAL=1 \
    SHIM_LOG="${LOG}" \
    SHIM_SERVICES="${SHIM_SERVICES}" \
    SHIM_APP_IDS="${SHIM_APP_IDS}" \
    SHIM_CURRENT_IMAGE="${SHIM_CURRENT_IMAGE}" \
    SHIM_UNHEALTHY_IMAGE="${SHIM_UNHEALTHY_IMAGE:-}" \
    SHIM_PULL_FAIL="${SHIM_PULL_FAIL:-0}" \
    SHIM_MIGRATE_FAIL="${SHIM_MIGRATE_FAIL:-0}" \
    SHIM_BACKUP_FAIL="${SHIM_BACKUP_FAIL:-0}" \
    SHIM_SYNTHETIC_FAIL_IMAGE="${SHIM_SYNTHETIC_FAIL_IMAGE:-}" \
    bash deploy/release.sh "$1" >"${WORK}/out.log" 2>&1
  )
  RC=$?
  set -e
  OUT="$(cat "${WORK}/out.log")"
}

# line_of <needle> — 1-based line number of the first log line containing the
# needle, or empty when absent. Used for ordering assertions.
line_of() { grep -n -F "$1" "${LOG}" | head -n1 | cut -d: -f1; }

expect_rc() {
  if [ "${RC}" -ne "$1" ]; then fail "exit code: expected $1, got ${RC}"; return 1; fi
  ok
}
expect_out() {
  if ! printf '%s' "${OUT}" | grep -qF "$1"; then fail "output missing: $1"; return 1; fi
  ok
}
expect_log() {
  if ! grep -qF "$1" "${LOG}"; then fail "call log missing: $1"; return 1; fi
  ok
}
expect_no_log() {
  if grep -qF "$1" "${LOG}"; then fail "call log should not contain: $1"; return 1; fi
  ok
}
expect_order() {
  local a b ia ib
  a="$1"; b="$2"
  ia="$(line_of "${a}")"; ib="$(line_of "${b}")"
  if [ -z "${ia}" ] || [ -z "${ib}" ]; then
    fail "cannot order '${a}' (line ${ia:-none}) before '${b}' (line ${ib:-none})"
    return 1
  fi
  if [ "${ia}" -ge "${ib}" ]; then fail "'${a}' (line ${ia}) must precede '${b}' (line ${ib})"; return 1; fi
  ok
}

# dump_log prints the recorded call sequence — the single most useful thing to
# see when an ordering or presence assertion fails.
dump_log() {
  echo "  --- recorded calls ---" >&2
  sed 's/^/  /' "${LOG}" >&2
  echo "  ----------------------" >&2
}

# begin() prints the previous case's call log when that case had failures, so
# a broken assertion is always accompanied by the sequence that produced it.
FAIL_AT_BEGIN=0
begin() {
  if [ "${FAIL}" -gt "${FAIL_AT_BEGIN}" ]; then dump_log; fi
  CURRENT="$1"; FAIL_AT_BEGIN="${FAIL}"
  printf '\n== %s\n' "${CURRENT}"; : >"${LOG}"
}

# ------------------------------------------------------------- case: happy --

begin "happy path: pull, backup before migrate, app then caddy, exit 0"
SHIM_SERVICES="db pgbouncer backup backup-scheduler migrate app caddy"
SHIM_APP_IDS="cid-app-1 cid-app-2"
SHIM_CURRENT_IMAGE="${PREV_IMAGE}"
SHIM_UNHEALTHY_IMAGE=""
run_release "${NEW_TAG}"
expect_rc 0
expect_log "pull targets=app migrate image=${NEW_IMAGE}"
expect_order "run=backup" "run=migrate"
expect_order "run=migrate" "up=app"
expect_order "up=app" "up=caddy"
expect_order "up=caddy" "run=app"
expect_out "synthetic checks passed"
expect_log "up=app image=${NEW_IMAGE}"
expect_out "release ${NEW_TAG} complete"
if [ ! -f "${PROJ}/deploy/.release-state" ]; then
  fail "state file was not written on success"
else
  ok
  grep -q "^image=${NEW_IMAGE}$" "${PROJ}/deploy/.release-state" \
    || fail "state file did not record the released image"
  grep -q "^previous_image=${PREV_IMAGE}$" "${PROJ}/deploy/.release-state" \
    || fail "state file did not record the previous image"
fi

# ---------------------------------------------------------- case: rollback --

begin "rollback: new image never ready -> previous image restored, exit 1"
SHIM_UNHEALTHY_IMAGE="${NEW_IMAGE}"
run_release "${NEW_TAG}"
expect_rc 1
expect_log "up=app image=${NEW_IMAGE}"
expect_log "up=app image=${PREV_IMAGE}"
expect_out "ROLLBACK: restoring ${PREV_IMAGE}"
expect_out "ROLLBACK OK"
expect_out "release ${NEW_TAG} failed"
# The gate must run against the NEW image first, then the previous one.
NEW_UP="$(grep -n -F "up=app image=${NEW_IMAGE}" "${LOG}" | head -n1 | cut -d: -f1)"
PREV_UP="$(grep -n -F "up=app image=${PREV_IMAGE}" "${LOG}" | head -n1 | cut -d: -f1)"
if [ -z "${PREV_UP}" ] || [ "${NEW_UP}" -ge "${PREV_UP}" ]; then
  fail "rollback must attempt the new image before restoring the previous one"
else
  ok
fi
if [ -f "${PROJ}/deploy/.release-state" ]; then
  fail "a failed release must not record a last-good state"
else
  ok
fi

# --------------------------------------------------- case: migrate failure --

begin "migrate failure: aborts before the app is touched"
SHIM_UNHEALTHY_IMAGE=""
SHIM_MIGRATE_FAIL=1
run_release "${NEW_TAG}"
expect_rc 1
expect_out "MIGRATION FAILED"
expect_no_log "up=app"
SHIM_MIGRATE_FAIL=0

# ----------------------------------------------------- case: pull failure ---

begin "pull failure: nothing on the host is touched"
SHIM_PULL_FAIL=1
run_release "${NEW_TAG}"
expect_rc 1
expect_out "nothing on the host changed"
expect_no_log "run="
expect_no_log "up="
SHIM_PULL_FAIL=0

# --------------------------------------------------- case: backup failure ---

begin "backup failure: aborts before any schema change"
SHIM_BACKUP_FAIL=1
run_release "${NEW_TAG}"
expect_rc 1
expect_out "pre-migrate backup failed"
expect_no_log "run=migrate"
SHIM_BACKUP_FAIL=0

# --------------------------------------------------- case: synthetic fail ---

begin "synthetic failure after a healthy rollout: image rolled back"
SHIM_UNHEALTHY_IMAGE=""
SHIM_SYNTHETIC_FAIL_IMAGE="${NEW_IMAGE}"
run_release "${NEW_TAG}"
expect_rc 1
expect_log "run=app image=${NEW_IMAGE}"
expect_out "SYNTHETIC CHECKS FAILED"
expect_out "ROLLBACK: restoring ${PREV_IMAGE}"
expect_out "ROLLBACK OK"
expect_log "run=app image=${PREV_IMAGE}"
SHIM_SYNTHETIC_FAIL_IMAGE=""

# ------------------------------------------------------- case: edge stack ---

begin "edge stack: no migrate/backup services, app + caddy only"
SHIM_SERVICES="app caddy"
SHIM_UNHEALTHY_IMAGE=""
run_release "${NEW_TAG}"
expect_rc 0
expect_log "pull targets=app image=${NEW_IMAGE}"
expect_no_log "run=migrate"
expect_no_log "run=backup"
expect_log "up=app image=${NEW_IMAGE}"
expect_log "up=caddy"
expect_out "schema is owned by the primary"
expect_log "run=app image=${NEW_IMAGE}"
SHIM_SERVICES="db pgbouncer backup backup-scheduler migrate app caddy"

# ------------------------------------------------------------------ report --

if [ "${FAIL}" -gt "${FAIL_AT_BEGIN}" ]; then dump_log; fi

printf '\n----------------------------------------\n'
printf '%s passed, %s failed\n' "${PASS}" "${FAIL}"
if [ "${FAIL}" -ne 0 ]; then
  echo "release_test.sh: FAILED" >&2
  exit 1
fi
echo "release_test.sh: OK"
