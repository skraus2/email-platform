#!/usr/bin/env bash
# TypeScript client against real processes: builds the binaries, starts API
# and worker against the local Compose database and Mailpit, creates a
# project, a key and a webhook endpoint through the admin CLI, and runs the
# client's integration tests (send, replay, get, events, list across pages,
# API errors, and a real webhook delivery verified by the client; the test
# process is the webhook endpoint). Then sends SIGTERM, requires a clean
# shutdown and checks that the logs carry no key, secret or recipient.
# Needs `make up migrate` first, and Node (the version in .nvmrc).
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
# A run-local key-encryption key for the endpoint's secret.
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
ADDR="127.0.0.1:${SDK_IT_PORT:-18180}"
RECV_ADDR="127.0.0.1:${SDK_IT_RECV_PORT:-18190}"
NODE="${NODE:-node}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
cleanup() {
  [[ -n "${key:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$key")" >/dev/null 2>&1 || true
  # Pending deliveries of this run must not reach whatever binds the port later.
  [[ -n "${endpoint:-}" ]] && "$TMP/admin" webhook disable -project "$project" -id "$endpoint" >/dev/null 2>&1 || true
  kill "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

"$NODE" -e 'const [major] = process.versions.node.split(".").map(Number); if (major < 24) { console.error(`Node ${process.versions.node} is too old; the client needs the version in .nvmrc`); process.exit(1); }'
go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin

HTTP_ADDR="$ADDR" ADMIN_HTTP_ADDR=off LOG_FORMAT=json "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
LOG_FORMAT=json "$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!

# Logs are printed on failure with the run's secrets filtered out.
redact() { if [[ -s "$TMP/needles" ]]; then grep -viFf "$TMP/needles" "$1" || true; else cat "$1"; fi; }
fail() {
  echo "SDK INTEGRATION FAIL: $*" >&2
  for f in api worker; do echo "--- $f.log" >&2; redact "$TMP/$f.log" >&2; done
  exit 1
}

for _ in $(seq 1 50); do
  curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 && grep -q '"worker ready"' "$TMP/worker.log" && break
  sleep 0.1
done
# The key goes only to an API this script started: another process on the
# port would also answer /readyz.
kill -0 "$API_PID" 2>/dev/null || fail "api exited (port $ADDR taken?)"
curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 || fail "api not ready (did you run make migrate?)"
grep -q '"worker ready"' "$TMP/worker.log" || fail "worker did not become ready"
echo "api + worker -> ready"

run="$(date +%s)-$RANDOM"
project=$("$TMP/admin" project create -name "sdk-it-$run" | cut -f1) || fail "admin project create"
key=$("$TMP/admin" key create -project "$project" 2>"$TMP/admin.err") || fail "admin key create: $(cat "$TMP/admin.err")"
[[ "$key" == ep_* ]] || fail "admin did not issue a key"
# Needles from a file: secrets must not appear in grep's command line.
umask 077
printf '%s\n' "$key" "sdk-it-$run@example.test" "sdk integration body" "sdk-it-$run:" >"$TMP/needles"
tr ',' '\n' <<<"$WEBHOOK_KEKS" | sed 's/^[^:]*://' >>"$TMP/needles"

"$TMP/admin" webhook create -project "$project" -url "http://$RECV_ADDR/hooks" >"$TMP/webhook.out" 2>"$TMP/admin.err" ||
  fail "admin webhook create: $(cat "$TMP/admin.err")"
endpoint=$(head -1 "$TMP/webhook.out" | cut -f1)
secret=$(sed -nE 's/^secret \(shown once, store it now\): (whsec_[A-Za-z0-9_-]+)$/\1/p' "$TMP/webhook.out")
[[ -n "$secret" ]] || fail "admin webhook create printed no secret"
printf '%s\n' "$secret" >>"$TMP/needles"
echo "project, key and webhook endpoint for http://$RECV_ADDR/hooks -> created"

# Credentials go through the environment of the test process, never argv;
# it gets neither the database nor the key-encryption key.
env -u WEBHOOK_KEKS -u DATABASE_URL -u TEST_DATABASE_URL \
  EP_BASE_URL="http://$ADDR" EP_API_KEY="$key" EP_WEBHOOK_SECRET="$secret" EP_WEBHOOK_ADDR="$RECV_ADDR" EP_RUN="$run" \
  "$NODE" --test --test-reporter=spec 'packages/sdk-typescript/integration/**/*.test.ts' ||
  fail "client integration tests failed"

kill -TERM "$API_PID" "$WORKER_PID"
wait "$API_PID" || fail "api exited non-zero after SIGTERM"
wait "$WORKER_PID" || fail "worker exited non-zero after SIGTERM"
grep -q '"shutdown complete"' "$TMP/api.log" || fail "api did not log shutdown complete"
grep -q '"shutdown complete"' "$TMP/worker.log" || fail "worker did not log shutdown complete"
echo "SIGTERM -> api and worker shut down cleanly"

# After shutdown, so that lines written while stopping are checked too.
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then
  echo "SDK INTEGRATION FAIL: logs contain a key, webhook secret, KEK, idempotency key, recipient or body" >&2; exit 1
fi
echo "logs contain no API key, webhook secret or KEK, idempotency key, recipient or body"
if grep -q 'email_platform:email_platform@' "$TMP/api.log" "$TMP/worker.log"; then
  fail "database password appeared in logs"
fi
echo "SDK INTEGRATION OK"
