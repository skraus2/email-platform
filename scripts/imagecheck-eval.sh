#!/usr/bin/env bash
# Evaluates grype JSON reports (one file per image, named <image>.json) and
# decides the image scan result:
#   - FAIL: a High or Critical vulnerability with a fixed version available
#     (the image can be updated), unless an ignore rule suppressed it,
#   - FAIL: an ignore rule without a vulnerability ID or without a reason,
#   - FAIL: an ignore rule that suppressed nothing in any image (stale),
#   - FAIL: a file that is not a complete grype report, two reports for one
#     image, or a scan configuration that drops findings outside the ignore
#     rules (exclusions other than the app's own binaries, only-fixed,
#     only-notfixed, ignore-wontfix, VEX documents, CPE matching off for
#     binaries, a non-squashed search scope),
#   - reported only: High/Critical without a fix, and fixable findings below
#     High (Go advisories often carry no severity score).
# Grype's four built-in ignore rules (kernel header packages, matched
# indirectly) are not ours; they are recognised exactly and skipped in the
# rule checks.
# Usage: imagecheck-eval.sh <report.json>...
set -euo pipefail
[[ $# -gt 0 ]] || { echo "usage: $0 <report.json>..." >&2; exit 64; }

out=$(jq -nr --argjson files $# '
  def high: .vulnerability.severity == "High" or .vulnerability.severity == "Critical";
  def fixed: .vulnerability.fix.state == "fixed";
  def row: [.vulnerability.id, .vulnerability.severity, .artifact.name, .artifact.version,
            (if fixed then "fix " + (.vulnerability.fix.versions | join(",")) else "no fix" end)];
  # The config echoes every rule field, applied rules only the non-empty
  # ones: compare both without empty values.
  def key: walk(if type == "object" then with_entries(select(.value | . != "" and . != false and . != null and . != {})) else . end);
  def builtin: key as $k | [
      {package: {name: "kernel-headers", type: "rpm", "upstream-name": "kernel"}, "match-type": "exact-indirect-match"},
      {package: {name: "linux(-.*)?-headers-.*", type: "deb", "upstream-name": "linux.*"}, "match-type": "exact-indirect-match"},
      {package: {name: "linux-libc-dev", type: "deb", "upstream-name": "linux"}, "match-type": "exact-indirect-match"},
      {package: {name: "linux-kbuild-.*", type: "deb", "upstream-name": "linux.*"}, "match-type": "exact-indirect-match"}
    ] | any(. == $k);
  def complete: type == "object" and .descriptor.name? == "grype" and (.matches | type) == "array"
    and (.descriptor.db.status.built // "") != "" and (.descriptor.configuration | type) == "object";
  # Settings that would drop findings without an ignore rule.
  def drift: .image as $i | .descriptor.configuration | [
      (if .exclude != (if $i == "app" then ["/app/*"] else [] end) then "exclude \(.exclude | tojson)" else empty end),
      (if .["only-fixed"] != false then "only-fixed" else empty end),
      (if .["only-notfixed"] != false then "only-notfixed" else empty end),
      (if (.["ignore-wontfix"] // "") != "" then "ignore-wontfix" else empty end),
      (if (.["vex-documents"] // []) != [] then "vex-documents" else empty end),
      (if .search.scope != "squashed" then "search.scope \(.search.scope)" else empty end),
      (if .match.stock["using-cpes"] != true then "match.stock.using-cpes" else empty end)
    ] | map("\($i): \(.)")[];
  def rulename: "\(.vulnerability)\(if (.package.location // "") != "" then " at " + .package.location else "" end)";

  [inputs | {image: (input_filename | split("/") | last | rtrimstr(".json")), ok: complete} + (if type == "object" then . else {} end)] as $all
  | ([$all[] | select(.ok | not) | .image]
     + (if ($all | length) != $files then ["\($files) files, \($all | length) reports (empty file?)"] else [] end)
     + [$all | group_by(.image)[] | select(length > 1) | "\(.[0].image) twice"]) as $broken
  | [$all[] | select(.ok)] as $reports
  | [$reports[] | drift] as $drift
  | ([$reports[] | .descriptor.configuration.ignore] | unique | length) as $rulesets
  | ($reports | map(.matches | length) | add // 0) as $n
  | [$reports[] | .image as $i | .matches[] | select(high) | {image: $i, fixed: fixed, row: row}] as $high
  | [$reports[] | .image as $i | (.ignoredMatches // [])[] | {image: $i, row: row, rules: .appliedIgnoreRules}] as $ignored
  | ($reports[0].descriptor.configuration.ignore // [] | map(select(builtin | not))) as $rules
  | [$rules[] | select(.vulnerability == "" or (.reason // "") == "")] as $invalid
  | [$ignored[].rules[] | key] as $applied
  | [$rules[] | select(key as $k | $applied | any(. == $k) | not)] as $stale
  | ($high | map(select(.fixed))) as $fail
  | [$reports[] | .image as $i | .matches[] | select((high | not) and fixed) | {image: $i, row: row}] as $lowfix
  | (
      "imagecheck: \($reports | length) images, \($n) findings, \($high | length) High/Critical (\($fail | length) with a fix), \($ignored | length) ignored by rule",
      ($reports[] | "  \(.image): \(.matches | length) findings, DB built \(.descriptor.db.status.built // "?")"),
      (if ($fail | length) > 0 then "FAIL High/Critical with a fix (update the image or its base):", ($fail[] | "  \(.image)\t\(.row | join("\t"))") else empty end),
      (if ($high | map(select(.fixed | not)) | length) > 0 then "reported (no fix available yet):", ($high[] | select(.fixed | not) | "  \(.image)\t\(.row | join("\t"))") else empty end),
      (if ($lowfix | length) > 0 then "reported (fix available, below High):", ($lowfix[] | "  \(.image)\t\(.row | join("\t"))") else empty end),
      (if ($ignored | length) > 0 then "ignored by rule:", ($ignored[] | "  \(.image)\t\(.row | join("\t"))\t\(.rules | map(.reason) | join("; "))") else empty end),
      (if ($invalid | length) > 0 then "FAIL ignore rules need a vulnerability ID and a reason:", ($invalid[] | "  \(key | tojson)") else empty end),
      (if ($stale | length) > 0 then "FAIL stale ignore rules (match nothing any more, remove them):", ($stale[] | "  \(rulename): \(.reason)") else empty end),
      (if ($broken | length) > 0 then "FAIL not a complete grype report:", ($broken[] | "  \(.)") else empty end),
      (if ($drift | length) > 0 then "FAIL scan configuration drops findings outside the ignore rules:", ($drift[] | "  \(.)") else empty end),
      (if $rulesets > 1 then "FAIL reports were scanned with different ignore rules" else empty end),
      (if ($fail + $invalid + $stale + $broken + $drift | length) > 0 or $rulesets > 1 or ($reports | length) == 0 then "imagecheck: FAIL" else "imagecheck: PASS" end)
    )
' "$@")
echo "$out"
[[ "${out##*$'\n'}" == "imagecheck: PASS" ]]
