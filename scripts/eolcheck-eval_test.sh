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

# 7. Nothing to check or no JSON: fail, never pass.
expect FAIL "empty" '[]'
expect FAIL "invalid json" '{'

if [[ $failures -gt 0 ]]; then echo "eolcheck-eval_test: $failures failure(s)" >&2; exit 1; fi
echo "eolcheck-eval_test: ok"
