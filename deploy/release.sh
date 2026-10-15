#!/usr/bin/env bash
# release.sh — the CD unit for the Phase-1 Docker topology.
#
#   bash deploy/release.sh v1.4.0
#
# What it does, in order, and why each step is gated the way it is:
#
#   1. resolves the image tag and records the image currently serving
#   2. pulls the published image (failure here changes nothing on the host)
#   3. takes a full base backup — BEFORE the migrate one-shot, because the
#      schema is forward-only and this backup is the only way back
#   4. runs the `migrate` one-shot (no-op on the edge host, which owns no schema)
#   5. recreates the `app` replicas from the new image and refuses to move on
#      until every replica reports healthy
#   6. recreates `caddy` so its upstream `app:8080` is re-resolved to the new
#      replica IPs, then checks readiness once more through ingress
#   7. runs the post-deploy synthetic checks (`demo synthetic`) against the new
#      revision, so a deploy that is up but not doing its job fails here
#   8. on any failure in 5-7, restores the previously running image
#      automatically and exits non-zero, so a bad tag leaves the last good
#      revision serving instead of taking the API down
#
# Rollback is image-only and deliberate. It does NOT reverse migrations: a
# release whose migration was destructive is recovered with point-in-time
# recovery (§9 of docs/runbooks/GO_LIVE_RUNBOOK.md), which is exactly why step 3 runs before
# step 4. The script says so out loud when it has to roll back.
#
# Runs on either host of the dual-Caddy topology: the primary stack
# (compose.yaml: db, pgbouncer, backup, migrate, app, caddy) and the edge
# stack (compose.edge.yaml: app, caddy). Services are detected, not assumed.
#
# Env (all optional; compose reads .env itself and the shell wins over it):
#   APP_IMAGE_REPO      registry repo (default ghcr.io/ekearua/easymoney)
#   APP_IMAGE_REGISTRY  registry host for login (default ghcr.io)
#   APP_IMAGE_USER      registry user for login (default GITHUB_ACTOR/token)
#   APP_IMAGE_TOKEN     registry password/token; unset skips login (public pkg)
#   COMPOSE_FILES       compose file selection (default "-f compose.yaml");
#                       edge host: COMPOSE_FILES="-f compose.edge.yaml"
#   PUBLIC_HOST         derive the ingress probe as https://$PUBLIC_HOST/health/ready
#   PUBLIC_HEALTH_URL   explicit ingress probe URL (overrides PUBLIC_HOST)
#   RELEASE_SKIP_INGRESS_CHECK=1  skip the through-Caddy probe
#   RELEASE_SKIP_SYNTHETIC=1      skip the post-deploy synthetic checks
#   RELEASE_SKIP_BACKUP=1         skip the pre-migrate backup (NOT for prod)
#   RELEASE_VERIFY_SIGNATURE=1    cosign-verify the image by digest BEFORE pull
#                                 (needs cosign + docker buildx on the host)
#   COSIGN_IDENTITY_REGEXP        cert identity to pin (default: this repo's
#                                 release workflow on a v* tag)
#   COSIGN_OIDC_ISSUER            OIDC issuer (default GitHub Actions)
#   RELEASE_HEALTH_TIMEOUT        seconds to wait at each gate (default 120)
#   RELEASE_HEALTH_INTERVAL       poll interval in seconds (default 3)
#   RELEASE_STATE_FILE            last-good marker (default deploy/.release-state)

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

log() { printf '[release] %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: bash deploy/release.sh <tag>

  <tag>   published image tag to roll out, e.g. v1.4.0
          (the image is <APP_IMAGE_REPO>:<tag>, default repo
           ghcr.io/ekearua/easymoney; override with APP_IMAGE_REPO)

Rolls out the image, applies migrations, gates on readiness, and restores the
previous image automatically if the new one never becomes ready.
EOF
}

TAG="${1:-}"
case "${TAG}" in
  -h|--help|help) usage; exit 0 ;;
  "") usage; exit 2 ;;
esac

command -v docker >/dev/null 2>&1 || die "docker not found on PATH"
docker compose version >/dev/null 2>&1 || die "docker compose v2 not found"

COMPOSE_FILES="${COMPOSE_FILES:--f compose.yaml}"
read -r -a COMPOSE_ARGS <<<"${COMPOSE_FILES}"

HEALTH_TIMEOUT="${RELEASE_HEALTH_TIMEOUT:-120}"
HEALTH_INTERVAL="${RELEASE_HEALTH_INTERVAL:-3}"
STATE_FILE="${RELEASE_STATE_FILE:-${SCRIPT_DIR}/.release-state}"
APP_IMAGE_REPO="${APP_IMAGE_REPO:-ghcr.io/ekearua/easymoney}"

