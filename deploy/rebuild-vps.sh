#!/usr/bin/env bash
# Rebuild and restart the Xego demo service on the Oracle Ubuntu VPS.
#
# Safe defaults match the current non-Docker VPS setup:
#   repo:    /tmp/whatsapp-payment-build
#   service: whatsapp-payment
#   binary:  /opt/whatsapp-payment/whatsapp-payment-demo
#   env:     /etc/whatsapp-payment.env
#
# Optional overrides:
#   REPO_DIR=/path/to/repo SERVICE_NAME=whatsapp-payment ./deploy/rebuild-vps.sh
#   SKIP_PULL=1 RUN_TESTS=0 RUN_VET=0 RUN_MIGRATIONS=0 SHOW_LOGS=0 bash deploy/rebuild-vps.sh

set -Eeuo pipefail

REPO_DIR="${REPO_DIR:-/tmp/whatsapp-payment-build}"
SERVICE_NAME="${SERVICE_NAME:-whatsapp-payment}"
APP_USER="${APP_USER:-whatsapp-payment}"
APP_GROUP="${APP_GROUP:-whatsapp-payment}"
INSTALL_DIR="${INSTALL_DIR:-/opt/whatsapp-payment}"
BINARY_NAME="${BINARY_NAME:-whatsapp-payment-demo}"
ENV_FILE="${ENV_FILE:-/etc/whatsapp-payment.env}"
BUILD_OUT="${BUILD_OUT:-${REPO_DIR}/${BINARY_NAME}}"
LOG_LINES="${LOG_LINES:-120}"

SKIP_PULL="${SKIP_PULL:-0}"
RUN_GO_MOD_DOWNLOAD="${RUN_GO_MOD_DOWNLOAD:-1}"
RUN_TESTS="${RUN_TESTS:-1}"
RUN_VET="${RUN_VET:-1}"
RUN_MIGRATIONS="${RUN_MIGRATIONS:-1}"
SHOW_LOGS="${SHOW_LOGS:-1}"

installed_binary="${INSTALL_DIR}/${BINARY_NAME}"

on_error() {
  local line="$1"
  echo
  echo "Rebuild failed around line ${line}."
  echo "Check the error above. The installed service was not intentionally rolled back."
}
trap 'on_error "$LINENO"' ERR

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1"
    exit 1
  fi
}

run_migrate_with_env() {
  if [[ ! -f "$ENV_FILE" ]]; then
    echo "Env file not found: $ENV_FILE"
    exit 1
  fi

  # Prefer running as the service account when it can read the env file.
  # This avoids fragile commands like: env $(grep ... | xargs), which break
  # when values contain spaces or shell-sensitive characters.
  if sudo -n -u "$APP_USER" test -r "$ENV_FILE" 2>/dev/null; then
    sudo -u "$APP_USER" bash -c '
      set -Eeuo pipefail
      set -a
      source "$1"
      set +a
      exec "$2" migrate
    ' _ "$ENV_FILE" "$installed_binary"
    return
  fi

  echo "Service user cannot read $ENV_FILE directly; sourcing it through sudo root shell."
  sudo bash -c '
    set -Eeuo pipefail
    set -a
    source "$1"
    set +a
    exec sudo -E -u "$2" "$3" migrate
  ' _ "$ENV_FILE" "$APP_USER" "$installed_binary"
}

echo "== Xego VPS rebuild =="
echo "Repo:        $REPO_DIR"
echo "Service:     $SERVICE_NAME"
echo "Install bin: $installed_binary"
echo

require_command git
require_command go
require_command sudo
require_command systemctl

cd "$REPO_DIR"

if [[ ! -f go.mod || ! -d cmd/demo ]]; then
  echo "This does not look like the Xego Go repo: $REPO_DIR"
  exit 1
fi

if [[ "$SKIP_PULL" != "1" ]]; then
  echo "== Pulling latest code =="
  git pull --ff-only
fi

if [[ "$RUN_GO_MOD_DOWNLOAD" == "1" ]]; then
  echo "== Downloading Go modules =="
  go mod download
fi

if [[ "$RUN_TESTS" == "1" ]]; then
  echo "== Running tests =="
  go test -p 1 -count=1 ./...
fi

if [[ "$RUN_VET" == "1" ]]; then
  echo "== Running go vet =="
  go vet ./...
fi

echo "== Building binary =="
CGO_ENABLED=0 go build \
  -buildvcs=false \
  -trimpath \
  -ldflags="-s -w" \
  -o "$BUILD_OUT" \
  ./cmd/demo

echo "== Installing binary =="
sudo install -o root -g "$APP_GROUP" -m 0750 "$BUILD_OUT" "$installed_binary"

if [[ "$RUN_MIGRATIONS" == "1" ]]; then
  echo "== Running migrations =="
  run_migrate_with_env
fi

echo "== Restarting service =="
sudo systemctl restart "$SERVICE_NAME"

echo "== Service status =="
sudo systemctl --no-pager --full status "$SERVICE_NAME" || true

if [[ "$SHOW_LOGS" == "1" ]]; then
  echo "== Recent logs =="
  sudo journalctl -u "$SERVICE_NAME" -n "$LOG_LINES" --no-pager
fi

echo
echo "Rebuild complete."
