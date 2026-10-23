#!/usr/bin/env bash
# Runs the production Compose stack (deploy/compose.yaml) locally with stand-ins
# for what only exists on AWS (deploy/check/compose.yaml): a TLS PostgreSQL
# with a generated CA instead of RDS, Mailpit instead of SES, Caddy's internal
# CA instead of Let's Encrypt. Proves:
#   - render-env (with a stub aws CLI) writes an env file Compose reads as is;
#     preflight passes with it and refuses a missing alert receiver,
#   - a second `up` on the existing volumes succeeds (release / rollback),
#   - the app connects with sslmode=verify-full; a wrong CA or a host name not in
#     the certificate is refused, and migrate refuses sslmode=require,
#   - only the application user reaches the instance metadata address,
#   - HTTPS serves /healthz and an authenticated send that reaches Mailpit; the
#     operator API, /metrics and everything else are 404 through the proxy;
#     plain HTTP redirects to HTTPS,
#   - the dashboard login page is served on the host's loopback only,
#   - Prometheus scrapes api and worker, loaded every alert rule and found
#     Alertmanager,
#   - every container except netns runs as a non-root user, all with a
#     read-only root file system, memory and process limits.
# Uses its own Compose project and ports; removes everything at the end.
# Needs Docker and openssl; no AWS, no real mail.
set -euo pipefail

: "${DATABASE_URL:=postgres://email_platform:email_platform@localhost:54320/email_platform?sslmode=disable}"
export DATABASE_URL
# shellcheck source=scripts/local-guard.sh
source "$(dirname "$0")/local-guard.sh"
unset DATABASE_URL

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK_DIR="$(mktemp -d)"
umask 077
PROJECT="ep-deploy-check-$$"
dc() { docker compose -p "$PROJECT" -f "$ROOT/deploy/compose.yaml" -f "$ROOT/deploy/check/compose.yaml" "$@"; }
cleanup() {
  dc --profile ops down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$CHECK_DIR"
}
trap cleanup EXIT
fail() {
  echo "DEPLOY CHECK FAIL: $*" >&2
  dc logs --no-color --tail 40 api worker caddy postgres 2>/dev/null | grep -vFf "$CHECK_DIR/needles" >&2 || true
  exit 1
}

# --- Certificates: a CA for the database (and a second, wrong one) ---------
mkdir -p "$CHECK_DIR/certs" "$CHECK_DIR/wrong" "$CHECK_DIR/alertmanager"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=deploy-check CA" \
  -keyout "$CHECK_DIR/certs/ca.key" -out "$CHECK_DIR/certs/ca.pem" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" \
  -keyout "$CHECK_DIR/certs/server.key" -out "$CHECK_DIR/certs/server.csr" 2>/dev/null
printf 'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n' >"$CHECK_DIR/certs/ext.cnf"
openssl x509 -req -in "$CHECK_DIR/certs/server.csr" -CA "$CHECK_DIR/certs/ca.pem" -CAkey "$CHECK_DIR/certs/ca.key" \
  -CAcreateserial -days 1 -extfile "$CHECK_DIR/certs/ext.cnf" -out "$CHECK_DIR/certs/server.crt" 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=wrong CA" \
  -keyout "$CHECK_DIR/wrong/ca.key" -out "$CHECK_DIR/wrong/ca.pem" 2>/dev/null
# The postgres container user must be able to read the key it copies.
chmod 644 "$CHECK_DIR/certs/server.key" "$CHECK_DIR/certs/server.crt" "$CHECK_DIR/certs/ca.pem"
chmod 755 "$CHECK_DIR" "$CHECK_DIR/certs"

