#!/usr/bin/env bash
# Browser end-to-end test of the dashboard. Creates a throwaway database on
# the server of TEST_DATABASE_URL, migrates it, starts the API (public and
# admin listener) and seeds it through the admin CLI and the /v1 API, starts
# the worker (Mailpit only), builds the dashboard and runs the Playwright
# suite, which serves the production build itself. Every process is stopped
# and the database dropped on exit. Needs `make up` first.
# Extra arguments go to `playwright test` (e.g. a spec file or --headed).
set -euo pipefail

: "${TEST_DATABASE_URL:?TEST_DATABASE_URL must be set (make sets a local default)}"
# The guard checks DATABASE_URL; the throwaway database lives on this server.
DATABASE_URL="$TEST_DATABASE_URL"
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
command -v psql >/dev/null || { echo "e2e: psql is required to create the throwaway database" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
API_ADDR="127.0.0.1:${E2E_API_PORT:-18180}"
ADMIN_ADDR="127.0.0.1:${E2E_ADMIN_PORT:-18181}"
export E2E_DASHBOARD_PORT="${E2E_DASHBOARD_PORT:-3100}"
export WEBHOOK_KEKS="${WEBHOOK_KEKS:-run:$(openssl rand -base64 32)}"
# Short enough that the cookie expiry bound in the suite is meaningful.
export ADMIN_SESSION_MAX=2h

DB="ep_e2e_$(openssl rand -hex 6)"
base="${TEST_DATABASE_URL%%\?*}"
params=""
[[ "$TEST_DATABASE_URL" == *\?* ]] && params="?${TEST_DATABASE_URL#*\?}"
export DATABASE_URL="${base%/*}/$DB$params"

TMP="$(mktemp -d)"
chmod 700 "$TMP"
cleanup() {
  kill "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  wait "${API_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
  psql -X -q -d "$TEST_DATABASE_URL" -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" >/dev/null 2>&1 ||
    echo "e2e: could not drop database $DB" >&2
  rm -rf "$TMP"
}
trap cleanup EXIT

# Logs are printed on failure with the run's secrets filtered out.
redact() { if [[ -s "$TMP/needles" ]]; then grep -viFf "$TMP/needles" "$1" || true; else cat "$1"; fi; }
fail() {
  echo "E2E FAIL: $*" >&2
  for f in api worker; do [[ -f "$TMP/$f.log" ]] && { echo "--- $f.log" >&2; redact "$TMP/$f.log" >&2; }; done
  exit 1
}

# $DB is generated from hex digits only, so it needs no quoting.
psql -X -q -v ON_ERROR_STOP=1 -d "$TEST_DATABASE_URL" -c "CREATE DATABASE $DB" >/dev/null
echo "database $DB created"

go build -o "$TMP/" ./cmd/api ./cmd/worker ./cmd/admin ./cmd/migrate
"$TMP/migrate" up >/dev/null || fail "migrate"

HTTP_ADDR="$API_ADDR" ADMIN_HTTP_ADDR="$ADMIN_ADDR" LOG_FORMAT=json "$TMP/api" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 100); do
  curl -fsS "http://$API_ADDR/readyz" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -fsS "http://$API_ADDR/readyz" >/dev/null 2>&1 || fail "api not ready"
echo "api ready on $API_ADDR (admin $ADMIN_ADDR)"

# Seed while the worker is down: the email whose recipient is suppressed
# after it was accepted must still be queued when the worker starts.
umask 077
export E2E_SEED="$TMP/seed.json" E2E_NEEDLES="$TMP/needles" E2E_ADMIN_BIN="$TMP/admin"
export E2E_API_URL="http://$API_ADDR" E2E_ADMIN_API_URL="http://$ADMIN_ADDR"
(cd "$ROOT/apps/dashboard" && pnpm exec node e2e/seed.ts seed) || fail "seed"

PROVIDER_RATE_PER_SECOND=100 LOG_FORMAT=json "$TMP/worker" >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!
(cd "$ROOT/apps/dashboard" && pnpm exec node e2e/seed.ts wait) || fail "seeded emails did not reach a final state"
echo "seed complete"

pnpm --filter dashboard build >"$TMP/build.log" 2>&1 || { cat "$TMP/build.log" >&2; fail "dashboard build"; }
echo "dashboard built"

status=0
(cd "$ROOT/apps/dashboard" && ADMIN_API_URL="http://$ADMIN_ADDR" pnpm exec playwright test "$@") || status=$?
[[ $status == 0 ]] || fail "playwright exited with $status"

kill -TERM "$API_PID" "$WORKER_PID"
wait "$API_PID" || fail "api exited non-zero after SIGTERM"
wait "$WORKER_PID" || fail "worker exited non-zero after SIGTERM"
# After shutdown, so that lines written while stopping are checked too.
if grep -qiFf "$TMP/needles" "$TMP/api.log" "$TMP/worker.log"; then
  echo "E2E FAIL: logs contain a seeded secret or recipient address" >&2
  exit 1
fi
echo "E2E OK"
