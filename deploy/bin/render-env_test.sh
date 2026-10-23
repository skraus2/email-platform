#!/usr/bin/env bash
# Tests deploy/bin/render-env with a stub `aws` CLI on PATH (no AWS call).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

# The stub prints $work/params (Name<TAB>Value lines) and records its
# arguments and environment; exit code from $work/exit.
cat >"$work/bin/aws" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$WORK/args"
env >"$WORK/env"
cat "$WORK/params"
exit "$(cat "$WORK/exit" 2>/dev/null || echo 0)"
STUB
chmod +x "$work/bin/aws"

secret_db='postgres://app:pa55w0rd-SECRET@db.example.internal:5432/ep?sslmode=verify-full&sslrootcert=/etc/email-platform/db-ca.pem'
secret_kek='k1:c2VjcmV0LWtleS1lbmNyeXB0aW9uLWtleS0zMmJ5dGVzIQ=='
params() { printf '%s\n' "$@" >"$work/params"; }
# A forgotten `aws configure` of the operator: must not be used.
mkdir -p "$work/home/.aws"
printf '[default]\naws_access_key_id = AKIASTATICKEY\naws_secret_access_key = static\n' >"$work/home/.aws/credentials"
render() {
  env -i PATH="$work/bin:/usr/bin:/bin" HOME="$work/home" WORK="$work" AWS_REGION=eu-central-1 APP_ENV_FILE="$work/run/app.env" "$@" \
    bash "$here/render-env" >"$work/stdout" 2>"$work/stderr"
}
leaked() { grep -qF -e "pa55w0rd-SECRET" -e "c2VjcmV0" "$work/stdout" "$work/stderr"; }

# 1. Success: file, mode, quoting, names printed, values not.
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek" "/email-platform/prod/AWS_REGION	eu-central-1"
render || fail "valid parameters refused: $(cat "$work/stderr")"
[[ -f "$work/run/app.env" ]] || fail "no env file written"
mode=$(stat -c %a "$work/run/app.env" 2>/dev/null || stat -f %Lp "$work/run/app.env")
[[ "$mode" == 600 ]] || fail "env file mode $mode, want 600"
grep -qxF "DATABASE_URL='$secret_db'" "$work/run/app.env" || fail "DATABASE_URL not written literally"
grep -qxF "WEBHOOK_KEKS='$secret_kek'" "$work/run/app.env" || fail "WEBHOOK_KEKS not written"
grep -q 'wrote 3 variables' "$work/stdout" && grep -q 'DATABASE_URL WEBHOOK_KEKS AWS_REGION' "$work/stdout" || fail "summary: $(cat "$work/stdout")"
leaked && fail "a value reached stdout/stderr"
grep -qx -- '--with-decryption' "$work/args" && grep -qx '/email-platform/prod/' "$work/args" && grep -qx 'eu-central-1' "$work/args" ||
  fail "aws called with $(tr '\n' ' ' <"$work/args")"
[[ -z "$(find "$work/run" -name '.app.env.*')" ]] || fail "temporary files left behind"
render HTTPS_PROXY=http://proxy.example:3128 AWS_CA_BUNDLE=/x || fail "proxy settings refused: $(cat "$work/stderr")"
grep -qE '^(HTTPS_PROXY|AWS_CA_BUNDLE)=' "$work/env" && fail "aws ran with a proxy or a custom CA bundle"
for want in AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_CONFIG_FILE=/dev/null AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true AWS_EC2_METADATA_V1_DISABLED=true; do
  grep -qxF "$want" "$work/env" || fail "aws ran without $want"
done

# 2. Failures keep the previous file and leak nothing.
before="$(cat "$work/run/app.env")"
check_refused() {
  local name="$1"
  shift
  if render "$@"; then fail "$name: accepted"; fi
  [[ "$(cat "$work/run/app.env")" == "$before" ]] || fail "$name: previous env file changed"
  leaked && fail "$name: a value reached stdout/stderr"
  [[ -z "$(find "$work/run" -name '.app.env.*')" ]] || fail "$name: temporary files left behind"
}
params "/email-platform/prod/DATABASE_URL	$secret_db"
check_refused "missing WEBHOOK_KEKS"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	"
check_refused "empty value"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek" "/email-platform/prod/bad-name	x"
check_refused "invalid name"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	it's"
check_refused "single quote"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek"
check_refused "duplicate"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	k1:first-line" "c2VjcmV0-continued"
check_refused "value with a line break"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	k1:first-line" "" "/email-platform/prod/LOG_LEVEL	info"
check_refused "blank line"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek" "/email-platform/prod/AWS_ACCESS_KEY_ID	AKIAEXAMPLE"
check_refused "AWS credential as a parameter"
params "/email-platform/prod/DATABASE_URL	$secret_db" "/email-platform/prod/WEBHOOK_KEKS	$secret_kek"
echo 255 >"$work/exit"
check_refused "aws failure"
rm -f "$work/exit"
for v in AWS_ACCESS_KEY_ID=AKIAEXAMPLE AWS_SECRET_ACCESS_KEY=x AWS_SESSION_TOKEN=x AWS_PROFILE=default AWS_SHARED_CREDENTIALS_FILE=/x AWS_CONFIG_FILE=/x \
  AWS_EC2_METADATA_DISABLED=true AWS_WEB_IDENTITY_TOKEN_FILE=/x AWS_ROLE_ARN=arn:aws:iam::1:role/x AWS_CONTAINER_CREDENTIALS_FULL_URI=http://x \
  AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/x AWS_ENDPOINT_URL=https://evil.example AWS_ENDPOINT_URL_SSM=https://evil.example; do
  rm -f "$work/args"
  check_refused "static credentials ($v)" "$v"
  [[ ! -f "$work/args" ]] || fail "$v: aws was called anyway"
done
if env -i PATH="$work/bin:/usr/bin:/bin" WORK="$work" APP_ENV_FILE="$work/run/app.env" bash "$here/render-env" >/dev/null 2>&1; then
  fail "missing AWS_REGION accepted"
fi

if [[ $failures -gt 0 ]]; then
  echo "render-env tests: $failures failure(s)" >&2
  exit 1
fi
echo "render-env tests OK"
