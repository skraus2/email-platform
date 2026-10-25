#!/usr/bin/env bash
# Tests scripts/imagecheck-os.sh with hand-written grype reports (no
# Docker, no network): distribution -> endoflife.date cycle mapping,
# unknown and unmapped operating systems, the OSLESS allowlist and its
# stale check.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

# report <dir> <image> <distro json>: the parts of a grype report read here
report() { mkdir -p "$work/$1"; printf '{"descriptor":{"name":"grype"},"matches":[],"distro":%s}' "$3" >"$work/$1/$2.json"; }
distro() { printf '{"name":"%s","version":"%s","idLike":[]}' "$1" "$2"; }
# The scanned stack as grype reports it today: the OSLESS images included.
stack() {
  report "$1" app "$(distro debian 13.7)"
  report "$1" caddy "$(distro alpine 3.23.7)"
  report "$1" dashboard "$(distro alpine 3.24.3)"
  report "$1" netns "$(distro alpine 3.24)"
  report "$1" prometheus "$(distro busybox 1.38.0)"
  report "$1" alertmanager "$(distro busybox 1.38.0)"
}
# expect <PASS|FAIL> <case> <dir>; output in $work/out, cycles in $work/cycles
expect() {
  local want="$1" name="$2" got=PASS
  rm -f "$work/cycles"
  "$here/imagecheck-os.sh" "$work/cycles" "$work/$3"/*.json >"$work/out" 2>&1 || got=FAIL
  [[ "$got" == "$want" ]] || fail "$name: got $got, want $want: $(cat "$work/out")"
}

# 1. Today's stack: every OS mapped or listed, cycles written for the lookup.
stack 1
expect PASS "stack" 1
want='alpine-linux 3.23 caddy image (alpine 3.23.7)
alpine-linux 3.24 dashboard image (alpine 3.24.3)
alpine-linux 3.24 netns image (alpine 3.24)
debian 13 app image (debian 13.7)'
[[ "$(sort "$work/cycles")" == "$want" ]] || fail "stack: cycles wrong: $(cat "$work/cycles")"
grep -q "^  caddy.alpine 3.23.7.-> alpine-linux 3.23$" "$work/out" || fail "stack: caddy row wrong: $(cat "$work/out")"
grep -q "^  prometheus.busybox 1.38.0.no published lifecycle (OSLESS)$" "$work/out" || fail "stack: prometheus row wrong: $(cat "$work/out")"
grep -qx "imagecheck: operating systems mapped" "$work/out" || fail "stack: verdict wrong: $(cat "$work/out")"

# 2. Debian with only a major version.
stack 2; report 2 app "$(distro debian 13)"
expect PASS "debian major only" 2
grep -qx "debian 13 app image (debian 13)" "$work/cycles" || fail "debian major: cycle wrong: $(cat "$work/cycles")"

# 3. No distribution found (OS-less image, or no os-release): fails, named.
for d in null '{}' '{"name":"","version":""}'; do
  stack 3; report 3 netns "$d"
  expect FAIL "no distro $d" 3
  grep -q "^  netns.(no distribution found).FAIL: no end-of-life mapping$" "$work/out" || fail "no distro $d: row wrong: $(cat "$work/out")"
  grep -q "operating system unknown or not mapped .*: netns$" "$work/out" || fail "no distro $d: image not named: $(cat "$work/out")"
done
stack 3b; printf '{"descriptor":{"name":"grype"},"matches":[]}' >"$work/3b/netns.json"
expect FAIL "distro field missing" 3b

# 4. A distribution without a mapping, or an unexpected version, fails.
for d in "ubuntu 24.04" "wolfi 20230201" "alpine edge" "alpine 3.24_alpha20260101" "alpine 3" "debian trixie" "debian 12.15.1" "Alpine 3.24.3"; do
  stack 4; report 4 dashboard "$(distro ${d% *} ${d#* })"
  expect FAIL "unmapped $d" 4
  grep -q "operating system unknown or not mapped .*: dashboard$" "$work/out" || fail "unmapped $d: not named: $(cat "$work/out")"
  grep -q "dashboard" "$work/cycles" && fail "unmapped $d: cycle written: $(cat "$work/cycles")"
done

# 5. OSLESS lists an image with one distribution only: the same distribution
# in an unlisted image fails, and so does the listed image with another one.
stack 5; report 5 caddy "$(distro busybox 1.38.0)"
expect FAIL "busybox in an unlisted image" 5
grep -q "not mapped .*: caddy$" "$work/out" || fail "busybox in caddy: not named: $(cat "$work/out")"
stack 5b; report 5b prometheus "$(distro wolfi 20230201)"
expect FAIL "listed image, other distro" 5b
grep -q "not mapped .*: prometheus$" "$work/out" || fail "other distro: not named: $(cat "$work/out")"

# 6. An OSLESS entry that matches no scanned image is stale: the image got
# a mapped OS, or is no longer scanned.
stack 6; report 6 alertmanager "$(distro alpine 3.24.3)"
expect FAIL "stale: image now mapped" 6
grep -q "stale OSLESS entries .*: alertmanager busybox$" "$work/out" || fail "stale mapped: not named: $(cat "$work/out")"
grep -qx "alpine-linux 3.24 alertmanager image (alpine 3.24.3)" "$work/cycles" || fail "stale mapped: cycle not written: $(cat "$work/cycles")"
stack 6b; rm "$work/6b/prometheus.json"
expect FAIL "stale: image gone" 6b
grep -q "stale OSLESS entries .*: prometheus busybox$" "$work/out" || fail "stale gone: not named: $(cat "$work/out")"

# 7. Untrusted text from the image never reaches the output as control
# characters (a newline could start a CI workflow command).
stack 7; report 7 netns '{"name":"alp\nine","version":"3.24\n::error::x"}'
expect FAIL "control characters" 7
grep -q '^::' "$work/out" && fail "control characters: workflow command line in output: $(cat "$work/out")"

# 8. Not JSON: fails, never passes.
stack 8; echo '{' >"$work/8/netns.json"
expect FAIL "invalid json" 8

if [[ $failures -gt 0 ]]; then echo "imagecheck-os_test: $failures failure(s)" >&2; exit 1; fi
echo "imagecheck-os_test: ok"
