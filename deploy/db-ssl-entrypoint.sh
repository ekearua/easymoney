#!/bin/sh
# Xego PostgreSQL entrypoint wrapper: ensures a self-signed certificate exists
# and then hands over to the official docker-entrypoint.sh with SSL enabled.
set -eu

CERT_DIR=/etc/postgresql/ssl
CERT_FILE="$CERT_DIR/server.crt"
KEY_FILE="$CERT_DIR/server.key"

if [ ! -s "$CERT_FILE" ] || [ ! -s "$KEY_FILE" ]; then
  mkdir -p "$CERT_DIR"
  openssl req -new -x509 -days 3650 -nodes \
    -out "$CERT_FILE" -keyout "$KEY_FILE" \
    -subj "/CN=db" >/dev/null 2>&1
  chown postgres:postgres "$CERT_DIR" "$CERT_FILE" "$KEY_FILE"
  chmod 0600 "$KEY_FILE"
fi

exec /usr/local/bin/docker-entrypoint.sh "$@"
