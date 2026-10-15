#!/usr/bin/env bash
# Process-level smoke test: builds the binaries, starts API and worker against
# the local Compose database and Mailpit, probes /healthz and /readyz, sends
# one email end to end (admin CLI -> POST -> worker -> Mailpit), checks that
# parallel requests with one idempotency key produce one message, that the
# email's event history shows `captured` and a local receiver gets exactly
# one correctly signed `email.captured` webhook, that a suppressed recipient
# is refused, that a second API requiring verified senders refuses an
# unverified one (422) and accepts it once verified, and that the project
# rate limit answers 429, then sends SIGTERM and requires a clean shutdown.
# Needs `make up migrate` first.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
# The worker needs a key-encryption key for webhook secrets; a run-local one
# is generated unless the environment provides one.
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
ADDR="127.0.0.1:${SMOKE_PORT:-18080}"
# A second API that requires verified senders, and the webhook receiver.
ADDR2="127.0.0.1:${SMOKE_PORT2:-18081}"
RECV_ADDR="127.0.0.1:${SMOKE_RECV_PORT:-18090}"
# The first API also serves the admin API; the second has it off.
ADMIN_ADDR="127.0.0.1:${SMOKE_ADMIN_PORT:-18082}"
TMP="$(mktemp -d)"
cleanup() {
  [[ -n "${key:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$key")" >/dev/null 2>&1 || true
  # Pending deliveries of this run must not reach whatever binds the port later.
  [[ -s "$TMP/webhook.out" ]] && "$TMP/admin" webhook disable -project "$project" -id "$(head -1 "$TMP/webhook.out" | cut -f1)" >/dev/null 2>&1 || true
  kill "${API_PID:-}" "${API2_PID:-}" "${WORKER_PID:-}" "${RECV_PID:-}" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

MAILPIT_URL="${TEST_MAILPIT_URL:-http://127.0.0.1:8025}"
go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin
go build -tags smoke -o "$TMP/" ./cmd/webhookrecv

# A low rate limit so that the smoke run can reach it.
HTTP_ADDR="$ADDR" ADMIN_HTTP_ADDR="$ADMIN_ADDR" LOG_FORMAT=json API_RATE_LIMIT_PER_MINUTE=20 "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
HTTP_ADDR="$ADDR2" ADMIN_HTTP_ADDR=off LOG_FORMAT=json REQUIRE_VERIFIED_SENDER=true "$TMP/api" >"$TMP/api2.log" 2>&1 &
API2_PID=$!
LOG_FORMAT=json "$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!
env -u WEBHOOK_KEKS WEBHOOK_SECRET_FILE="$TMP/whsec" "$TMP/webhookrecv" -addr "$RECV_ADDR" -out "$TMP/hooks.jsonl" >"$TMP/recv.log" 2>&1 &
RECV_PID=$!

# Logs are printed on failure with the run's secrets filtered out (the
# needles file is written as soon as the key exists).
redact() { if [[ -s "$TMP/needles" ]]; then grep -viFf "$TMP/needles" "$1" || true; else cat "$1"; fi; }
fail() {
  echo "SMOKE FAIL: $*" >&2
  for f in api api2 worker recv; do echo "--- $f.log" >&2; redact "$TMP/$f.log" >&2; done
  exit 1
}

for _ in $(seq 1 50); do
  curl -fsS "http://$ADDR/healthz" >/dev/null 2>&1 && curl -fsS "http://$ADDR2/healthz" >/dev/null 2>&1 && break
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
# Needles from a file: the key must not appear in grep's command line.
printf '%s\n' "$key" "smoke-recipient@example.test" "blocked@example.test" "smoke body" "smoke-par-" >"$TMP/needles"
tr ',' '\n' <<<"$WEBHOOK_KEKS" | sed 's/^[^:]*://' >>"$TMP/needles"
# Pass the key via a file, not the command line (visible in ps).
umask 077
printf 'Authorization: Bearer %s\n' "$key" >"$TMP/auth.header"
# The admin API is on its own listener: no session or a project key is
# refused there, and its routes do not exist on the public listener.
admin_code() { curl -sS -o /dev/null -w '%{http_code}' "$@"; }
[[ "$(admin_code "http://$ADMIN_ADDR/admin/v1/me")" == 401 ]] || fail "admin /me without a session is not 401"
[[ "$(admin_code -H @"$TMP/auth.header" "http://$ADMIN_ADDR/admin/v1/me")" == 401 ]] || fail "admin /me with a project key is not 401"
[[ "$(admin_code -H @"$TMP/auth.header" "http://$ADMIN_ADDR/v1/emails")" == 404 ]] || fail "/v1 is routed on the admin listener"
[[ "$(admin_code "http://$ADDR/admin/v1/me")" == 404 ]] || fail "admin routes exist on the public listener"
echo "admin API -> separate listener, 401 without session and with a project key"

# The internal app's webhook endpoint; the secret is shown once and handed to
# the receiver through a file.
"$TMP/admin" webhook create -project "$project" -url "http://$RECV_ADDR/hooks" >"$TMP/webhook.out" 2>"$TMP/admin.err" ||
  fail "admin webhook create: $(cat "$TMP/admin.err")"
sed -nE 's/^secret \(shown once, store it now\): (whsec_[A-Za-z0-9_-]+)$/\1/p' "$TMP/webhook.out" >"$TMP/whsec"
[[ -s "$TMP/whsec" ]] || fail "admin webhook create printed no secret"
cat "$TMP/whsec" >>"$TMP/needles"
echo "admin webhook create -> endpoint for http://$RECV_ADDR/hooks"
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

# The event history and the signed webhook for the same email.
events=$(curl -sS "http://$ADDR/v1/emails/$id/events" -H @"$TMP/auth.header")
[[ "$events" == *'"type":"captured"'* && "$events" == *'"origin":"platform"'* ]] || fail "events: $events"
echo "GET /v1/emails/$id/events -> captured"
hooks=0
for _ in $(seq 1 100); do
  hooks=$(grep -c "\"email_id\":\"$id\"" "$TMP/hooks.jsonl" 2>/dev/null || true)
  [[ "$hooks" -ge 1 ]] && break
  sleep 0.1
done
[[ "$hooks" == 1 ]] || fail "receiver got $hooks webhooks for $id, want 1"
grep -q "\"type\":\"email.captured\",\"id\":\"[0-9a-f-]\{36\}\",\"email_id\":\"$id\",\"valid\":true" "$TMP/hooks.jsonl" ||
  fail "webhook not a valid email.captured: $(grep "$id" "$TMP/hooks.jsonl")"
echo "receiver -> 1 email.captured webhook, signature valid"

# Parallel requests with one idempotency key: one email, one message.
post_keyed() {
  curl -sS -o "$TMP/par.$1.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" \
    -H @"$TMP/auth.header" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-par-$run" \
    -d "{\"from\":\"smoke@example.test\",\"to\":\"smoke-recipient@example.test\",\"subject\":\"parallel $run\",\"text\":\"smoke body\"}" >"$TMP/par.$1.code"
}
run="$(date +%s)-$RANDOM"
par_pids=()
for i in $(seq 1 8); do post_keyed "$i" & par_pids+=($!); done
wait "${par_pids[@]}" || true # each request's status is checked below
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
[[ "$count" == 1 ]] || fail "parallel same-key requests: Mailpit holds $count copies, want 1"
par_state=$(curl -sS "http://$ADDR/v1/emails/$par_ids" -H @"$TMP/auth.header")
for _ in $(seq 1 50); do
  [[ "$par_state" == *'"status":"captured"'* ]] && break
  sleep 0.1
  par_state=$(curl -sS "http://$ADDR/v1/emails/$par_ids" -H @"$TMP/auth.header")
done
[[ "$par_state" == *'"status":"captured"'* && "$par_state" == *'"attempts":1,'* ]] || fail "parallel email not captured in one attempt: $par_state"
echo "8 parallel POSTs with one Idempotency-Key -> 1 email, 1 message in Mailpit"

# A suppressed recipient is refused.
"$TMP/admin" suppression add -project "$project" -email blocked@example.test -reason manual >/dev/null || fail "admin suppression add"
code=$(curl -sS -o "$TMP/supp.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" -H @"$TMP/auth.header" \
  -H 'Content-Type: application/json' -d '{"from":"smoke@example.test","to":"Blocked@Example.test","subject":"s","text":"t"}')
[[ "$code" == 422 ]] && grep -q '"recipient_suppressed"' "$TMP/supp.json" || fail "suppressed recipient: $code $(cat "$TMP/supp.json")"
echo "suppressed recipient -> 422 recipient_suppressed"

# With verified senders required, an unverified sender is refused before
# anything is stored; once its identity is verified it may send.
post2() {
  curl -sS -o "$TMP/v.json" -w '%{http_code}' -X POST "http://$ADDR2/v1/emails" -H @"$TMP/auth.header" \
    -H 'Content-Type: application/json' -d '{"from":"verified@example.test","to":"smoke-recipient@example.test","subject":"v","text":"smoke body"}'
}
code=$(post2)
[[ "$code" == 422 ]] && grep -q '"sender_not_verified"' "$TMP/v.json" || fail "unverified sender: $code $(cat "$TMP/v.json")"
echo "unverified sender (verification required) -> 422 sender_not_verified"
sender=$("$TMP/admin" sender add -project "$project" -address verified@example.test | cut -f1) || fail "admin sender add"
"$TMP/admin" sender verify -project "$project" -id "$sender" >/dev/null || fail "admin sender verify"
code=$(post2)
[[ "$code" == 202 ]] || fail "verified sender: $code $(cat "$TMP/v.json")"
echo "verified sender -> 202"

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


kill -TERM "$API_PID" "$API2_PID" "$WORKER_PID" "$RECV_PID"
wait "$API_PID" || fail "api exited non-zero after SIGTERM"
wait "$API2_PID" || fail "second api exited non-zero after SIGTERM"
wait "$RECV_PID" || fail "webhook receiver exited non-zero after SIGTERM"
wait "$WORKER_PID" || fail "worker exited non-zero after SIGTERM"
grep -q '"shutdown complete"' "$TMP/api.log" || fail "api did not log shutdown complete"
grep -q '"shutdown complete"' "$TMP/api2.log" || fail "second api did not log shutdown complete"
grep -q '"shutdown complete"' "$TMP/worker.log" || fail "worker did not log shutdown complete"
echo "SIGTERM -> api, worker and receiver shut down cleanly"
# Exactly one webhook for the first email, also after everything stopped.
[[ $(grep -c "\"email_id\":\"$id\"" "$TMP/hooks.jsonl") == 1 ]] || fail "more than one webhook for $id"

# After shutdown, so that lines written while stopping are checked too.
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/api2.log" "$TMP/worker.log"; then
  echo "SMOKE FAIL: logs contain a secret, idempotency key or personal data" >&2; exit 1
fi
echo "logs contain no API key, webhook secret or KEK, idempotency key, recipient or body"

if grep -q 'email_platform:email_platform@' "$TMP/api.log" "$TMP/api2.log" "$TMP/worker.log"; then
  fail "database password appeared in logs"
fi
echo "SMOKE OK"
