#!/usr/bin/env bash
# Automation acceptance through the real binaries (API, worker, Mailpit):
#   - a contact event starts a run; a retried request with the same
#     Idempotency-Key starts no second run;
#   - the first email reaches Mailpit with one-click unsubscribe headers
#     carrying the contact's own token;
#   - an event reported during the wait ends that run at its condition, the
#     other run sends its second email after the wait, across a worker
#     SIGKILL while both runs wait;
#   - a webhook receiver that refuses the first delivery of every event gets
#     it again (signed) after the worker restart;
#   - archiving an automation cancels its waiting run.
# Needs `make up migrate`. Takes about two minutes (waits are at least 60 s).
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
ADDR="127.0.0.1:${ACCEPT_PORT:-18180}"
RECV_ADDR="127.0.0.1:${ACCEPT_RECV_PORT:-18190}"
MAILPIT_URL="${TEST_MAILPIT_URL:-http://127.0.0.1:8025}"
TMP="$(mktemp -d)"
API_PID="" WORKER_PID="" RECV_PID=""

cleanup() {
  [[ -n "${key:-}" ]] && "$TMP/admin" key revoke -prefix "$(cut -d_ -f2 <<<"$key")" >/dev/null 2>&1 || true
  [[ -s "$TMP/webhook.out" ]] && "$TMP/admin" webhook disable -project "$project" -id "$(head -1 "$TMP/webhook.out" | cut -f1)" >/dev/null 2>&1 || true
  kill "$API_PID" "$WORKER_PID" "$RECV_PID" 2>/dev/null || true
  wait "$API_PID" "$WORKER_PID" "$RECV_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

redact() { if [[ -s "$TMP/needles" ]]; then grep -viFf "$TMP/needles" "$1" || true; else cat "$1"; fi; }
fail() {
  echo "ACCEPTANCE FAIL: $*" >&2
  for f in api worker recv; do echo "--- $f.log (tail)" >&2; redact "$TMP/$f.log" | tail -n 30 >&2; done
  exit 1
}

go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin
go build -tags smoke -o "$TMP/" ./cmd/webhookrecv

# Webhook retries 8–10 s apart, so the refused first delivery is retried
# after the worker restart below.
export LOG_FORMAT=json PROVIDER_RATE_PER_SECOND=50 WEBHOOK_RETRY_BASE_DELAY=8s WEBHOOK_RETRY_MAX_DELAY=10s
env -u WEBHOOK_KEKS HTTP_ADDR="$ADDR" ADMIN_HTTP_ADDR=off "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
start_worker() {
  "$TMP/worker" >>"$TMP/worker.log" 2>&1 &
  WORKER_PID=$!
  for _ in $(seq 1 100); do
    [[ $(grep -c '"worker ready"' "$TMP/worker.log") -ge $1 ]] && return
    sleep 0.1
  done
  fail "worker did not become ready"
}
start_worker 1
env -u WEBHOOK_KEKS WEBHOOK_SECRET_FILE="$TMP/whsec" "$TMP/webhookrecv" -addr "$RECV_ADDR" -out "$TMP/hooks.jsonl" -fail-first 1 >"$TMP/recv.log" 2>&1 &
RECV_PID=$!
for _ in $(seq 1 50); do curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fsS "http://$ADDR/readyz" >/dev/null || fail "api not ready"

project=$("$TMP/admin" project create -name "accept-$(date +%s)-$RANDOM" | cut -f1) || fail "admin project create"
key=$("$TMP/admin" key create -project "$project" \
  -scopes emails:read,templates:write,contacts:write,automations:read,automations:write,events:write 2>/dev/null) || fail "admin key create"
run_id=$RANDOM
ANN="ann-$run_id@accept.test" BOB="bob-$run_id@accept.test" CID="cid-$run_id@accept.test"
umask 077
printf '%s\n' "$key" "$ANN" "$BOB" "$CID" "accept-secret-" >"$TMP/needles"
tr ',' '\n' <<<"$WEBHOOK_KEKS" | sed 's/^[^:]*://' >>"$TMP/needles"
printf 'Authorization: Bearer %s\n' "$key" >"$TMP/auth.header"

"$TMP/admin" webhook create -project "$project" -url "http://$RECV_ADDR/hooks" >"$TMP/webhook.out" 2>"$TMP/admin.err" ||
  fail "admin webhook create: $(cat "$TMP/admin.err")"
sed -nE 's/^secret \(shown once, store it now\): (whsec_[A-Za-z0-9_-]+)$/\1/p' "$TMP/webhook.out" >"$TMP/whsec"
[[ -s "$TMP/whsec" ]] || fail "no webhook secret"
cat "$TMP/whsec" >>"$TMP/needles"

# api METHOD PATH BODY [HEADER] -> response body; fails on >= 300.
api() {
  local code extra=()
  [[ $# -ge 4 ]] && extra=(-H "$4")
  code=$(curl -sS -o "$TMP/resp.json" -w '%{http_code}' -X "$1" "http://$ADDR$2" -H @"$TMP/auth.header" \
    -H 'Content-Type: application/json' ${extra[@]+"${extra[@]}"} ${3:+-d "$3"})
  [[ "$code" -lt 300 ]] || fail "$1 $2: $code $(cat "$TMP/resp.json")"
  cat "$TMP/resp.json"
}
# field NAME: the first value of NAME in a JSON response.
field() { grep -o "\"$1\":\"\{0,1\}[^\",}]*" | head -1 | sed -E "s/^\"$1\":\"?//"; }
# run_field NAME: a field of a run itself, not of its emails.
run_field() { sed -E 's/,"emails":\[.*//' | field "$1"; }
first_run() { sed -nE 's/.*"runs":\[\{"id":"([0-9a-f-]{36})".*/\1/p'; }

tmpl=$(api POST /v1/templates '{"slug":"welcome"}' | field id)
api POST "/v1/templates/$tmpl/versions" '{"subject":"Welcome","text":"Hi {{.name}}\nUnsubscribe: {{.unsubscribe_url}}","html":"<p>Hi {{.name}}</p><a href=\"{{.unsubscribe_url}}\">Unsubscribe</a>","variables":{"name":{"type":"string"},"unsubscribe_url":{"type":"string","required":true}}}' >/dev/null
send="{\"type\":\"send\",\"from\":\"news@accept.test\",\"template_id\":\"$tmpl\",\"template_version\":1,\"unsubscribe_url\":\"https://app.accept.test/u?t={token}\"}"
welcome=$(api POST /v1/automations "{\"name\":\"Welcome\",\"trigger\":{\"event\":\"user.signed_up\"},\"steps\":[$send,{\"type\":\"wait\",\"seconds\":60},{\"type\":\"condition\",\"event\":\"order.completed\",\"op\":\"not_occurred\"},$send]}" | field id)
trial=$(api POST /v1/automations "{\"name\":\"Trial\",\"trigger\":{\"event\":\"trial.started\"},\"steps\":[{\"type\":\"wait\",\"seconds\":60},$send]}" | field id)
contact() { api POST /v1/contacts "{\"email\":\"$1\",\"status\":\"subscribed\",\"consent_source\":\"acceptance\",\"attributes\":{\"name\":\"$2\"}}" | field id; }
ann=$(contact "$ANN" Ann) bob=$(contact "$BOB" Bob) cid=$(contact "$CID" Cid)

# A retried trigger (same key, same payload) replays: one run.
ev1=$(api POST /v1/contact-events "{\"name\":\"user.signed_up\",\"contact_id\":\"$ann\",\"data\":{\"name\":\"accept-secret-$run_id\"}}" "Idempotency-Key: signup-$ann")
ev2=$(api POST /v1/contact-events "{\"name\":\"user.signed_up\",\"contact_id\":\"$ann\",\"data\":{\"name\":\"accept-secret-$run_id\"}}" "Idempotency-Key: signup-$ann")
run_ann=$(first_run <<<"$ev1")
[[ -n "$run_ann" && "$(first_run <<<"$ev2")" == "$run_ann" && "$(field id <<<"$ev1")" == "$(field id <<<"$ev2")" ]] ||
  fail "retried trigger: $ev1 / $ev2"
run_bob=$(api POST /v1/contact-events "{\"name\":\"user.signed_up\",\"contact_id\":\"$bob\"}" "Idempotency-Key: signup-$bob" | first_run)
runs=$(api GET "/v1/automations/$welcome/runs" "" | grep -o '"id":"[0-9a-f-]\{36\}","automation_id"' | wc -l | tr -d ' ')
[[ "$runs" == 2 ]] || fail "welcome automation has $runs runs, want 2"
echo "contact events -> 2 runs; a retried trigger with the same key replayed the first (same event and run)"

# Archiving cancels a waiting run (in the background, within seconds).
run_cid=$(api POST /v1/contact-events "{\"name\":\"trial.started\",\"contact_id\":\"$cid\"}" | first_run)
api PATCH "/v1/automations/$trial" '{"status":"archived"}' >/dev/null
for _ in $(seq 1 100); do
  [[ "$(api GET "/v1/automation-runs/$run_cid" "" | run_field status)" == canceled ]] && break
  sleep 0.2
done
[[ "$(api GET "/v1/automation-runs/$run_cid" "" | run_field reason)" == automation_archived ]] || fail "archived automation's run not canceled"
echo "archive -> waiting run canceled (automation_archived)"

# wait_run RUN STEP SECONDS: until the run is at STEP or ended.
run_email() { api GET "/v1/automation-runs/$1" "" | sed -nE "s/.*\"emails\":\[(.*)\].*/\1/p" | grep -o "\"id\":\"[0-9a-f-]\{36\}\",\"step\":$2,\"status\":\"captured\"" | field id; }
deadline=$((SECONDS + 60))
until [[ -n "$(run_email "$run_ann" 0)" && -n "$(run_email "$run_bob" 0)" ]]; do
  [[ $SECONDS -ge $deadline ]] && fail "first emails not captured: $(api GET "/v1/automation-runs/$run_ann" "")"
  sleep 0.5
done
ann_first=$(run_email "$run_ann" 0)

# Mailpit: the message carries the contact's own one-click unsubscribe link.
msg=$(curl -sS "$MAILPIT_URL/api/v1/search?query=message-id:%22$ann_first@email-platform.local%22" | sed -nE 's/.*"messages":\[\{"ID":"([^"]+)".*/\1/p')
[[ -n "$msg" ]] || fail "first email of $run_ann not in Mailpit"
headers=$(curl -sS "$MAILPIT_URL/api/v1/message/$msg/headers")
token=$(docker compose exec -T postgres psql -U email_platform -d email_platform -Atc "SELECT unsubscribe_token FROM contacts WHERE id = '$ann'")
[[ "$headers" == *"https://app.accept.test/u?t=$token"* && "$headers" == *"List-Unsubscribe=One-Click"* ]] ||
  fail "List-Unsubscribe headers missing or not personal"
echo "Mailpit -> first emails with one-click List-Unsubscribe carrying the contact's token"

# The order arrives during the wait: Ann's run will end at its condition.
api POST /v1/contact-events "{\"name\":\"order.completed\",\"contact_id\":\"$ann\"}" >/dev/null

# The receiver refused the first delivery of Ann's email.captured; kill the
# worker while the retry is pending and both runs wait.
deadline=$((SECONDS + 30))
until grep -q "\"email_id\":\"$ann_first\",\"valid\":true,\"status\":503" "$TMP/hooks.jsonl" 2>/dev/null; do
  [[ $SECONDS -ge $deadline ]] && fail "no refused delivery for $ann_first"
  sleep 0.2
done
kill -KILL "$WORKER_PID"; wait "$WORKER_PID" 2>/dev/null || true
echo "worker SIGKILL while both runs wait and a webhook retry is pending"
sleep 62
start_worker 2

deadline=$((SECONDS + 90))
until [[ "$(api GET "/v1/automation-runs/$run_bob" "" | run_field status)" == completed && -n "$(run_email "$run_bob" 3)" ]]; do
  [[ $SECONDS -ge $deadline ]] && fail "Bob's run not completed: $(api GET "/v1/automation-runs/$run_bob" "")"
  sleep 0.5
done
ann_run=$(api GET "/v1/automation-runs/$run_ann" "")
[[ "$(run_field status <<<"$ann_run")" == exited && "$(run_field reason <<<"$ann_run")" == condition_not_met ]] || fail "Ann's run: $ann_run"
[[ "$(grep -o '"step":[0-9]*,"status"' <<<"$ann_run" | wc -l | tr -d ' ')" == 1 ]] || fail "Ann's run sent more than one email: $ann_run"
echo "after restart -> Bob's run completed (2 emails), Ann's run exited at its condition (1 email)"

# The refused delivery was retried after the restart and accepted, signed.
deadline=$((SECONDS + 30))
until grep -q "\"email_id\":\"$ann_first\",\"valid\":true,\"status\":204" "$TMP/hooks.jsonl" 2>/dev/null; do
  [[ $SECONDS -ge $deadline ]] && fail "refused delivery for $ann_first not retried: $(grep "$ann_first" "$TMP/hooks.jsonl")"
  sleep 0.5
done
grep "$ann_first" "$TMP/hooks.jsonl" | grep -q '"valid":false' && fail "an unsigned or badly signed delivery for $ann_first"
echo "webhook -> refused email.captured delivery retried after the worker restart and accepted (signature valid)"

copies() { curl -sS "$MAILPIT_URL/api/v1/search?query=to:%22$1%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/'; }
[[ "$(copies "$ANN")" == 1 && "$(copies "$BOB")" == 2 && "$(copies "$CID")" == 0 ]] ||
  fail "Mailpit copies: Ann $(copies "$ANN"), Bob $(copies "$BOB"), Cid $(copies "$CID"); want 1, 2, 0"
echo "Mailpit -> Ann 1, Bob 2, Cid 0 messages"

kill -TERM "$API_PID" "$WORKER_PID"
wait "$API_PID" || fail "api exited non-zero after SIGTERM"
wait "$WORKER_PID" || fail "worker exited non-zero after SIGTERM"
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then fail "logs contain a secret, key, event data or recipient address"; fi
echo "ACCEPTANCE OK"
