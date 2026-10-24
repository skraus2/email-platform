#!/usr/bin/env bash
# Tests scripts/imagecheck-eval.sh with hand-written grype reports (no
# Docker, no network): fail threshold, ignore rules, rule validation,
# revisit dates and the stale-rule check.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
# The date the evaluator runs on; rule reasons carry a later revisit date.
today=2026-11-24
later='revisit by 2026-11-25'

builtin_rule='{"vulnerability":"","reason":"","namespace":"","fix-state":"","package":{"name":"linux-libc-dev","version":"","type":"deb","location":"","upstream-name":"linux"},"match-type":"exact-indirect-match"}'
# match <id> <severity> <fix state> [fix version]
match() {
  local versions='[]'
  [[ -n "${4:-}" ]] && versions="[\"$4\"]"
  printf '{"vulnerability":{"id":"%s","severity":"%s","fix":{"state":"%s","versions":%s}},"artifact":{"name":"pkg","version":"1.0"}}' "$1" "$2" "$3" "$versions"
}
# rule <id> <reason> [location]: as the config echoes it (every field)
rule() {
  printf '{"vulnerability":"%s","include-aliases":false,"reason":"%s","namespace":"","fix-state":"","package":{"name":"","version":"","language":"","type":"","location":"%s","upstream-name":""},"vex-status":"","vex-justification":"","match-type":""}' "$1" "$2" "${3:-}"
}
# applied <id> <reason> [location]: as a match's appliedIgnoreRules echoes it
applied() {
  if [[ -n "${3:-}" ]]; then
    printf '{"vulnerability":"%s","reason":"%s","namespace":"","package":{"language":"","location":"%s"}}' "$1" "$2" "$3"
  else
    printf '{"vulnerability":"%s","reason":"%s","namespace":""}' "$1" "$2"
  fi
}
# report <file> <rules json array> <matches json array> [ignored matches json array]
# The configuration echoes grype's defaults; app.json carries the app
# image's exclusion. CONFIG='"key":value,' overrides a setting.
report() {
  mkdir -p "$(dirname "$1")"
  local exclude='[]'
  [[ "$(basename "$1")" == app.json ]] && exclude='["/app/*"]'
  printf '{"descriptor":{"name":"grype","version":"0.121.0","configuration":{"exclude":%s,"only-fixed":false,"only-notfixed":false,"ignore-wontfix":"","vex-documents":[],"search":{"scope":"squashed"},"match":{"stock":{"using-cpes":true}},%s"ignore":%s},"db":{"status":{"built":"2026-10-23T06:30:15Z"}}},"matches":%s,"ignoredMatches":%s}' \
    "$exclude" "${CONFIG:-}" "$2" "$3" "${4:-[]}" >"$1"
}
# expect <PASS|FAIL> <case> <files...>; output in $work/out
expect() {
  local want="$1" name="$2"; shift 2
  local got=PASS
  "$here/imagecheck-eval.sh" "$today" "$@" >"$work/out" 2>&1 || got=FAIL
  [[ "$got" == "$want" ]] || fail "$name: got $got, want $want: $(cat "$work/out")"
}

# 1. Clean images pass; built-in rules are not checked.
report "$work/1/a.json" "[$builtin_rule]" '[]'
report "$work/1/b.json" "[$builtin_rule]" "[$(match CVE-1 Medium fixed 2.0)]"
expect PASS "clean" "$work/1/a.json" "$work/1/b.json"
grep -q "2 images, 1 findings" "$work/out" || fail "clean: summary wrong: $(cat "$work/out")"

# 2. High with a fix fails, named with its image.
report "$work/2/caddy.json" '[]' "[$(match GO-2 High fixed 1.26.9)]"
expect FAIL "high fixed" "$work/2/caddy.json"
grep -q "caddy.GO-2.High.pkg.1.0.fix 1.26.9" "$work/out" || fail "high fixed: finding not listed: $(cat "$work/out")"

# 3. Critical with a fix fails.
report "$work/3/a.json" '[]' "[$(match CVE-3 Critical fixed 3.1)]"
expect FAIL "critical fixed" "$work/3/a.json"

# 4. High/Critical without a fix (unknown, not-fixed, wont-fix) are reported only.
report "$work/4/a.json" '[]' "[$(match CVE-4 High unknown), $(match CVE-5 Critical not-fixed), $(match CVE-6 High wont-fix)]"
expect PASS "no fix" "$work/4/a.json"
grep -q "reported (no fix available yet)" "$work/out" && grep -q CVE-6 "$work/out" || fail "no fix: not reported: $(cat "$work/out")"