compose() { docker compose "${COMPOSE_ARGS[@]}" "$@"; }

# ---------------------------------------------------------------- discovery --

SERVICES="$(compose config --services 2>/dev/null || true)"
has_service() { printf '%s\n' "${SERVICES}" | grep -qx "$1"; }

env_file_value() {
  local key="$1" file="${ROOT_DIR}/.env" line
  [ -f "${file}" ] || return 1
  line="$(grep -E "^[[:space:]]*${key}=" "${file}" 2>/dev/null | tail -n1)" || return 1
  [ -n "${line}" ] || return 1
  printf '%s' "${line#*=}" | tr -d '\r' | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'\$//"
}

state_value() {
  local key="$1" line
  [ -f "${STATE_FILE}" ] || return 1
  line="$(grep -E "^${key}=" "${STATE_FILE}" 2>/dev/null | tail -n1)" || return 1
  [ -n "${line}" ] || return 1
  printf '%s' "${line#*=}" | tr -d '\r'
}

# The image the running app container was created from — the truthful "what is
# serving right now", independent of what .env or the state file happen to say.
running_app_image() {
  local id
  id="$(compose ps -q app 2>/dev/null | head -n1 || true)"
  [ -n "${id}" ] || return 1
  docker inspect --format '{{.Config.Image}}' "${id}" 2>/dev/null
}

short_id() { printf '%s' "${1:0:12}"; }

# ------------------------------------------------------------------- gating --

wait_app_ready() {
  local deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  local ids id status total healthy
  while :; do
    ids="$(compose ps -q app 2>/dev/null || true)"
    if [ -n "${ids}" ]; then
      total=0; healthy=0
      for id in ${ids}; do
        total=$(( total + 1 ))
        status="$(docker inspect \
          --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
          "${id}" 2>/dev/null || echo unknown)"
        case "${status}" in
          healthy) healthy=$(( healthy + 1 )) ;;
          unhealthy|exited|dead)
            log "app container $(short_id "${id}") reported ${status}"
            return 1
            ;;
        esac
      done
      if [ "${total}" -gt 0 ] && [ "${healthy}" -eq "${total}" ]; then
        log "app healthy (${healthy}/${total} replica(s))"
        return 0
      fi
    fi
    if [ "$(date +%s)" -ge "${deadline}" ]; then
      log "timed out after ${HEALTH_TIMEOUT}s waiting for app readiness"
      return 1
    fi
    sleep "${HEALTH_INTERVAL}"
  done
}

wait_ingress_ready() {
  [ "${RELEASE_SKIP_INGRESS_CHECK:-0}" = "1" ] && { log "ingress check skipped (RELEASE_SKIP_INGRESS_CHECK=1)"; return 0; }
  local url="${PUBLIC_HEALTH_URL:-}"
  if [ -z "${url}" ]; then
    local host="${PUBLIC_HOST:-$(env_file_value PUBLIC_HOST || true)}"
    [ -n "${host}" ] && url="https://${host}/health/ready"
  fi
  if [ -z "${url}" ]; then
    log "no PUBLIC_HEALTH_URL/PUBLIC_HOST — skipping ingress check (app healthcheck already passed)"
    return 0
  fi
  command -v curl >/dev/null 2>&1 || { log "curl not found — skipping ingress check"; return 0; }

  local deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while :; do
    # -f makes a 503 (database unreachable) a non-zero exit, which is the whole
    # point of this probe: Caddy must not be serving a stale 200.
    if curl -fsS --max-time 5 "${url}" >/dev/null 2>&1; then
      log "ingress ready: ${url}"
      return 0
    fi
    if [ "$(date +%s)" -ge "${deadline}" ]; then
      log "ingress not ready within ${HEALTH_TIMEOUT}s: ${url}"
      return 1
    fi
    sleep "${HEALTH_INTERVAL}"
  done
}

# Recreate app from an image and gate on both readiness signals.
recreate_and_gate() {
  local image="$1"
  export APP_IMAGE="${image}"
  log "recreating app from ${image}"
  compose up -d --no-build --no-deps app || return 1
  wait_app_ready || return 1
  if has_service caddy; then
    # Caddy caches upstream addresses, so a recreated replica set is only
    # reachable after Caddy is recreated too; the caddy_data volume keeps the
    # certificates, so this is a config/upstream refresh, not a cert reissue.
    compose up -d --no-build --no-deps caddy >/dev/null || log "WARN: caddy recreate failed"
  fi
  wait_ingress_ready || return 1
  run_synthetic_checks || return 1
  return 0
}

