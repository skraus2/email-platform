#!/usr/bin/env bash
# End-of-life check of the runtimes and base systems the project pins:
# reads each release cycle from the files that pin it, looks it up on
# endoflife.date and lets scripts/eolcheck-eval.sh decide (fail at end of
# life, warn EOL_WARN_DAYS days before). A cycle that cannot be read from
# its file fails the check instead of being skipped. Needs network.
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
add go go.mod "$(sed -n 's/^toolchain go\([0-9]*\.[0-9]*\).*/\1/p' go.mod)"
add nodejs .nvmrc "$(sed -n 's/^v\{0,1\}\([0-9][0-9]*\).*/\1/p' .nvmrc)"
dockerfiles="Dockerfile apps/dashboard/Dockerfile deploy/caddy/Dockerfile deploy/netns/Dockerfile"
# shellcheck disable=SC2086 # word splitting of the file list is intended
add alpine-linux "Dockerfiles" "$(grep -ho '^FROM [^ ]*alpine:\{0,1\}[0-9][0-9]*\.[0-9][0-9]*' $dockerfiles | sed 's/.*alpine:\{0,1\}//' | sort -u)"
# shellcheck disable=SC2086
add debian "Dockerfile (distroless)" "$(grep -ho '^FROM [^ ]*-debian[0-9][0-9]*' $dockerfiles | sed 's/.*-debian//' | sort -u)"
add postgresql compose.yaml "$(sed -n 's/^ *image: *postgres:\([0-9][0-9]*\).*/\1/p' compose.yaml)"
add prometheus deploy/compose.yaml "$(sed -n 's|^ *image: *prom/prometheus:v\([0-9]*\.[0-9]*\).*|\1|p' deploy/compose.yaml)"

for entry in "${cycles[@]}"; do
  read -r product cycle source <<<"$entry"
  file="$work/$product-$cycle.json"
  code=$(curl -sS --max-time 15 -o "$file" -w '%{http_code}' "https://endoflife.date/api/v1/products/$product/releases/$cycle")
  case "$code" in
    200) jq -e '.result.name' "$file" >/dev/null || { echo "eolcheck: unexpected response for $product $cycle" >&2; exit 1; } ;;
    404) echo 'null' >"$file" ;;
    *) echo "eolcheck: endoflife.date answered $code for $product $cycle" >&2; exit 1 ;;
  esac
  jq -n --arg p "$product" --arg c "$cycle" --arg s "$source" --slurpfile r "$file" \
    '{product: $p, cycle: $c, source: $s, release: ($r[0] | if . == null then null else .result end)}' >"$file.entry"
done

jq -s . "$work"/*.entry >"$work/cycles.json"
./scripts/eolcheck-eval.sh "$work/cycles.json" "$(date -u +%Y-%m-%d)" "$WARN_DAYS"
