#!/usr/bin/env bash
# End-of-life check of the runtimes the project pins: reads each release
# cycle from the files that pin it, looks it up on endoflife.date and lets
# scripts/eolcheck-eval.sh decide (fail at end of life, warn EOL_WARN_DAYS
# days before). A cycle that cannot be read from its file fails the check
# instead of being skipped. The operating systems of the container images
# are checked by scripts/imagecheck.sh from what the image scan finds.
# Needs network, except with --list, which only prints the cycles read
# from the files.
# Usage: eolcheck.sh [--list | --cycles <file>]
#   --cycles checks the "<product> <cycle> <source>" lines of <file>
#   instead of the pinned runtimes.
set -euo pipefail
cd "$(dirname "$0")/.."

WARN_DAYS="${EOL_WARN_DAYS:-90}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cycles=()

# product, source file(s), cycle(s) found there (one per line)
add() {
  local product="$1" source="$2" found="$3"
  [[ -n "$found" ]] || { echo "eolcheck: no $product version found in $source" >&2; exit 1; }
  while read -r cycle; do cycles+=("$product $cycle $source"); done <<<"$found"
}
if [[ "${1:-}" == --cycles ]]; then
  [[ $# -eq 2 && -s "$2" ]] || { echo "usage: $0 --cycles <non-empty file>" >&2; exit 64; }
  while IFS= read -r line; do cycles+=("$line"); done <"$2"
else
  add go go.mod "$(sed -n 's/^toolchain go\([0-9]*\.[0-9]*\).*/\1/p' go.mod)"
  add nodejs .nvmrc "$(sed -n 's/^v\{0,1\}\([0-9][0-9]*\).*/\1/p' .nvmrc)"
  add postgresql compose.yaml "$(sed -n 's/^ *image: *postgres:\([0-9][0-9]*\).*/\1/p' compose.yaml)"
  add prometheus deploy/compose.yaml "$(sed -n 's|^ *image: *prom/prometheus:v\([0-9]*\.[0-9]*\).*|\1|p' deploy/compose.yaml)"
fi
# Product and cycle become part of the lookup URL.
for entry in "${cycles[@]}"; do
  [[ "$entry" =~ ^[a-z][a-z0-9-]*\ [0-9]+(\.[0-9]+)?\ [^[:cntrl:]]+$ ]] || { echo "eolcheck: not \"<product> <cycle> <source>\": ${entry//[[:cntrl:]]/?}" >&2; exit 1; }
done

if [[ "${1:-}" == --list ]]; then printf '%s\n' "${cycles[@]}"; exit 0; fi

for entry in "${cycles[@]}"; do
  read -r product cycle source <<<"$entry"
  file="$work/$product-$cycle-${source//[^A-Za-z0-9.-]/_}.json"
  code=$(curl -sS --max-time 15 -o "$file" -w '%{http_code}' "https://endoflife.date/api/v1/products/$product/releases/$cycle")
  case "$code" in
    200) jq -e '.result | (.isEol | type) == "boolean" and has("eolFrom")' "$file" >/dev/null ||
           { echo "eolcheck: unexpected response for $product $cycle" >&2; exit 1; } ;;
    404) echo 'null' >"$file" ;;
    *) echo "eolcheck: endoflife.date answered $code for $product $cycle" >&2; exit 1 ;;
  esac
  jq -n --arg p "$product" --arg c "$cycle" --arg s "$source" --slurpfile r "$file" \
    '{product: $p, cycle: $c, source: $s, release: ($r[0] | if . == null then null else .result end)}' >"$file.entry"
done

jq -s . "$work"/*.entry >"$work/cycles.json"
./scripts/eolcheck-eval.sh "$work/cycles.json" "$(date -u +%Y-%m-%d)" "$WARN_DAYS"
