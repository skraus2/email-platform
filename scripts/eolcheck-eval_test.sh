#!/usr/bin/env bash
# Tests scripts/eolcheck-eval.sh with hand-written endoflife.date release
# objects (no network): end of life, warning window and its boundaries,
# cycles without a date, unlisted cycles, end of active support; and
# eolcheck.sh reading the pinned cycles (--list) and refusing malformed
# --cycles input before any lookup.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

TODAY=2026-10-24
# cycle <product> <eolFrom|null> [isEol] [eoasFrom]: one entry as eolcheck.sh writes it
cycle() {
  local eol='null' is="${3:-false}" eoas='null' iseoas=false
  [[ "$2" != null ]] && eol="\"$2\""
  [[ -n "${4:-}" ]] && { eoas="\"$4\""; iseoas=true; }
  printf '{"product":"%s","cycle":"1","source":"file","release":{"name":"1","isEol":%s,"eolFrom":%s,"isEoas":%s,"eoasFrom":%s}}' \
    "$1" "$is" "$eol" "$iseoas" "$eoas"
}
# expect <PASS|FAIL> <case> <json>; output in $work/out
expect() {
  local want="$1" name="$2" got=PASS
  printf '%s' "$3" >"$work/in.json"
  "$here/eolcheck-eval.sh" "$work/in.json" "$TODAY" 90 >"$work/out" 2>&1 || got=FAIL
  [[ "$got" == "$want" ]] || fail "$name: got $got, want $want: $(cat "$work/out")"
}

# 1. Far from end of life, and no date announced: pass.
expect PASS "supported" "[$(cycle go 2028-01-01), $(cycle nodejs null)]"
grep -q "^OK.go 1.end of life on 2028-01-01" "$work/out" || fail "supported: row wrong: $(cat "$work/out")"
grep -q "no end-of-life date announced" "$work/out" || fail "no date: not said: $(cat "$work/out")"
grep -qx "eolcheck: PASS" "$work/out" || fail "supported: verdict wrong: $(cat "$work/out")"

# 2. Inside the warning window: pass with a warning naming the days left.
expect PASS "warning" "[$(cycle prometheus 2026-11-06)]"
grep -q "^WARN.prometheus 1.end of life on 2026-11-06 (in 13 days)" "$work/out" || fail "warning: row wrong: $(cat "$work/out")"
grep -qx "eolcheck: PASS with warnings" "$work/out" || fail "warning: verdict wrong: $(cat "$work/out")"

# 3. Window boundaries: exactly 90 days warns, 91 days does not.
expect PASS "90 days" "[$(cycle a 2027-01-22)]"
grep -q "^WARN" "$work/out" || fail "90 days: no warning: $(cat "$work/out")"
expect PASS "91 days" "[$(cycle a 2027-01-23)]"
grep -q "^OK" "$work/out" || fail "91 days: warned: $(cat "$work/out")"

# 4. End of life today, in the past, or flagged without a date: fail.
expect FAIL "eol today" "[$(cycle a 2026-10-24)]"
expect FAIL "eol past" "[$(cycle go 2028-01-01), $(cycle alpine-linux 2026-05-01)]"
grep -q "^FAIL.alpine-linux 1.end of life since 2026-05-01" "$work/out" || fail "eol past: row wrong: $(cat "$work/out")"
expect FAIL "flagged eol" "[$(cycle a null true)]"

# 5. A cycle endoflife.date does not list cannot be checked: fail.
expect FAIL "not listed" '[{"product":"go","cycle":"9.9","source":"go.mod","release":null}]'
grep -q "cycle not listed" "$work/out" || fail "not listed: not said: $(cat "$work/out")"

# 6. Past active support: still OK, but said.
expect PASS "eoas" "[$(cycle debian 2028-06-30 false 2026-07-11)]"
grep -q "active support ended 2026-07-11, security fixes only" "$work/out" || fail "eoas: not said: $(cat "$work/out")"