# run_synthetic_checks exercises the deployed revision's real paths (database,
# schema, payment reference/receipt resolution, outbox drain) in a throwaway
# container from the same image, so a revision that is healthy but broken is a
# failed release rather than a silent one. `run --rm` never touches the serving
# replicas.
#
# During a rollback (ROLLBACK_MODE=1) the checks run image-only: the outbox
# backlog assertion is skipped because it reflects an environment/data condition
# the previous image cannot fix, and letting it fail here would turn a
# successful restore into a misleading "rollback failed".
run_synthetic_checks() {
  if [ "${RELEASE_SKIP_SYNTHETIC:-0}" = "1" ]; then
    log "synthetic checks skipped (RELEASE_SKIP_SYNTHETIC=1)"
    return 0
  fi
  local args=(synthetic pipeline)
  if [ "${ROLLBACK_MODE:-0}" = "1" ]; then
    args+=(--image-only)
    log "post-deploy synthetic checks (image-only: environment-level stages skipped)"
  else
    log "post-deploy synthetic checks"
  fi
  if compose run --rm --no-deps app "${args[@]}"; then
    log "synthetic checks passed"
    return 0
  fi
  log "SYNTHETIC CHECKS FAILED — the deployed revision is up but not doing its job"
  return 1
}

rollback() {
  if [ -z "${PREV_IMAGE}" ]; then
    log "ROLLBACK SKIPPED: no previously running image recorded."
    log "  Recover by pinning APP_IMAGE to the last known-good tag and re-running,"
    log "  or restore data with the PITR procedure (docs/runbooks/GO_LIVE_RUNBOOK.md §9)."
    return 1
  fi
  if [ "${PREV_IMAGE}" = "${NEW_IMAGE}" ]; then
    log "ROLLBACK SKIPPED: previous image equals the released image (${PREV_IMAGE})."
    return 1
  fi
  log "ROLLBACK: restoring ${PREV_IMAGE}"
  # Re-gate the previous image image-only (see run_synthetic_checks): its health
  # and schema/payment stages still count, but environment-level assertions must
  # not turn a successful restore into a false rollback failure.
  ROLLBACK_MODE=1
  if recreate_and_gate "${PREV_IMAGE}"; then
    log "ROLLBACK OK — ${PREV_IMAGE} is serving again"
    return 0
  fi
  log "ROLLBACK FAILED — the previous revision did not come up either; escalate (docs/runbooks/GO_LIVE_RUNBOOK.md §9)"
  return 1
}

write_state() {
  local tmp="${STATE_FILE}.tmp.$$"
  mkdir -p "$(dirname "${STATE_FILE}")"
  {
    printf 'tag=%s\n' "${TAG}"
    printf 'image=%s\n' "${NEW_IMAGE}"
    printf 'previous_image=%s\n' "${PREV_IMAGE:-}"
    printf 'deployed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'deployed_by=%s\n' "${GITHUB_ACTOR:-${USER:-unknown}}"
    printf 'host=%s\n' "$(hostname 2>/dev/null || echo unknown)"
  } >"${tmp}"
  mv "${tmp}" "${STATE_FILE}"
}

# --------------------------------------------------------------- preflight --

has_service app || die "no 'app' service in '${COMPOSE_FILES}'; check COMPOSE_FILES"

NEW_IMAGE="${APP_IMAGE_REPO}:${TAG}"

PREV_IMAGE="$(running_app_image || true)"
[ -n "${PREV_IMAGE}" ] || PREV_IMAGE="$(state_value previous_image || true)"
[ -n "${PREV_IMAGE}" ] || PREV_IMAGE="$(env_file_value APP_IMAGE || true)"

log "release         ${TAG}"
log "new image       ${NEW_IMAGE}"
log "current image   ${PREV_IMAGE:-<none running>}"
log "compose files   ${COMPOSE_FILES}"