# --- Runtime env, as render-env writes it on the VM ------------------------
CHECK_DB_PASSWORD="$(openssl rand -hex 16)"
KEK="$(openssl rand -base64 32)"
db_url() { printf 'postgres://email_platform:%s@%s:5433/email_platform?sslmode=verify-full&sslrootcert=%s' "$CHECK_DB_PASSWORD" "$1" "$2"; }
# render-env reads SSM through a stub aws CLI that prints the parameters.
mkdir -p "$CHECK_DIR/bin"
{
  printf '/email-platform/prod/DATABASE_URL\t%s\n' "$(db_url localhost /etc/email-platform/db-ca.pem)"
  printf '/email-platform/prod/WEBHOOK_KEKS\tcheck:%s\n' "$KEK"
  printf '/email-platform/prod/MAIL_PROVIDER\tmailpit\n/email-platform/prod/SMTP_ADDR\t127.0.0.1:1025\n/email-platform/prod/LOG_FORMAT\tjson\n'
} >"$CHECK_DIR/ssm-params"
printf '#!/usr/bin/env bash\ncat "%s"\n' "$CHECK_DIR/ssm-params" >"$CHECK_DIR/bin/aws"
chmod 700 "$CHECK_DIR/bin/aws"
env -i PATH="$CHECK_DIR/bin:/usr/bin:/bin" AWS_REGION=eu-central-1 APP_ENV_FILE="$CHECK_DIR/app.env" \
  bash "$ROOT/deploy/bin/render-env" >/dev/null || fail "render-env"
printf '%s\n' "$CHECK_DB_PASSWORD" "$KEK" >"$CHECK_DIR/needles"
echo "http://127.0.0.1:9095/alerts" >"$CHECK_DIR/alertmanager/webhook-url"
chmod 755 "$CHECK_DIR/alertmanager"
chmod 644 "$CHECK_DIR/alertmanager/webhook-url"

export CHECK_DIR CHECK_DB_PASSWORD
export APP_ENV_FILE="$CHECK_DIR/app.env" DB_CA_FILE="$CHECK_DIR/certs/ca.pem" ALERT_SECRETS_DIR="$CHECK_DIR/alertmanager"
export API_DOMAIN=localhost ACME_EMAIL=ops@example.test CADDYFILE="$ROOT/deploy/check/Caddyfile"
export HTTP_PORT="${DEPLOY_CHECK_HTTP_PORT:-18180}" HTTPS_PORT="${DEPLOY_CHECK_HTTPS_PORT:-18443}" DASHBOARD_PORT="${DEPLOY_CHECK_DASHBOARD_PORT:-13000}"
export APP_IMAGE=email-platform-app:deploy-check DASHBOARD_IMAGE=email-platform-dashboard:deploy-check

# --- Preflight ---------------------------------------------------------------
"$ROOT/deploy/bin/preflight" || fail "preflight refused the rendered files"
mv "$CHECK_DIR/alertmanager/webhook-url" "$CHECK_DIR/alertmanager/missing"
if "$ROOT/deploy/bin/preflight" 2>/dev/null; then fail "preflight accepted a missing alert receiver"; fi
mv "$CHECK_DIR/alertmanager/missing" "$CHECK_DIR/alertmanager/webhook-url"
if [[ "$(uname -s)" == Linux && "$(id -u)" != 65534 ]]; then
  chmod 600 "$CHECK_DIR/alertmanager/webhook-url"
  if "$ROOT/deploy/bin/preflight" 2>/dev/null; then fail "preflight accepted a receiver file Alertmanager cannot read"; fi
  chmod 644 "$CHECK_DIR/alertmanager/webhook-url"
fi
echo "preflight -> passes; refuses a missing (and on Linux an unreadable) alert receiver"

# --- Up ----------------------------------------------------------------------
dc build --quiet || fail "build"
dc up -d --wait --wait-timeout 180 || fail "stack did not become healthy"
echo "compose up -> all services running/healthy"
# A release or rollback runs `up` again on the existing volumes: every one-shot
# service (migrate, caddy-init) must succeed a second time.
dc up -d --wait --wait-timeout 180 || fail "second compose up on the existing volumes failed"
echo "compose up again -> one-shot services re-run, all services healthy"

# --- Database TLS --------------------------------------------------------------
# The URL (with the password) reaches the container through the environment,
# never the command line.
run_migrate() { DATABASE_URL="$1" dc run --rm --no-deps -e DATABASE_URL migrate >"$CHECK_DIR/migrate.out" 2>&1; }
run_migrate "$(db_url localhost /etc/email-platform/db-ca.pem)" || fail "verify-full with the right CA: $(grep -vFf "$CHECK_DIR/needles" "$CHECK_DIR/migrate.out" | tail -3)"
cp "$CHECK_DIR/wrong/ca.pem" "$CHECK_DIR/certs/wrong-ca.pem"
chmod 644 "$CHECK_DIR/certs/wrong-ca.pem"
DATABASE_URL="$(db_url localhost /etc/email-platform/wrong-ca.pem)" dc run --rm --no-deps \
  -v "$CHECK_DIR/certs/wrong-ca.pem:/etc/email-platform/wrong-ca.pem:ro" -e DATABASE_URL migrate >"$CHECK_DIR/migrate.out" 2>&1 &&
  fail "a server certificate from another CA was accepted"