# 5. Below High with a fix passes (Medium, Low, Negligible, Unknown) and is listed.
report "$work/5/a.json" '[]' "[$(match C-7 Medium fixed 1), $(match C-8 Low fixed 1), $(match C-14 Negligible fixed 1), $(match C-9 Unknown fixed 1)]"
expect PASS "below high" "$work/5/a.json"
grep -q "fix available, below High" "$work/out" && grep -q "C-9.Unknown" "$work/out" || fail "below high: fixable finding not listed: $(cat "$work/out")"

# 5b. Findings and applied rules count in every image, not only the first.
report "$work/5b/a.json" '[]' '[]'
report "$work/5b/b.json" '[]' "[$(match GO-15 High fixed 1.2)]"
expect FAIL "high in second image" "$work/5b/a.json" "$work/5b/b.json"

# 6. An ignore rule that suppressed a finding in one image passes and shows its reason.
reason6="stdlib path not used by the binary; $later"
r6="$(rule GO-10 "$reason6")"
a6="$(applied GO-10 "$reason6")"
report "$work/6/a.json" "[$builtin_rule, $r6]" '[]' "[$(match GO-10 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$a6]}]"
report "$work/6/b.json" "[$builtin_rule, $r6]" '[]'
expect PASS "ignored" "$work/6/a.json" "$work/6/b.json"
grep -q "not used by the binary; $later" "$work/out" || fail "ignored: reason not shown: $(cat "$work/out")"

# 7. A rule that matched nothing in any image is stale.
r7="$(rule GO-11 "old finding; $later")"
report "$work/7/a.json" "[$r6, $r7]" '[]' "[$(match GO-10 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$a6]}]"
expect FAIL "stale" "$work/7/a.json"
grep -q "stale ignore rules" "$work/out" && grep -q "GO-11: old finding; $later" "$work/out" || fail "stale: not named: $(cat "$work/out")"
grep -q "GO-10" <(sed -n '/stale ignore rules/,$p' "$work/out") && fail "stale: applied rule GO-10 reported stale: $(cat "$work/out")"

# 7b. Rules scoped by location: the same ID at a second location that
# matched nothing is stale on its own, named with its location.
ra="$(rule GO-13 "internal only; $later" /bin/a)"
rb="$(rule GO-13 "internal only; $later" /bin/b)"
report "$work/7b/a.json" "[$builtin_rule, $ra, $rb]" '[]' "[$(match GO-13 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-13 "internal only; $later" /bin/a)]}]"
expect FAIL "stale location" "$work/7b/a.json"
grep -q "GO-13 at /bin/b" "$work/out" && ! grep -q "GO-13 at /bin/a" "$work/out" || fail "stale location: wrong rule named: $(cat "$work/out")"
report "$work/7c/a.json" "[$ra, $rb]" '[]' "[$(match GO-13 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-13 "internal only; $later" /bin/a)]}, $(match GO-13 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-13 "internal only; $later" /bin/b)]}]"
expect PASS "both locations applied" "$work/7c/a.json"

# 8. Rules without a reason or without a vulnerability ID are refused.
r8="$(rule GO-10 '')"
report "$work/8/a.json" "[$r8]" '[]' "[$(match GO-10 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-10 '')]}]"
expect FAIL "no reason" "$work/8/a.json"
r9="$(rule '' "whole package; $later")"
report "$work/9/a.json" "[$r9]" '[]' "[$(match GO-12 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied '' "whole package; $later")]}]"
expect FAIL "no vulnerability" "$work/9/a.json"
grep -q "need a vulnerability ID and a reason" "$work/out" || fail "no vulnerability: not named: $(cat "$work/out")"

r16="$(rule GO-16 "second image only; $later")"
report "$work/7d/a.json" "[$r16]" '[]'
report "$work/7d/b.json" "[$r16]" '[]' "[$(match GO-16 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-16 "second image only; $later")]}]"
expect PASS "rule applied in second image" "$work/7d/a.json" "$work/7d/b.json"

# 8b. A user rule shaped like a built-in one is still checked.
fake='{"vulnerability":"","reason":"","namespace":"","fix-state":"","package":{"name":"","version":"","type":"","location":"","upstream-name":"openssl"},"match-type":"exact-indirect-match"}'
report "$work/8b/a.json" "[$builtin_rule, $fake]" '[]'
expect FAIL "fake built-in rule" "$work/8b/a.json"

