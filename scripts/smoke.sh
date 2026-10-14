#!/usr/bin/env bash
# Process-level smoke test: builds the binaries, starts API and worker against
# the local Compose database and Mailpit, probes /healthz and /readyz, sends
# one email end to end (admin CLI -> POST -> worker -> Mailpit), checks that
# parallel requests with one idempotency key produce one message, that a
# suppressed recipient is refused and that the project rate limit answers
# 429, then sends SIGTERM and requires a clean shutdown. Needs `make up
# migrate` first.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
ADDR="127.0.0.1:${SMOKE_PORT:-18080}"
TMP="$(mktemp -d)"
cleanup() {
  [[ -n "${key:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$key")" >/dev/null 2>&1 || true
  kill "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

MAILPIT_URL="${TEST_MAILPIT_URL:-http://127.0.0.1:8025}"
go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin

# A low rate limit so that the smoke run can reach it.
HTTP_ADDR="$ADDR" LOG_FORMAT=json API_RATE_LIMIT_PER_MINUTE=20 "$TMP/api" >"$TMP/api.log" 2>&1 &
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

# One email end to end through the real processes.
project=$("$TMP/admin" project create -name "smoke-$(date +%s)-$RANDOM" | cut -f1) || fail "admin project create"
key=$("$TMP/admin" key create -project "$project" 2>"$TMP/admin.err") || fail "admin key create: $(cat "$TMP/admin.err")"
[[ "$key" == ep_* ]] || fail "admin did not issue a key"
# Pass the key via a file, not the command line (visible in ps).
umask 077
printf 'Authorization: Bearer %s\n' "$key" >"$TMP/auth.header"
subject="smoke $(date +%s)-$RANDOM"
code=$(curl -sS -o "$TMP/post.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" \
  -H @"$TMP/auth.header" -H 'Content-Type: application/json' \
  -d "{\"from\":\"smoke@example.test\",\"to\":\"smoke-recipient@example.test\",\"subject\":\"$subject\",\"text\":\"smoke body\"}")
[[ "$code" == 202 ]] || fail "POST /v1/emails: $code $(cat "$TMP/post.json")"
id=$(sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$TMP/post.json")
[[ -n "$id" ]] || fail "no id in POST response"
echo "POST /v1/emails -> 202 queued ($id)"
status=""
for _ in $(seq 1 100); do
  status=$(curl -sS "http://$ADDR/v1/emails/$id" -H @"$TMP/auth.header" | sed -E 's/.*"status":"([^"]+)".*/\1/')
  [[ "$status" == captured || "$status" == failed ]] && break
  sleep 0.1
done
[[ "$status" == captured ]] || fail "email status $status, want captured"
count=0
for _ in $(seq 1 30); do
  count=$(curl -sS "$MAILPIT_URL/api/v1/search?query=message-id:%22$id@email-platform.local%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/')
  [[ "$count" -ge 1 ]] && break
  sleep 0.1
done
[[ "$count" == 1 ]] || fail "Mailpit holds $count copies, want 1"
echo "worker -> Mailpit captured exactly 1 message"

# Parallel requests with one idempotency key: one email, one message.
post_keyed() {
  curl -sS -o "$TMP/par.$1.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" \
    -H @"$TMP/auth.header" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-par-$run" \
    -d "{\"from\":\"smoke@example.test\",\"to\":\"smoke-recipient@example.test\",\"subject\":\"parallel $run\",\"text\":\"smoke body\"}" >"$TMP/par.$1.code"
}
run="$(date +%s)-$RANDOM"
par_pids=()
for i in $(seq 1 8); do post_keyed "$i" & par_pids+=($!); done
wait "${par_pids[@]}"
par_ids=$(for i in $(seq 1 8); do
  [[ $(cat "$TMP/par.$i.code") == 202 ]] || fail "parallel POST $i: $(cat "$TMP/par.$i.code")"
  sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$TMP/par.$i.json"
done | sort -u)
[[ $(wc -l <<<"$par_ids" | tr -d ' ') == 1 ]] || fail "parallel same-key requests produced several emails: $par_ids"
count=0
for _ in $(seq 1 50); do
  count=$(curl -sS "$MAILPIT_URL/api/v1/search?query=message-id:%22$par_ids@email-platform.local%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/')
  [[ "$count" -ge 1 ]] && break
  sleep 0.1
done
sleep 0.5
count=$(curl -sS "$MAILPIT_URL/api/v1/search?query=message-id:%22$par_ids@email-platform.local%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/')
[[ "$count" == 1 ]] || fail "parallel same-key requests: Mailpit holds $count copies, want 1"
echo "8 parallel POSTs with one Idempotency-Key -> 1 email, 1 message in Mailpit"

# A suppressed recipient is refused.
"$TMP/admin" suppression add -project "$project" -email blocked@example.test -reason manual >/dev/null || fail "admin suppression add"
code=$(curl -sS -o "$TMP/supp.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" -H @"$TMP/auth.header" \
  -H 'Content-Type: application/json' -d '{"from":"smoke@example.test","to":"Blocked@Example.test","subject":"s","text":"t"}')
[[ "$code" == 422 ]] && grep -q '"recipient_suppressed"' "$TMP/supp.json" || fail "suppressed recipient: $code $(cat "$TMP/supp.json")"
echo "suppressed recipient -> 422 recipient_suppressed"

# The project rate limit (20/min for this run) answers 429 with Retry-After.
limited=""
for _ in $(seq 1 40); do
  code=$(curl -sS -D "$TMP/rl.headers" -o /dev/null -w '%{http_code}' -X POST "http://$ADDR/v1/emails" -H @"$TMP/auth.header" \
    -H 'Content-Type: application/json' -d '{"from":"smoke@example.test","to":"smoke-recipient@example.test","subject":"rate","text":"smoke body"}')
  if [[ "$code" == 429 ]]; then limited=1; break; fi
  [[ "$code" == 202 ]] || fail "rate limit loop: $code"
done
[[ -n "$limited" ]] || fail "no 429 within 40 sends at a limit of 20/min"
grep -qiE '^retry-after: [0-9]+' "$TMP/rl.headers" || fail "429 without Retry-After"
echo "project rate limit -> 429 with $(grep -i '^retry-after' "$TMP/rl.headers" | tr -d '\r')"

# Needles from a file: the key must not appear in grep's command line.
printf '%s\n' "$key" "smoke-recipient@example.test" "blocked@example.test" "smoke body" "smoke-par-" >"$TMP/needles"
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then fail "logs contain a secret, idempotency key or personal data"; fi
echo "logs contain no API key, idempotency key, recipient or body"

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
