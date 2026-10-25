#!/usr/bin/env bash
# Backup and restore drill against the local Compose PostgreSQL: seeds a
# throwaway source database through the real API, worker and admin CLI,
# takes a logical backup (pg_dump custom format, the client of the pinned
# server image), restores it into a second, empty database and proves the
# restore is complete and usable:
#   - same migration version and same row count in every table,
#   - the dump's text and hex renderings contain neither the key-encryption
#     key, the plaintext webhook secret nor the API key,
#   - the schema (tables, indexes, constraints, sequences) is identical,
#   - webhook secrets in the restored database decrypt with the KEK kept
#     outside the backup (admin webhook reencrypt to a new key),
#   - an API started on the restored database is ready and serves a seeded
#     email to the original API key.
# Prints the dump size and the restore time (the local RTO for this size).
# Both databases are dropped at the end. Needs `make up` first.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (make sets a local default)}"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"

run=$$-$RANDOM
SRC="ep_restore_src_${run//-/_}"
DST="ep_restore_dst_${run//-/_}"
base="${DATABASE_URL%%\?*}"
base="${base%/*}"
SRC_URL="$base/$SRC?sslmode=disable"
DST_URL="$base/$DST?sslmode=disable"
ADDR="127.0.0.1:${RESTORE_PORT:-18280}"
KEK_OLD="$(openssl rand -base64 32)"
KEK_NEW="$(openssl rand -base64 32)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
umask 077

# PostgreSQL client tools run inside the server container, so they match
# the server version (CI points PG_EXEC at its service container).
PG_EXEC="${PG_EXEC:-docker compose exec -T postgres}"
if [[ ! "$PG_EXEC" =~ ^docker\ (compose\ exec\ -T\ postgres|exec\ -i\ [0-9a-f]{12,64})$ ]]; then
  echo "refusing to run: PG_EXEC must be the local Compose postgres service or a container id" >&2
  exit 1
fi
read -r -a pg_exec <<<"$PG_EXEC"
pg() { "${pg_exec[@]}" "$@"; }
psql_db() { pg psql -X -v ON_ERROR_STOP=1 -U email_platform -d "$1" -Atq -c "$2"; }

