#!/usr/bin/env bash
# Tests deploy/aws/up, teardown and leftovers with a stub `aws` CLI on PATH
# (no AWS call): parameters and tags passed, confirmation required, deletion
# order, parameter values never read, leftovers found and failing checks
# counted.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/resp" "$work/fail" "$work/present"
failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# The stub appends its arguments to $WORK/calls. `<service> <operation>`
# prints $WORK/resp/<service>-<operation> if present and fails if
# $WORK/fail/<service>-<operation> exists; describe-stacks succeeds only
# for stacks with a file in $WORK/present.
cat >"$work/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$WORK/calls"
op="$1-$2"
[[ ! -e "$WORK/fail/$op" ]] || { echo "An error occurred (AccessDenied)" >&2; exit 254; }
case "$op" in
sts-get-caller-identity) printf '123456789012\tarn:aws:sts::123456789012:assumed-role/Admin/op\n'; exit 0 ;;
cloudformation-describe-stacks)
  while [[ $# -gt 0 ]]; do [[ "$1" == --stack-name ]] && stack="$2"; shift; done
  [[ -e "$WORK/present/$stack" ]] || { echo "An error occurred (ValidationError): Stack with id $stack does not exist" >&2; exit 254; } ;;
esac
[[ ! -f "$WORK/resp/$op" ]] || cat "$WORK/resp/$op"
STUB
chmod +x "$work/bin/aws"

conf="$work/testrun.local"
cat >"$conf" <<'CONF'
AWS_PROFILE=test-profile
AWS_REGION=eu-central-1
OPERATOR_EMAIL=ops@example.com
SES_DOMAIN=mail.example.com
SES_TEST_RECIPIENT=me@example.com
API_ALLOWED_CIDR=203.0.113.10/32
DB_ENGINE_VERSION=18.1
CONF

run() { # run SCRIPT INPUT ARGS...: output in $work/out, exit code returned
  local script="$1" input="$2"
  shift 2
  : >"$work/calls"
  printf '%s\n' "$input" | env -i PATH="$work/bin:/usr/bin:/bin" WORK="$work" TESTRUN_CONF="$conf" \
    bash "$here/$script" "$@" >"$work/out" 2>&1
}
called() { grep -qF -- "$1" "$work/calls"; }

# up: parameters, tags, capabilities; empty optional parameters left out.
touch "$work/present/email-platform-ses-identity" "$work/present/email-platform-budget"
run up yes ses-identity || fail "up ses-identity: $(cat "$work/out")"
called "cloudformation deploy --stack-name email-platform-ses-identity --template-file $here/ses-identity.yaml" ||
  fail "up: deploy call $(cat "$work/calls")"
called "--parameter-overrides SesDomain=mail.example.com RecipientEmail=me@example.com --capabilities CAPABILITY_NAMED_IAM --tags project=email-platform" ||
  fail "up: parameters/tags $(cat "$work/calls")"
called MailFromDomain && fail "up: empty MailFromDomain passed"
run up yes budget || fail "up budget: $(cat "$work/out")"
called "LimitUsd=10 AlertEmail=ops@example.com" || fail "up budget: $(cat "$work/calls")"

# up: refused without "yes", with a missing setting or an unknown stack.
run up no budget && fail "up ran without confirmation"
called "cloudformation deploy" && fail "up deployed without confirmation"
sed '/DB_ENGINE_VERSION/d' "$conf" >"$work/partial" && cp "$conf" "$work/full" && cp "$work/partial" "$conf"
run up yes platform && fail "up platform ran without DB_ENGINE_VERSION"
grep -q "DB_ENGINE_VERSION must be set" "$work/out" || fail "up: missing setting not named: $(cat "$work/out")"
[[ ! -s "$work/calls" ]] || fail "up called aws before validating the settings"
cp "$work/full" "$conf"
run up yes nonsense && fail "up accepted an unknown stack"

# up platform: the image is resolved once and passed pinned; the sandbox
# recipient is passed along.
echo ami-0123456789abcdef0 >"$work/resp/ssm-get-parameter"
touch "$work/present/email-platform-platform"
run up yes platform || fail "up platform: $(cat "$work/out")"
called "ImageId=ami-0123456789abcdef0" && called "RecipientEmail=me@example.com" || fail "up platform: $(cat "$work/calls")"
grep -q "add PLATFORM_IMAGE_ID=ami-0123456789abcdef0" "$work/out" || fail "up platform: pinning hint missing"
called DbBackupRetentionDays && fail "up platform: empty backup retention passed"
printf 'PLATFORM_IMAGE_ID=ami-0fedcba9876543210\nDB_BACKUP_RETENTION_DAYS=14\n' >>"$conf"
run up yes platform || fail "up platform pinned: $(cat "$work/out")"
called "ssm get-parameter" && fail "up platform resolved the image although it is pinned"
called "ImageId=ami-0fedcba9876543210" || fail "up platform: pinned image not passed"
called "DbBackupRetentionDays=14" || fail "up platform: backup retention not passed"
cp "$work/full" "$conf"
rm "$work/resp/ssm-get-parameter" "$work/present/email-platform-platform"

# up and teardown refuse static keys in the environment.
echo AWS_ACCESS_KEY_ID=AKIASTATIC >>"$conf"
run up yes budget && fail "up ran with a static key"
run teardown 123456789012 && fail "teardown ran with a static key"
[[ ! -s "$work/calls" ]] || fail "aws called despite a static key"
cp "$work/full" "$conf"
rm "$work/present/email-platform-ses-identity"

# teardown: wrong account id deletes nothing.
printf '/email-platform/prod/DATABASE_URL\t/email-platform/prod/WEBHOOK_KEKS\n' >"$work/resp/ssm-describe-parameters"
touch "$work/present/email-platform-platform" "$work/present/email-platform-ses-test" \
  "$work/present/email-platform-ses-prod" "$work/present/email-platform-budget"
run teardown 999999999999 && fail "teardown ran with a wrong account id"
grep -q delete "$work/calls" && fail "teardown deleted without confirmation"
grep -q "deletes SSM parameters: /email-platform/prod/DATABASE_URL" "$work/out" || fail "teardown: names not shown before the prompt"

# teardown: a broad SSM prefix is refused before any call.
echo SSM_PREFIX=/email-platform/ >>"$conf"
run teardown 123456789012 && fail "teardown accepted a broad SSM prefix"
[[ ! -s "$work/calls" ]] || fail "teardown called aws with a broad prefix"
cp "$work/full" "$conf"

# teardown: parameters, then stacks in reverse order; absent stacks skipped;
# budget kept; leftovers run at the end.
run teardown 123456789012 || true
deletes="$(grep -E 'delete-parameter|delete-stack' "$work/calls" | awk '{print $NF}' | tr '\n' ' ')"
want="/email-platform/prod/DATABASE_URL /email-platform/prod/WEBHOOK_KEKS email-platform-platform email-platform-ses-test email-platform-ses-prod "
[[ "$deletes" == "$want" ]] || fail "teardown order: got '$deletes', want '$want'"
grep -q "email-platform-ses-identity: not present" "$work/out" || fail "teardown: absent stack not reported"
grep -qE 'with-decryption|get-parameters-by-path' "$work/calls" && fail "teardown fetched parameter values"
called "cloudformation wait stack-delete-complete --stack-name email-platform-ses-prod" || fail "teardown did not wait"
called "cloudformation list-stacks" || fail "teardown did not run leftovers"
run teardown 123456789012 --with-budget || true
[[ "$(grep delete-stack "$work/calls" | tail -1 | awk '{print $NF}')" == email-platform-budget ]] ||
  fail "teardown --with-budget: budget not deleted last"
rm "$work/resp/ssm-describe-parameters"

# teardown: an unexplained describe-stacks error stops the run.
touch "$work/fail/cloudformation-describe-stacks"
run teardown 123456789012 && fail "teardown went on although describe-stacks failed"
grep -q "cannot tell whether it exists" "$work/out" || fail "teardown: describe error not named: $(cat "$work/out")"
grep -q delete-stack "$work/calls" && fail "teardown deleted a stack after an unexplained error"
rm "$work/fail/cloudformation-describe-stacks"

# teardown: a stack that fails to delete stops the run.
touch "$work/fail/cloudformation-wait"
run teardown 123456789012 && fail "teardown succeeded although a stack did not delete"
grep -q "email-platform-platform did not delete" "$work/out" || fail "teardown: failed delete not named: $(cat "$work/out")"
[[ "$(grep -c delete-stack "$work/calls")" == 1 ]] || fail "teardown continued after a failed delete"
rm "$work/fail/cloudformation-wait"

# leftovers: clean region passes; the budget stack alone is info.
run leftovers "" || fail "leftovers on a clean region: $(cat "$work/out")"
printf 'email-platform-budget\tCREATE_COMPLETE\n' >"$work/resp/cloudformation-list-stacks"
run leftovers "" || fail "leftovers failed on the budget stack alone: $(cat "$work/out")"
grep -q "^info  budget stack" "$work/out" || fail "leftovers: budget not listed as info"

# leftovers: billable resources fail the run and are listed.
printf 'email-platform-platform\tDELETE_FAILED\n' >>"$work/resp/cloudformation-list-stacks"
printf '198.51.100.7\teipalloc-1\n' >"$work/resp/ec2-describe-addresses"
run leftovers "" && fail "leftovers passed with a stack and an Elastic IP left"
grep -q "^LEFT  CloudFormation stacks" "$work/out" && grep -q "^LEFT  Elastic IPs" "$work/out" && grep -q "eipalloc-1" "$work/out" ||
  fail "leftovers output: $(cat "$work/out")"
rm "$work/resp/cloudformation-list-stacks" "$work/resp/ec2-describe-addresses"

# leftovers: KMS keys pending deletion are info, enabled keys are left.
echo "arn:aws:kms:eu-central-1:123456789012:key/k1" >"$work/resp/resourcegroupstaggingapi-get-resources"
printf 'PendingDeletion\t2026-11-05\n' >"$work/resp/kms-describe-key"
run leftovers "" || fail "leftovers failed on a key pending deletion: $(cat "$work/out")"
grep -q "^info  KMS keys pending deletion" "$work/out" || fail "leftovers: pending key not info"
printf 'Enabled\tNone\n' >"$work/resp/kms-describe-key"
run leftovers "" && fail "leftovers passed with an enabled key"
rm "$work/resp/resourcegroupstaggingapi-get-resources" "$work/resp/kms-describe-key"

# leftovers: a failing tagging call is a leftover, not a list of key ids.
touch "$work/fail/resourcegroupstaggingapi-get-resources"
run leftovers "" && fail "leftovers passed although the key lookup failed"
grep -A2 "^LEFT  KMS keys not scheduled" "$work/out" | grep -q "check failed" || fail "leftovers: key lookup failure not shown"
called "kms describe-key" && fail "leftovers described words of an error message as keys"
rm "$work/fail/resourcegroupstaggingapi-get-resources"

# leftovers: a failing check counts as a leftover.
touch "$work/fail/rds-describe-db-instances"
run leftovers "" && fail "leftovers passed although a check failed"
grep -A2 "^LEFT  RDS instances" "$work/out" | grep -q "check failed" || fail "leftovers: failed check not shown: $(cat "$work/out")"
rm "$work/fail/rds-describe-db-instances"

# Templates: every policy statement starts with a Sid (SNS rejects topic
# policies without unique ones; the others follow suit).
if grep -nE '^[[:space:]]*- (Effect|Action|Principal|Resource|Condition):' "$here"/*.yaml; then
  fail "policy statement without Sid (listed above)"
fi

if [[ "$failures" -gt 0 ]]; then
  echo "deploy/aws scripts: $failures failure(s)" >&2
  exit 1
fi
echo "deploy/aws scripts: all checks passed"