# ----------------------------------------------------------------- verify ---
# Opt-in signature gate (RELEASE_VERIFY_SIGNATURE=1). Resolves the digest for
# the tag and verifies the keyless cosign signature by that digest, so a moved
# or unsigned tag cannot be rolled out even though the pipeline verifies the
# images it itself publishes.
verify_signature() {
  [ "${RELEASE_VERIFY_SIGNATURE:-0}" = "1" ] || return 0
  command -v cosign >/dev/null 2>&1 || die "RELEASE_VERIFY_SIGNATURE=1 but cosign is not on PATH"
  if ! docker buildx version >/dev/null 2>&1; then
    die "RELEASE_VERIFY_SIGNATURE=1 but docker buildx is not available to resolve the digest"
  fi
  local identity="${COSIGN_IDENTITY_REGEXP:-^https://github.com/ekearua/easymoney/.github/workflows/release.yml@refs/tags/v.*\$}"
  local issuer="${COSIGN_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
  local digest
  digest="$(docker buildx imagetools inspect "${NEW_IMAGE}" --format '{{.Manifest.Digest}}' 2>/dev/null || true)"
  [ -n "${digest}" ] || die "RELEASE_VERIFY_SIGNATURE=1 but no digest resolvable for ${NEW_IMAGE}"
  log "verifying cosign signature: ${NEW_IMAGE}@${digest}"
  cosign verify \
    --certificate-identity-regexp "${identity}" \
    --certificate-oidc-issuer "${issuer}" \
    "${APP_IMAGE_REPO}@${digest}" >/dev/null 2>&1 \
    || die "cosign verification FAILED for ${NEW_IMAGE}@${digest}; refusing to deploy"
  log "signature ok"
}

# --------------------------------------------------------------------- pull --

if [ -n "${APP_IMAGE_TOKEN:-}" ]; then
  registry="${APP_IMAGE_REGISTRY:-ghcr.io}"
  log "registry login  ${registry}"
  printf '%s' "${APP_IMAGE_TOKEN}" \
    | docker login "${registry}" -u "${APP_IMAGE_USER:-${GITHUB_ACTOR:-token}}" --password-stdin \
    || die "registry login failed"
elif case "${APP_IMAGE_REPO}" in ghcr.io/*) true ;; *) false ;; esac; then
  # GHCR packages are private by default, and a host's docker auth may not have
  # a token yet. Warn up front so a missing APP_IMAGE_TOKEN is found here rather
  # than in the pull error below (which still aborts before anything changes).
  log "WARN: ${APP_IMAGE_REPO} is a GHCR repo and APP_IMAGE_TOKEN is unset; pull will fail if the package is private and the host has no docker login"
fi

pull_targets=()
if has_service app; then pull_targets+=(app); fi
if has_service migrate; then pull_targets+=(migrate); fi

# Pin the image for every compose call that follows: `pull` must fetch the
# released tag (not whatever .env last recorded), and `run`/`up` must use it
# rather than rebuilding or running the previous revision.
export APP_IMAGE="${NEW_IMAGE}"

verify_signature

log "pulling ${pull_targets[*]}"
if ! compose pull "${pull_targets[@]}"; then
  if [ -z "${APP_IMAGE_TOKEN:-}" ]; then
    die "image pull failed for ${NEW_IMAGE}; nothing on the host changed (GHCR packages are private by default — set APP_IMAGE_TOKEN in the host .env if the package is private)"
  fi
  die "image pull failed for ${NEW_IMAGE}; nothing on the host changed"
fi

# ------------------------------------------------------------------- backup --

if has_service backup && [ "${RELEASE_SKIP_BACKUP:-0}" != "1" ]; then
  log "pre-migrate base backup"
  if ! compose run --rm backup; then
    die "pre-migrate backup failed; aborting before any schema change"
  fi
  log "backup ok"
elif [ "${RELEASE_SKIP_BACKUP:-0}" = "1" ]; then
  log "WARN: RELEASE_SKIP_BACKUP=1 — no pre-migrate backup taken"
else
  log "no backup service in this stack — relying on the primary's backup pipeline"
fi

# ------------------------------------------------------------------ migrate --

if has_service migrate; then
  log "applying migrations"
  if ! compose run --rm migrate; then
    log "MIGRATION FAILED — app left untouched on ${PREV_IMAGE:-its current image}"
    log "Schema is forward-only; if the migration is destructive, recover with PITR (docs/runbooks/GO_LIVE_RUNBOOK.md §9)."
    exit 1
  fi
  log "migrations ok"
else
  log "no migrate service in this stack (edge host) — schema is owned by the primary"
fi

# ------------------------------------------------------------------- deploy --

if recreate_and_gate "${NEW_IMAGE}"; then
  write_state
  log "release ${TAG} complete (image ${NEW_IMAGE})"
  exit 0
fi

log "readiness gate FAILED for ${NEW_IMAGE}"
rollback || true
log "release ${TAG} failed; see the rollback result above"
exit 1