# 9. Anything but complete grype reports fails: invalid JSON, an empty
# file, a JSON document that is not a grype report, two reports for one image.
echo '{' >"$work/bad.json"
expect FAIL "invalid json" "$work/bad.json"
report "$work/9b/a.json" '[]' '[]'
: >"$work/9b/b.json"
expect FAIL "empty file" "$work/9b/a.json" "$work/9b/b.json"
grep -q "empty file" "$work/out" || fail "empty file: not named: $(cat "$work/out")"
echo '{}' >"$work/9b/c.json"
expect FAIL "not a grype report" "$work/9b/a.json" "$work/9b/c.json"
grep -q "not a complete grype report" "$work/out" || fail "not a grype report: not named: $(cat "$work/out")"
printf '{"descriptor":{"name":"grype","configuration":{"ignore":[]},"db":{"status":{"built":"x"}}},"results":[]}' >"$work/9b/d.json"
expect FAIL "renamed matches" "$work/9b/a.json" "$work/9b/d.json"
report "$work/9c/a.json" '[]' '[]'
report "$work/9d/a.json" '[]' '[]'
expect FAIL "same image twice" "$work/9c/a.json" "$work/9d/a.json"

# 10. Settings that drop findings outside the ignore rules fail.
for c in '"only-fixed":true,' '"only-notfixed":true,' '"ignore-wontfix":"fixed",' '"vex-documents":["v.json"],' '"search":{"scope":"all-layers"},' '"match":{"stock":{"using-cpes":false}},'; do
  CONFIG="$c" report "$work/10/a.json" '[]' '[]'
  expect FAIL "config $c" "$work/10/a.json"
done
CONFIG='"exclude":["/usr/bin/*"],' report "$work/10/caddy.json" '[]' '[]'
expect FAIL "exclude outside the app image" "$work/10/caddy.json"
report "$work/10b/app.json" '[]' '[]'
expect PASS "app exclusion" "$work/10b/app.json"
report "$work/10c/a.json" '[]' '[]'
report "$work/10c/b.json" "[$(rule GO-17 "only here; $later")]" '[]' "[$(match GO-17 High fixed 1 | sed 's/}$//'), \"appliedIgnoreRules\":[$(applied GO-17 "only here; $later")]}]"
expect FAIL "different rule sets" "$work/10c/a.json" "$work/10c/b.json"

# 11. Every rule names a valid revisit date after today (UTC date given by
# the caller); the rule and the date are named.
# dated <case> <reason>: one image whose only rule is GO-18 at /bin/x,
# applied to a finding, so the date is the only thing under test.
dated() {
  local r a
  r="$(rule GO-18 "$2" /bin/x)"
  a="$(applied GO-18 "$2" /bin/x)"
  report "$work/11/$1/a.json" "[$builtin_rule, $r]" '[]' "[$(match GO-18 High fixed 1.2 | sed 's/}$//'), \"appliedIgnoreRules\":[$a]}]"
}
dated future 'not reachable here; revisit by 2026-11-25.'
expect PASS "revisit date tomorrow" "$work/11/future/a.json"
dated missing 'not reachable here; remove with the next release'
expect FAIL "no revisit date" "$work/11/missing/a.json"
grep -qF '"revisit by YYYY-MM-DD" in the reason' "$work/out" && grep -qF "GO-18 at /bin/x: no revisit date" "$work/out" ||
  fail "no revisit date: not named: $(cat "$work/out")"
for bad in 2026-1-5 2026-02-30 2026-13-01 20261-01-01 24.11.2026 soon; do
  dated "bad-$bad" "not reachable here; revisit by $bad."
  expect FAIL "revisit date $bad" "$work/11/bad-$bad/a.json"
  grep -qF "GO-18 at /bin/x: revisit date \"$bad\" is not YYYY-MM-DD" "$work/out" ||
    fail "revisit date $bad: not named: $(cat "$work/out")"
done
dated today "not reachable here; revisit by $today"
expect FAIL "revisit date today" "$work/11/today/a.json"
grep -qF "reached their revisit date" "$work/out" && grep -qF "GO-18 at /bin/x: revisit by $today, today is $today" "$work/out" ||
  fail "revisit date today: not named: $(cat "$work/out")"
dated past 'not reachable here; revisit by 2025-12-31'
expect FAIL "revisit date past" "$work/11/past/a.json"
grep -qF "GO-18 at /bin/x: revisit by 2025-12-31, today is $today" "$work/out" ||
  fail "revisit date past: not named: $(cat "$work/out")"
# A later year with a smaller month and day is later.
dated next-year 'not reachable here; revisit by 2027-01-01'
expect PASS "revisit date next year" "$work/11/next-year/a.json"
# The caller's date is validated too.
"$here/imagecheck-eval.sh" 2026-11-31 "$work/11/future/a.json" >"$work/out" 2>&1 &&
  fail "invalid today accepted: $(cat "$work/out")"
grep -qF 'today "2026-11-31" is not a YYYY-MM-DD date' "$work/out" || fail "invalid today: not named: $(cat "$work/out")"

if [[ $failures -gt 0 ]]; then echo "imagecheck-eval_test: $failures failure(s)" >&2; exit 1; fi
echo "imagecheck-eval_test: ok"