grep -qiE 'x509|certificate' "$CHECK_DIR/migrate.out" || fail "wrong CA refused for another reason: $(grep -vFf "$CHECK_DIR/needles" "$CHECK_DIR/migrate.out" | tail -2)"
run_migrate "$(db_url 127.0.0.1 /etc/email-platform/db-ca.pem)" && fail "a host name not in the certificate was accepted"
grep -qiE 'x509|certificate' "$CHECK_DIR/migrate.out" || fail "wrong host refused for another reason: $(grep -vFf "$CHECK_DIR/needles" "$CHECK_DIR/migrate.out" | tail -2)"
run_migrate "postgres://email_platform:$CHECK_DB_PASSWORD@localhost:5433/email_platform?sslmode=require" &&
  fail "migrate accepted sslmode=require"
grep -q 'sslmode=verify-full' "$CHECK_DIR/migrate.out" || fail "sslmode=require refused for another reason: $(grep -vFf "$CHECK_DIR/needles" "$CHECK_DIR/migrate.out" | tail -2)"
echo "database TLS -> verify-full passes; wrong CA, wrong host name and migrate with sslmode=require refused"

# --- Public HTTPS surface ------------------------------------------------------
dc exec -T caddy cat /data/caddy/pki/authorities/local/root.crt >"$CHECK_DIR/caddy-root.pem" || fail "Caddy root CA"
https() { curl -sS --cacert "$CHECK_DIR/caddy-root.pem" "$@"; }
base="https://localhost:$HTTPS_PORT"
[[ "$(https -o /dev/null -w '%{http_code}' "$base/healthz")" == 200 ]] || fail "HTTPS /healthz"
[[ "$(https -o /dev/null -w '%{http_code}' "$base/readyz")" == 200 ]] || fail "HTTPS /readyz"
for path in /admin/v1/me /metrics / /dashboard; do
  [[ "$(https -o /dev/null -w '%{http_code}' "$base$path")" == 404 ]] || fail "HTTPS $path is not 404"
done
https -sI "$base/healthz" | grep -qi '^strict-transport-security: max-age=31536000' || fail "no HSTS header"
code=$(curl -sS -o /dev/null -w '%{http_code}' "http://localhost:$HTTP_PORT/healthz")
[[ "$code" == 308 || "$code" == 301 ]] || fail "plain HTTP answers $code, want a redirect to HTTPS"
echo "HTTPS -> probes 200, operator API/metrics/other paths 404, HSTS, HTTP redirects"

project=$(dc run --rm admin project create -name "deploy-check-$$" 2>/dev/null | cut -f1) || fail "admin project create"
dc run --rm admin key create -project "$project" >"$CHECK_DIR/key" 2>/dev/null || fail "admin key create"
cat "$CHECK_DIR/key" >>"$CHECK_DIR/needles"
printf 'Authorization: Bearer %s\n' "$(cat "$CHECK_DIR/key")" >"$CHECK_DIR/auth.header"
code=$(https -o "$CHECK_DIR/send.json" -w '%{http_code}' -X POST "$base/v1/emails" -H @"$CHECK_DIR/auth.header" \
  -H 'Content-Type: application/json' -d '{"from":"deploy@example.test","to":"check@example.test","subject":"deploy check","text":"hello"}')
[[ "$code" == 202 ]] || fail "POST /v1/emails over HTTPS: $code"
id=$(sed -nE 's/.*"id":"([0-9a-f-]{36})".*/\1/p' "$CHECK_DIR/send.json")
# Loopback-only services, queried from inside the shared namespace.
inside() { dc exec -T prometheus wget -qO- "$1"; }
count=0
for _ in $(seq 1 100); do
  count=$(inside "http://127.0.0.1:8025/api/v1/search?query=message-id:%22$id@email-platform.local%22" | sed -E 's/.*"messages_count":([0-9]+).*/\1/' || true)
  [[ "$count" == 1 ]] && break
  sleep 0.2
