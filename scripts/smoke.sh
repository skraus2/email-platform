#!/usr/bin/env bash
# Process-level smoke test: builds the binaries, starts API and worker against
# the local Compose database, probes /healthz and /readyz, then sends SIGTERM
# and requires a clean shutdown. Needs `make up migrate` first.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
ADDR="127.0.0.1:${SMOKE_PORT:-18080}"
TMP="$(mktemp -d)"
trap 'kill "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true; rm -rf "$TMP"' EXIT

go build -o "$TMP/" ./cmd/api ./cmd/worker

HTTP_ADDR="$ADDR" LOG_FORMAT=json "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
LOG_FORMAT=json "$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!

fail() { echo "SMOKE FAIL: $*" >&2; echo "--- api.log" >&2; cat "$TMP/api.log" >&2; echo "--- worker.log" >&2; cat "$TMP/worker.log" >&2; exit 1; }

for _ in $(seq 1 50); do
  curl -fsS "http://$ADDR/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
health=$(curl -sS -w ' %{http_code}' "http://$ADDR/healthz") || fail "healthz unreachable"
ready=$(curl -sS -w ' %{http_code}' "http://$ADDR/readyz") || fail "readyz unreachable"
echo "GET /healthz -> $health"
echo "GET /readyz  -> $ready"
[[ "$health" == *' 200' ]] || fail "healthz not 200"
[[ "$ready" == *' 200' ]] || fail "readyz not 200 (did you run make migrate?)"

for _ in $(seq 1 50); do
  grep -q '"worker ready"' "$TMP/worker.log" && break
  sleep 0.1
done
grep -q '"worker ready"' "$TMP/worker.log" || fail "worker did not become ready"
echo "worker -> ready"

kill -TERM "$API_PID" "$WORKER_PID"
wait "$API_PID" || fail "api exited non-zero after SIGTERM"
wait "$WORKER_PID" || fail "worker exited non-zero after SIGTERM"
grep -q '"shutdown complete"' "$TMP/api.log" || fail "api did not log shutdown complete"
grep -q '"shutdown complete"' "$TMP/worker.log" || fail "worker did not log shutdown complete"
echo "SIGTERM -> api and worker shut down cleanly"

if grep -q 'email_platform:email_platform@' "$TMP/api.log" "$TMP/worker.log"; then
  fail "database password appeared in logs"
fi
echo "SMOKE OK"
