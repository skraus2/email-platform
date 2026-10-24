#!/usr/bin/env bash
# Decides the end-of-life check from a JSON array of
#   {product, cycle, source, release}
# where release is the endoflife.date v1 release object (null when the
# cycle is not listed):
#   - FAIL: the cycle is end of life on <today> or is not listed,
#   - WARN: it reaches end of life within <warn-days> days (exit 0),
#   - OK otherwise, also when no end-of-life date is announced yet; a cycle
#     past its active support (security fixes only) says so.
# Usage: eolcheck-eval.sh <cycles.json> <today YYYY-MM-DD> <warn-days>
set -euo pipefail
[[ $# -eq 3 ]] || { echo "usage: $0 <cycles.json> <today> <warn-days>" >&2; exit 64; }

out=$(jq -r --arg today "$2" --argjson warn "$3" '
  # API text is untrusted: dates must be plain dates, other text loses
  # control characters (a newline could start a CI workflow command).
  def days: if type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$") then strptime("%Y-%m-%d") | mktime / 86400 | floor
            else error("unexpected date \(tojson)") end;
  def clean: tostring | gsub("[[:cntrl:]]"; "");
  ($today | days) as $t
  | if type != "array" or length == 0 then error("no cycles to check") else . end
  | map(. + (
      if .release == null then {state: "FAIL", note: "cycle not listed on endoflife.date"}
      elif .release.isEol == true then {state: "FAIL", note: "end of life since \(.release.eolFrom // "?" | clean)"}
      elif .release.eolFrom == null then {state: "OK", note: "no end-of-life date announced"}
      elif (.release.eolFrom | days) <= $t then {state: "FAIL", note: "end of life since \(.release.eolFrom)"}
      elif (.release.eolFrom | days) - $t <= $warn then {state: "WARN", note: "end of life on \(.release.eolFrom) (in \((.release.eolFrom | days) - $t) days)"}
      else {state: "OK", note: "end of life on \(.release.eolFrom)"} end)
    | if .release.isEoas == true then .note += "; active support ended \(.release.eoasFrom | clean), security fixes only" else . end)
  | (.[] | "\(.state)\t\(.product | clean) \(.cycle | clean)\t\(.note)\t\(.source | clean)"),
    (if any(.state == "FAIL") then "eolcheck: FAIL"
     elif any(.state == "WARN") then "eolcheck: PASS with warnings"
     else "eolcheck: PASS" end)
' "$1")
echo "$out"
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
  grep -E '^(WARN|FAIL)' <<<"$out" | while IFS=$'\t' read -r state what note _; do
    note="${note//%/%25}"
    echo "::warning title=End of life ($state): $what::$note"
  done || true
fi
[[ "${out##*$'\n'}" != "eolcheck: FAIL" ]]
