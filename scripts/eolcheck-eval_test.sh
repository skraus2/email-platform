#!/usr/bin/env bash
# Tests scripts/eolcheck-eval.sh with hand-written endoflife.date release
# objects (no network): end of life, warning window and its boundaries,
# cycles without a date, unlisted cycles, end of active support.
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
mkdir -p "$repo/scripts" "$repo/apps/dashboard" "$repo/deploy/netns"
cp "$here/eolcheck.sh" "$repo/scripts/"
fixture() {
  printf 'module x\n\ngo 1.26.0\n\ntoolchain go1.27.3\n' >"$repo/go.mod"
  echo 24 >"$repo/.nvmrc"
  printf 'FROM golang:1.27.3-alpine@sha256:aa AS build\nFROM gcr.io/distroless/static-debian12:nonroot@sha256:bb\n' >"$repo/Dockerfile"
  printf 'FROM node:24.22.1-bookworm-slim@sha256:cc AS build\nFROM node:24.22.1-alpine3.24@sha256:dd\n' >"$repo/apps/dashboard/Dockerfile"
  printf 'FROM alpine:3.24@sha256:ee\n' >"$repo/deploy/netns/Dockerfile"
  printf 'services:\n  postgres:\n    image: postgres:18.6-alpine\n' >"$repo/compose.yaml"
  printf 'services:\n  prometheus:\n    image: prom/prometheus:v3.13.5@sha256:ff\n' >"$repo/deploy/compose.yaml"
}
list() { (cd "$repo" && ./scripts/eolcheck.sh --list) >"$work/out" 2>&1; }
fixture
list || fail "list: refused a complete tree: $(cat "$work/out")"
for want in "go 1.27 go.mod" "nodejs 24 .nvmrc" "debian 12 Dockerfile" "alpine-linux 3.24 apps/dashboard/Dockerfile" \
  "alpine-linux 3.24 deploy/netns/Dockerfile" "postgresql 18 compose.yaml" "prometheus 3.13 deploy/compose.yaml"; do
  grep -qx "$want" "$work/out" || fail "list: missing '$want': $(cat "$work/out")"
done
# Each shipped runtime base must name its cycle; nothing is skipped silently.
fixture; printf 'FROM alpine@sha256:ee\n' >"$repo/deploy/netns/Dockerfile"
list && fail "list: netns base without a version accepted"
grep -q "no alpine-linux version found in deploy/netns/Dockerfile" "$work/out" || fail "list: netns not named: $(cat "$work/out")"
fixture; printf 'FROM gcr.io/distroless/static:nonroot@sha256:bb\n' >"$repo/Dockerfile"
list && fail "list: distroless without a Debian version accepted"
fixture; rm "$repo/apps/dashboard/Dockerfile"
list && fail "list: missing Dockerfile accepted"
fixture; printf 'module x\n\ngo 1.26.0\n' >"$repo/go.mod"
list && fail "list: go.mod without toolchain accepted"

if [[ $failures -gt 0 ]]; then echo "eolcheck-eval_test: $failures failure(s)" >&2; exit 1; fi
echo "eolcheck-eval_test: ok"
