#!/usr/bin/env bash
# Process-level resilience test against the local Compose stack: runs the
# real API and worker binaries and checks that sending survives
#   (a) a Mailpit outage,
#   (b) the worker being killed with SIGKILL mid-batch,
#   (c) a PostgreSQL restart while API and worker run.
# Needs `make up migrate`. Only local containers are stopped and started; no
# volume or data is removed. Takes a few minutes.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
# The worker needs a key-encryption key for webhook secrets; a run-local one
# is generated unless the environment provides one. The API runs without
# it (webhook registration through the API off).
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
ADDR="127.0.0.1:${RESILIENCE_PORT:-18081}"
MAILPIT_URL="${TEST_MAILPIT_URL:-http://127.0.0.1:8025}"
TMP="$(mktemp -d)"
API_PID="" WORKER_PID=""

cleanup() {
  # The run's key is useless afterwards; revoke it instead of leaving an
  # active credential behind.
  [[ -n "${key:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$key")" >/dev/null 2>&1 || true
  kill "$API_PID" "$WORKER_PID" 2>/dev/null || true
  wait "$API_PID" "$WORKER_PID" 2>/dev/null || true
  # Never leave the shared infrastructure stopped.
  docker compose start mailpit postgres >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

fail() {
  echo "RESILIENCE FAIL: $*" >&2
  echo "--- api.log (tail)" >&2; tail -n 30 "$TMP/api.log" >&2 || true
  echo "--- worker.log (tail)" >&2; tail -n 30 "$TMP/worker.log" >&2 || true
  exit 1
}

# Short retries and rescue so the scenarios finish in minutes.
export LOG_FORMAT=json SEND_MAX_ATTEMPTS=20 RETRY_BASE_DELAY=1s RETRY_MAX_DELAY=5s \
  PROVIDER_TIMEOUT=2s JOB_RESCUE_AFTER=65s API_RATE_LIMIT_PER_MINUTE=100000 \
  PROVIDER_RATE_PER_SECOND=4

go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin

start_api() {
  env -u WEBHOOK_KEKS HTTP_ADDR="$ADDR" "$TMP/api" >>"$TMP/api.log" 2>&1 &
  API_PID=$!
}
start_worker() {
  "$TMP/worker" >>"$TMP/worker.log" 2>&1 &
  WORKER_PID=$!
  for _ in $(seq 1 100); do
    [[ $(grep -c '"worker ready"' "$TMP/worker.log") -ge $1 ]] && return
    sleep 0.1
  done
  fail "worker did not become ready"
}

start_api
for _ in $(seq 1 50); do curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 && break; sleep 0.1; done
start_worker 1

project=$("$TMP/admin" project create -name "resilience-$(date +%s)-$RANDOM" | cut -f1) || fail "admin project create"
key=$("$TMP/admin" key create -project "$project" 2>/dev/null) || fail "admin key create"
umask 077
printf 'Authorization: Bearer %s\n' "$key" >"$TMP/auth.header"

# send N PREFIX: POSTs N emails with idempotency keys, appends ids to $TMP/ids.PREFIX.
send() {
  local n=$1 prefix=$2 code
  for i in $(seq 1 "$n"); do
    code=$(curl -sS -o "$TMP/post.json" -w '%{http_code}' -X POST "http://$ADDR/v1/emails" \
      -H @"$TMP/auth.header" -H 'Content-Type: application/json' -H "Idempotency-Key: reskey-$prefix-$i-$RANDOM" \
      -d "{\"from\":\"res@example.test\",\"to\":\"res-recipient@example.test\",\"subject\":\"resilience $prefix $i\",\"text\":\"resilience body\"}")
    [[ "$code" == 202 ]] || fail "POST in $prefix: $code $(cat "$TMP/post.json")"
    sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$TMP/post.json" >>"$TMP/ids.$prefix"
  done
}

status_of() { curl -sS "http://$ADDR/v1/emails/$1" -H @"$TMP/auth.header" | sed -E 's/.*"status":"([^"]+)".*/\1/'; }

# wait_captured PREFIX SECONDS
wait_captured() {
  local prefix=$1 deadline=$((SECONDS + $2)) pending
  while :; do
    pending=0
    while read -r id; do
      [[ $(status_of "$id") == captured ]] || pending=$((pending + 1))
    done <"$TMP/ids.$prefix"
    [[ $pending -eq 0 ]] && return
    [[ $SECONDS -ge $deadline ]] && fail "$prefix: $pending emails not captured within $2s"
    sleep 1
  done
}

copies() { curl -sS "$MAILPIT_URL/api/v1/search?query=message-id:%22$1@email-platform.local%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/'; }

wait_mailpit() {
  for _ in $(seq 1 100); do curl -fsS "$MAILPIT_URL/api/v1/info" >/dev/null 2>&1 && return; sleep 0.2; done
  fail "Mailpit not reachable"
}

# (a) Mailpit outage --------------------------------------------------------
docker compose stop mailpit >/dev/null
send 5 outage
sleep 4
retrying=$(curl -sS "http://$ADDR/v1/emails/$(head -1 "$TMP/ids.outage")" -H @"$TMP/auth.header")
[[ "$retrying" == *'"error_code":"provider_unreachable"'* ]] || fail "outage: email not marked provider_unreachable while retrying: $retrying"
docker compose start mailpit >/dev/null
wait_mailpit
wait_captured outage 90
while read -r id; do [[ $(copies "$id") == 1 ]] || fail "outage: $id has $(copies "$id") copies"; done <"$TMP/ids.outage"
echo "(a) Mailpit outage: 5/5 retried with provider_unreachable and captured once after recovery"

# (b) worker SIGKILL mid-batch ------------------------------------------------
# The provider rate (4/s) keeps jobs running while the batch drains, so the
# kill hits jobs in flight (mostly waiting for a provider token, sometimes
# mid-send); they stay "running" until JOB_RESCUE_AFTER. psql runs inside the
# Compose container: local-guard.sh ensures DATABASE_URL is that database.
send 30 kill
running=0
for _ in $(seq 1 50); do
  running=$(docker compose exec -T postgres psql -U email_platform -d email_platform -Atc \
    "SELECT count(*) FROM river_job WHERE kind = 'send_email' AND state = 'running'")
  [[ "$running" -ge 1 ]] && break
  sleep 0.05
done
kill -KILL "$WORKER_PID"
wait "$WORKER_PID" 2>/dev/null || true
sending=$(docker compose exec -T postgres psql -U email_platform -d email_platform -Atc \
  "SELECT id FROM emails WHERE status = 'sending'")
before=$(while read -r id; do status_of "$id"; done <"$TMP/ids.kill" | sort | uniq -c | tr '\n' ' ')
start_worker 2
wait_captured kill 240
dups=0 missing=0
while read -r id; do
  c=$(copies "$id")
  [[ "$c" -ge 1 ]] || missing=$((missing + 1))
  if [[ "$c" -gt 1 ]]; then
    dups=$((dups + 1))
    # Every duplicate must have been flagged for exactly this email.
    grep 'possible_duplicate_send' "$TMP/worker.log" | grep -qF "$id" || fail "kill: duplicate of $id was not flagged"
  fi
done <"$TMP/ids.kill"
[[ $missing -eq 0 ]] || fail "kill: $missing emails missing in Mailpit"
for id in $sending; do
  grep 'possible_duplicate_send' "$TMP/worker.log" | grep -qF "$id" || fail "kill: $id was mid-send at the kill but its retry was not flagged"
done
warned=$(grep -c 'possible_duplicate_send' "$TMP/worker.log" || true)
[[ "$running" -ge 1 ]] || fail "kill: no job was running at the kill; the scenario did not test rescue"
echo "(b) worker SIGKILL: $running jobs running at kill ($(wc -w <<<"$sending" | tr -d ' ') emails mid-send), email states [$before], 30/30 captured after restart; duplicates in Mailpit: $dups, possible_duplicate_send warnings: $warned"

# (c) PostgreSQL restart ------------------------------------------------------
send 5 prerestart
docker compose restart postgres >/dev/null 2>&1 &
RESTART_PID=$!
codes=()
saw503=0
while kill -0 "$RESTART_PID" 2>/dev/null; do
  r=$(curl -sS -o /dev/null -w '%{http_code}' "http://$ADDR/readyz" || echo 000)
  [[ "$r" == 503 ]] && saw503=1
  p=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://$ADDR/v1/emails" -H @"$TMP/auth.header" \
    -H 'Content-Type: application/json' -d '{"from":"res@example.test","to":"res-recipient@example.test","subject":"during restart","text":"x"}' || echo 000)
  codes+=("$p")
  [[ "$p" == 500 ]] && fail "POST during the database restart returned 500"
  sleep 0.2
done
wait "$RESTART_PID" || fail "docker compose restart postgres failed"
for _ in $(seq 1 100); do curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "http://$ADDR/readyz" >/dev/null || fail "API not ready after the database restart"
send 5 postrestart
wait_captured prerestart 120
wait_captured postrestart 120
echo "(c) PostgreSQL restart: readyz 503 seen: $saw503; POST codes during restart: $(printf '%s\n' "${codes[@]}" | sort | uniq -c | awk '{printf "%s×%s ", $1, $2}'); 10/10 captured afterwards without restarting api/worker"

# Needles from a file: the key must not appear in grep's command line.
printf '%s\n' "$key" "res-recipient@example.test" "resilience body" "reskey-" "email_platform:email_platform@" >"$TMP/needles"
if grep -qFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then fail "logs contain a secret, password, idempotency key or personal data"; fi
echo "RESILIENCE OK"