done
[[ "$count" == 1 ]] || fail "the send did not reach Mailpit"
echo "HTTPS send -> 202, captured by Mailpit"

# --- Dashboard ---------------------------------------------------------------
[[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$DASHBOARD_PORT/login")" == 200 ]] || fail "dashboard /login"
docker port "$(dc ps -q netns)" 3000/tcp | grep -qv '^127\.0\.0\.1:' && fail "dashboard port published beyond loopback"
echo "dashboard -> login page on 127.0.0.1:$DASHBOARD_PORT only"

# --- Monitoring ----------------------------------------------------------------
up=""
for _ in $(seq 1 60); do
  up=$(inside "http://127.0.0.1:9090/api/v1/query?query=sum(up%7Bjob%3D~%22email-platform-.*%22%7D)" | sed -nE 's/.*"value":\[[0-9.]+,"([0-9]+)"\].*/\1/p' || true)
  [[ "$up" == 2 ]] && break
  sleep 1
done
[[ "$up" == 2 ]] || fail "Prometheus does not see api and worker up (got '$up')"
rules=$(inside "http://127.0.0.1:9090/api/v1/rules?type=alert" | grep -o '"type":"alerting"' | wc -l | tr -d ' ')
want=$(grep -c '^      - alert:' "$ROOT/deploy/prometheus/alerts.yml")
[[ "$rules" == "$want" ]] || fail "Prometheus loaded $rules alert rules, want $want"
inside "http://127.0.0.1:9090/api/v1/alertmanagers" | grep -q '"url":"http://127.0.0.1:9093/api/v2/alerts"' || fail "Prometheus did not discover Alertmanager"
inside "http://127.0.0.1:9093/-/ready" >/dev/null || fail "Alertmanager not ready"
echo "monitoring -> api + worker up, $rules alert rules loaded, Alertmanager discovered"

# --- Instance metadata only for the application user ----------------------------
# Probes from the other services must be refused by the guard's rule itself
# (its packet counter rises); the application user is not probed, so nothing
# is ever sent to a real metadata service from the test machine.
rule='-d 169.254.169.254/32 -m owner ! --uid-owner 65532 -j REJECT'
dc exec -T netns iptables -w -S OUTPUT | grep -qF -- "$rule" || fail "netns has no metadata rule"
rejected() { dc exec -T netns iptables -w -L OUTPUT -v -x -n | awk '$3 == "REJECT" && $9 == "169.254.169.254" { print $1 }'; }
for svc in caddy dashboard prometheus alertmanager; do
  before=$(rejected)
  out=$(dc exec -T "$svc" wget -T 3 -qO /dev/null http://169.254.169.254/latest/meta-data/ 2>&1 || true)
  [[ "$out" == *refused* && "$(rejected)" -gt "$before" ]] || fail "$svc reaches the instance metadata address"
done
echo "instance metadata -> refused by the guard for caddy, dashboard, prometheus, alertmanager (only uid 65532 exempt)"
for svc in migrate api worker; do
  cid=$(dc ps -a -q "$svc" | head -1)
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | grep -qx 'AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true' ||
    fail "$svc runs without AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true"
done

# --- Container hardening -------------------------------------------------------
for svc in netns api worker dashboard caddy prometheus alertmanager; do
  cid=$(dc ps -q "$svc")
  [[ "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$cid")" == true ]] || fail "$svc root file system is writable"
  [[ "$(docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.PidsLimit}}' "$cid")" =~ ^[1-9][0-9]*\ [1-9][0-9]*$ ]] ||
    fail "$svc has no memory or process limit"
  [[ "$(docker inspect -f '{{.HostConfig.SecurityOpt}}' "$cid")" == *no-new-privileges* ]] || fail "$svc runs without no-new-privileges"
  [[ "$svc" == netns ]] && continue
  user=$(docker exec "$cid" id -u 2>/dev/null || docker inspect -f '{{.Config.User}}' "$cid")
  user="${user%%:*}"
  [[ -n "$user" && "$user" != 0 && "$user" != root ]] || fail "$svc runs as root"
done
echo "containers -> non-root (netns: root with NET_ADMIN only), read-only, limits, no-new-privileges"

dc logs --no-color 2>/dev/null | grep -qiFf "$CHECK_DIR/needles" && fail "logs contain a secret of this run"
echo "DEPLOY CHECK OK"