# 6b. Untrusted API text: a date with trailing text fails, control
# characters never reach the output (they could start a CI command).
expect FAIL "date with trailing text" '[{"product":"a","cycle":"1","source":"f","release":{"isEol":false,"eolFrom":"2027-12-01\n::error::x","isEoas":false}}]'
expect PASS "control characters" '[{"product":"a\u001b","cycle":"1","source":"f\n::error::x","release":{"isEol":false,"eolFrom":"2027-12-01","isEoas":true,"eoasFrom":"2026-01-01\n::stop-commands::y"}}]'
grep -q '^::' "$work/out" && fail "control characters: workflow command line in output: $(cat "$work/out")"
[[ $(wc -l <"$work/out") -eq 2 ]] || fail "control characters: extra lines: $(cat "$work/out")"

# 7. Nothing to check or no JSON: fail, never pass.
expect FAIL "empty" '[]'
expect FAIL "invalid json" '{'

# 8. Reading the cycles from the files (eolcheck.sh --list, no network).
repo="$work/repo"
mkdir -p "$repo/scripts" "$repo/deploy"
cp "$here/eolcheck.sh" "$repo/scripts/"
fixture() {
  printf 'module x\n\ngo 1.26.0\n\ntoolchain go1.27.3\n' >"$repo/go.mod"
  echo 24 >"$repo/.nvmrc"
  printf 'services:\n  postgres:\n    image: postgres:18.6-alpine\n' >"$repo/compose.yaml"
  printf 'services:\n  prometheus:\n    image: prom/prometheus:v3.13.5@sha256:ff\n' >"$repo/deploy/compose.yaml"
}
list() { (cd "$repo" && ./scripts/eolcheck.sh --list) >"$work/out" 2>&1; }
fixture
list || fail "list: refused a complete tree: $(cat "$work/out")"
for want in "go 1.27 go.mod" "nodejs 24 .nvmrc" "postgresql 18 compose.yaml" "prometheus 3.13 deploy/compose.yaml"; do
  grep -qx "$want" "$work/out" || fail "list: missing '$want': $(cat "$work/out")"
done
[[ $(wc -l <"$work/out") -eq 4 ]] || fail "list: unexpected entries: $(cat "$work/out")"
# The image operating systems come from the image scan, not from Dockerfiles.
printf 'FROM gcr.io/distroless/static-debian12:nonroot@sha256:bb\n' >"$repo/Dockerfile"
list && grep -qE '^(debian|alpine-linux) ' "$work/out" && fail "list: operating system read from a Dockerfile: $(cat "$work/out")"
fixture; printf 'module x\n\ngo 1.26.0\n' >"$repo/go.mod"
list && fail "list: go.mod without toolchain accepted"
grep -q "no go version found in go.mod" "$work/out" || fail "list: go.mod not named: $(cat "$work/out")"
fixture; printf 'services:\n  prometheus:\n    image: prom/prometheus:latest\n' >"$repo/deploy/compose.yaml"
list && fail "list: prometheus without a version accepted"

# 9. --cycles (the image operating systems): every line is checked before
# any lookup; a malformed one, an empty file or a missing file fails
# without network.
cycles() { printf "$1" >"$work/cycles"; (cd "$repo" && ./scripts/eolcheck.sh --cycles "$work/cycles") >"$work/out" 2>&1; }
for bad in 'alpine-linux 3.23\n' 'alpine-linux 3.23.7 caddy image\n' 'alpine-linux edge caddy image\n' '../go 1.27 x\n' \
  'Debian 12 app image\n' 'debian 12 app\001image\n' 'debian 12 app image\nnodejs\n'; do
  cycles "$bad" && fail "cycles: accepted '$bad': $(cat "$work/out")"
  grep -q 'not "<product> <cycle> <source>"' "$work/out" || fail "cycles '$bad': not named: $(cat "$work/out")"
done
cycles '' && fail "cycles: empty file accepted"
(cd "$repo" && ./scripts/eolcheck.sh --cycles "$work/missing") >"$work/out" 2>&1 && fail "cycles: missing file accepted"

if [[ $failures -gt 0 ]]; then echo "eolcheck-eval_test: $failures failure(s)" >&2; exit 1; fi
echo "eolcheck-eval_test: ok"
