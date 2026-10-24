#!/usr/bin/env bash
# Known vulnerabilities in what only runs on developer machines and in CI:
# the Go tools pinned in tools/ (scanned as built binaries) and the
# dashboard's development npm packages. Report only: findings never fail
# this script, a scanner that cannot run does. In GitHub Actions each
# finding group also becomes a warning annotation on the run.
set -euo pipefail
cd "$(dirname "$0")/.."

PNPM="${PNPM:-pnpm}"
warn() { # title, message
  echo "vulncheck-dev: $1: $2"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then echo "::warning title=$1::$2"; fi
}

vulncheck="$(go tool -modfile=tools/govulncheck/go.mod -n govulncheck)"
found=0
for modfile in tools/go.mod tools/*/go.mod; do
  tools="$(sed -n 's|^tool .*/||p' "$modfile")"
  [[ -n "$tools" ]] || { echo "vulncheck-dev: no single-line tool directive in $modfile" >&2; exit 1; }
  for tool in $tools; do
    bin="$(go tool -modfile="$modfile" -n "$tool")"
    echo "vulncheck-dev: $tool ($modfile)"
    rc=0
    "$vulncheck" -mode=binary "$bin" || rc=$?
    case $rc in
      0) ;;
      3) found=1; warn "Go tool $tool" "known vulnerabilities in $modfile (see the log)" ;;
      *) echo "vulncheck-dev: govulncheck failed on $tool (exit $rc)" >&2; exit 1 ;;
    esac
  done
done

# Production dependencies are gated by `make vulncheck`; this lists the rest.
echo "vulncheck-dev: dashboard development dependencies"
# The exit code mixes findings and failures; the JSON report tells them apart.
audit="$("$PNPM" audit --dev --json || true)"
jq -e '.metadata.vulnerabilities | [.critical, .high, .moderate, .low] | all(type == "number")' <<<"$audit" >/dev/null ||
  { echo "vulncheck-dev: pnpm audit returned no report" >&2; echo "$audit" | head -20 >&2; exit 1; }
counts="$(jq -r '.metadata.vulnerabilities | "\(.critical) critical, \(.high) high, \(.moderate) moderate, \(.low) low"' <<<"$audit")"
total="$(jq '.metadata.vulnerabilities | .critical + .high + .moderate + .low' <<<"$audit")"
echo "$counts"
# Advisory text is untrusted: control characters could start a CI command.
jq -r '.advisories[]? | "  \(.severity)\t\(.module_name)\t\(.github_advisory_id // .id)\t\(.title)" | gsub("[[:cntrl:]]"; " ")' <<<"$audit"
if [[ "$total" != 0 ]]; then
  found=1
  warn "npm development dependencies" "$counts"
fi

if [[ $found -eq 0 ]]; then echo "vulncheck-dev: no known vulnerabilities"; else echo "vulncheck-dev: findings reported (not failing)"; fi