cleanup() {
  kill "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  wait "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  for db in "$SRC" "$DST"; do
    psql_db postgres "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

# Secrets of this run, for redaction and leak checks; from a file, so they
# never appear on a command line.
printf '%s\n' "$KEK_OLD" "$KEK_NEW" >"$TMP/needles"
fail() {
  echo "RESTORE TEST FAIL: $*" >&2
  for f in api worker api-restored; do
    [[ -f "$TMP/$f.log" ]] && { echo "--- $f.log" >&2; grep -vFf "$TMP/needles" "$TMP/$f.log" >&2 || true; }
  done
  exit 1
}

wait_http() {
  for _ in $(seq 1 100); do
    curl -fsS "$1" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  return 1
}

go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin ./cmd/migrate
psql_db postgres "CREATE DATABASE $SRC" || fail "create $SRC"
DATABASE_URL="$SRC_URL" "$TMP/migrate" up >/dev/null || fail "migrate $SRC"

# --- Seed through the real processes -------------------------------------
export WEBHOOK_KEKS="old:$KEK_OLD"
env -u WEBHOOK_KEKS DATABASE_URL="$SRC_URL" HTTP_ADDR="$ADDR" ADMIN_HTTP_ADDR=off LOG_FORMAT=json "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
DATABASE_URL="$SRC_URL" LOG_FORMAT=json "$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!
wait_http "http://$ADDR/readyz" || fail "api on $SRC not ready"
kill -0 "$API_PID" 2>/dev/null || fail "api on $SRC exited (port $ADDR busy?)"

admin_src() { DATABASE_URL="$SRC_URL" "$TMP/admin" "$@"; }
project=$(admin_src project create -name "restore-$run" | cut -f1) || fail "project create"
admin_src key create -project "$project" 2>/dev/null \
  -scopes emails:send,emails:read,templates:read,templates:write,contacts:read,contacts:write >"$TMP/key" || fail "key create"
printf 'Authorization: Bearer %s\n' "$(cat "$TMP/key")" >"$TMP/auth.header"
cat "$TMP/key" >>"$TMP/needles"
# Subscribed to complaints only, which the local sink never produces.
admin_src webhook create -project "$project" -url "http://127.0.0.1:9/restore" -events email.complained >"$TMP/webhook.out" ||
  fail "webhook create"
secret=$(sed -nE 's/^secret \(shown once, store it now\): (whsec_[A-Za-z0-9_-]+)$/\1/p' "$TMP/webhook.out")
[[ -n "$secret" ]] || fail "webhook create printed no secret"
# Plaintext and hex (as a bytea column renders); the KEK's raw bytes in hex.
{
  printf '%s\n' "$secret"
  printf '%s' "$secret" | od -An -tx1 | tr -d ' \n'
  echo
  openssl base64 -d <<<"$KEK_OLD" | od -An -tx1 | tr -d ' \n'
  echo
} >>"$TMP/needles"

api() { curl -sS -o "$TMP/resp.json" -w '%{http_code}' -H @"$TMP/auth.header" -H 'Content-Type: application/json' "$@"; }
[[ "$(api -X POST "http://$ADDR/v1/templates" -d '{"slug":"restore"}')" == 201 ]] || fail "template create: $(cat "$TMP/resp.json")"
tmpl=$(sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$TMP/resp.json")
[[ "$(api -X POST "http://$ADDR/v1/templates/$tmpl/versions" -d '{"subject":"Hi {{.name}}","text":"Hello {{.name}}","variables":{"name":{"type":"string","required":true}}}')" == 201 ]] ||
  fail "template version: $(cat "$TMP/resp.json")"
[[ "$(api -X POST "http://$ADDR/v1/contacts" -d '{"email":"restore-contact@example.test","status":"subscribed","consent_source":"restore-test"}')" == 201 ]] ||
  fail "contact create: $(cat "$TMP/resp.json")"
ids=()
for i in 1 2 3; do
  [[ "$(api -X POST "http://$ADDR/v1/emails" -d "{\"from\":\"restore@example.test\",\"to\":\"restore-$i@example.test\",\"subject\":\"restore $run\",\"text\":\"body $i\"}")" == 202 ]] ||
    fail "send $i: $(cat "$TMP/resp.json")"
  ids+=("$(sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$TMP/resp.json")")
done
[[ "$(api -X POST "http://$ADDR/v1/emails" -d "{\"from\":\"restore@example.test\",\"to\":\"restore-t@example.test\",\"template_id\":\"$tmpl\",\"template_version\":1,\"variables\":{\"name\":\"R\"}}")" == 202 ]] ||
  fail "template send: $(cat "$TMP/resp.json")"
for id in "${ids[@]}"; do
  status=""
  for _ in $(seq 1 100); do
    api "http://$ADDR/v1/emails/$id" >/dev/null
    status=$(sed -E 's/.*"status":"([^"]+)".*/\1/' "$TMP/resp.json")
    [[ "$status" == captured || "$status" == failed ]] && break
    sleep 0.1
  done
  [[ "$status" == captured ]] || fail "email $id is $status, want captured"
done
kill -TERM "$API_PID" "$WORKER_PID" 2>/dev/null || fail "api or worker already exited"
wait "$API_PID" || fail "api exited non-zero"
wait "$WORKER_PID" || fail "worker exited non-zero"
unset API_PID WORKER_PID
grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log" && fail "a process log contains a secret of this run"
echo "seeded $SRC: project, key, template + version, contact, webhook endpoint, 4 emails (3 checked captured by Mailpit)"

# --- Backup ----------------------------------------------------------------
pg pg_dump -U email_platform -Fc -d "$SRC" >"$TMP/backup.dump" || fail "pg_dump"
size=$(wc -c <"$TMP/backup.dump" | tr -d ' ')
# The plain-SQL rendering of the dump (bytea as hex) holds no secret.
pg pg_restore -f - <"$TMP/backup.dump" >"$TMP/backup.sql" || fail "render dump"
grep -qiFf "$TMP/needles" "$TMP/backup.sql" && fail "the backup contains a KEK, API key or plaintext webhook secret"
echo "pg_dump -Fc -> $size bytes; no KEK, API key or plaintext webhook secret in its text or hex rendering"

# --- Restore ---------------------------------------------------------------
psql_db postgres "CREATE DATABASE $DST" || fail "create $DST"
TIMEFORMAT=%R
{ time pg pg_restore -U email_platform --exit-on-error --no-owner -d "$DST" <"$TMP/backup.dump" || fail "pg_restore"; } 2>"$TMP/rto"
rto=$(tail -1 "$TMP/rto")
echo "pg_restore into $DST -> ${rto}s"

version_sql="SELECT max(version_id) FROM goose_db_version WHERE is_applied"
v_src=$(psql_db "$SRC" "$version_sql") || fail "query $SRC"
v_dst=$(psql_db "$DST" "$version_sql") || fail "query $DST"
[[ -n "$v_src" && "$v_src" == "$v_dst" ]] || fail "migration version $v_src vs $v_dst"
v_code=$(find db/migrations -name '[0-9]*.sql' | sed -E 's|.*/0*([0-9]+)_.*|\1|' | sort -n | tail -1)
[[ "$v_dst" == "$v_code" ]] || fail "restored migration version $v_dst, code has $v_code"
counts_sql="SELECT string_agg(t || '=' || n, ' ' ORDER BY t) FROM (
  SELECT table_name AS t, (xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I', table_name), false, true, '')))[1]::text AS n
  FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE') c"
c_src=$(psql_db "$SRC" "$counts_sql") || fail "query $SRC"
c_dst=$(psql_db "$DST" "$counts_sql") || fail "query $DST"
[[ "$c_src" == "$c_dst" ]] || fail "row counts differ: $c_src vs $c_dst"
# Schema inventory: columns with types, constraints, indexes, triggers and
# sequence positions (by name and kind; pg_dump's text deparses CHECK
# expressions differently after a restore, so its output is not compared).
schema_sql="SELECT md5(string_agg(x, ',' ORDER BY x)) || ' ' || count(*) FROM (
  SELECT 'col:' || table_name || '.' || column_name || ':' || data_type || ':' || is_nullable AS x FROM information_schema.columns WHERE table_schema = 'public'
  UNION ALL SELECT 'con:' || conrelid::regclass || '.' || conname || ':' || contype::text FROM pg_constraint WHERE connamespace = 'public'::regnamespace
  UNION ALL SELECT 'idx:' || tablename || '.' || indexname FROM pg_indexes WHERE schemaname = 'public'
  UNION ALL SELECT 'trg:' || tgrelid::regclass || '.' || tgname FROM pg_trigger WHERE NOT tgisinternal
  UNION ALL SELECT 'seq:' || sequencename || ':' || coalesce(last_value, 0) FROM pg_sequences WHERE schemaname = 'public') s"
s_src=$(psql_db "$SRC" "$schema_sql") || fail "schema inventory of $SRC"
s_dst=$(psql_db "$DST" "$schema_sql") || fail "schema inventory of $DST"
[[ "$s_src" == "$s_dst" ]] || fail "restored schema inventory differs: $s_src vs $s_dst"
# From here on only the restored database exists: nothing below can pass by
# reading the source.
psql_db postgres "DROP DATABASE $SRC WITH (FORCE)" || fail "drop $SRC"
for t in projects api_keys emails email_events templates template_versions contacts webhook_endpoints; do
  [[ " $c_dst " =~ \ $t=([0-9]+)\  && "${BASH_REMATCH[1]}" -ge 1 ]] || fail "restored table $t is empty ($c_dst)"
done
tables=$(wc -w <<<"$c_dst" | tr -d ' ')
echo "migration version $v_dst (= code), schema inventory (${s_dst#* } objects) and row counts equal in all $tables tables; source dropped"

out=$(DATABASE_URL="$DST_URL" WEBHOOK_KEKS="new:$KEK_NEW,old:$KEK_OLD" "$TMP/admin" webhook reencrypt) || fail "reencrypt on $DST"
[[ "$out" == "re-encrypted 1 secrets with key new" ]] || fail "reencrypt: $out"
echo "webhook secret decrypts with the KEK kept outside the backup (re-encrypted to a new key)"

env -u WEBHOOK_KEKS DATABASE_URL="$DST_URL" HTTP_ADDR="$ADDR" ADMIN_HTTP_ADDR=off LOG_FORMAT=json "$TMP/api" >"$TMP/api-restored.log" 2>&1 &
API_PID=$!
wait_http "http://$ADDR/healthz" || fail "api on $DST did not start"
kill -0 "$API_PID" 2>/dev/null || fail "api on $DST exited (port $ADDR busy?)"
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://$ADDR/readyz")" == 200 ]] || fail "api on $DST not ready"
[[ "$(api "http://$ADDR/v1/emails/${ids[0]}")" == 200 ]] && grep -q '"status":"captured"' "$TMP/resp.json" ||
  fail "GET seeded email on $DST: $(cat "$TMP/resp.json")"
echo "api on the restored database -> ready, seeded email served to the original key"
kill -TERM "$API_PID" 2>/dev/null || fail "restored api already exited"
wait "$API_PID" || fail "restored api exited non-zero"
unset API_PID

echo "RESTORE TEST OK (dump $size bytes, restore ${rto}s)"
