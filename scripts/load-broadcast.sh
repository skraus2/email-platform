#!/usr/bin/env bash
# Broadcast load measurement against the local Compose stack: starts the real
# API and worker binaries, creates a throwaway project and key, and runs
# `loadtest broadcast` (a 50k-recipient broadcast with transactional sends
# alongside, see cmd/loadtest/broadcast.go). Needs `make up migrate`.
# Parameters via LOAD_ARGS, e.g. LOAD_ARGS="-contacts 10000 -cancel 0".
# Limits are raised for the run so that the measurement shows the system,
# not the configured caps.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
# The worker needs a key-encryption key for webhook secrets; a run-local one
# is generated unless the environment provides one. The API runs without
# it (webhook registration through the API off).
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
unset LOADTEST_API_KEY # never revoke a key from the caller's environment
ADDR="127.0.0.1:${LOAD_PORT:-18082}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
API_PID="" WORKER_PID=""
cleanup() {
  # Revoke the run's key instead of leaving an active credential behind.
  [[ -n "${LOADTEST_API_KEY:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$LOADTEST_API_KEY")" >/dev/null 2>&1 || true
  kill "$API_PID" "$WORKER_PID" 2>/dev/null || true
  wait "$API_PID" "$WORKER_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

export LOG_FORMAT=json LOG_LEVEL=info API_RATE_LIMIT_PER_MINUTE=1000000 \
  PROVIDER_RATE_PER_SECOND="${LOAD_PROVIDER_RATE:-1000}" \
  CONTACT_MAX_PER_PROJECT=1000000 AUDIENCE_MAX_CONTACTS=1000000

go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin
go build -tags load -o "$TMP/" ./cmd/loadtest

env -u WEBHOOK_KEKS HTTP_ADDR="$ADDR" "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
"$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!
for _ in $(seq 1 100); do curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fsS "http://$ADDR/readyz" >/dev/null || { cat "$TMP/api.log"; exit 1; }

project=$("$TMP/admin" project create -name "load-$(date +%s)-$RANDOM" | cut -f1)
LOADTEST_API_KEY=$("$TMP/admin" key create -project "$project" \
  -scopes emails:send,emails:read,templates:write,contacts:write,broadcasts:read,broadcasts:write 2>"$TMP/admin.err") ||
  { cat "$TMP/admin.err" >&2; exit 1; }
export LOADTEST_API_KEY

echo "machine: $(uname -sm), $(sysctl -n hw.ncpu 2>/dev/null || nproc) CPUs, provider rate ${PROVIDER_RATE_PER_SECOND}/s, worker concurrency 10"
# shellcheck disable=SC2086 # LOAD_ARGS is a list of flags
"$TMP/loadtest" broadcast -addr "http://$ADDR" -pids "api=$API_PID,worker=$WORKER_PID" ${LOAD_ARGS:-}
if grep -q '"level":"ERROR"' "$TMP/api.log" "$TMP/worker.log"; then
  echo "errors logged during the run:"; grep -h '"level":"ERROR"' "$TMP/api.log" "$TMP/worker.log" | head -5
fi
# Needles from a file: the key must not appear in grep's command line.
# Recipients appear masked (domain kept); the needles are full local parts.
printf '%s\n' "$LOADTEST_API_KEY" "reader-1-" "tx-1@" "Reader 1" "example.test/u?t=" "email_platform:email_platform@" >"$TMP/needles"
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then
  echo "LOAD FAIL: logs contain a secret, address, attribute or unsubscribe link" >&2; exit 1
fi
