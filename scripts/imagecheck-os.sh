#!/usr/bin/env bash
# Maps the operating system grype found in each image (the report's
# .distro, read from the image's own os-release) to its endoflife.date
# product and release cycle:
#   alpine X.Y[.Z]  ->  alpine-linux X.Y
#   debian N[.M]    ->  debian N
# and writes "<product> <cycle> <source>" lines for
# `scripts/eolcheck.sh --cycles`. An image whose operating system grype
# cannot name, or names without a mapping here, fails, unless OSLESS lists
# the image with exactly that distribution name; an OSLESS entry that
# matches no scanned image fails too (stale).
# Usage: imagecheck-os.sh <cycles-out> <report.json>...
set -euo pipefail
[[ $# -gt 1 ]] || { echo "usage: $0 <cycles-out> <report.json>..." >&2; exit 64; }
cycles_out="$1"
shift

# Images whose operating system has no published support lifecycle, as
# "<image> <distribution name as grype reports it>".
OSLESS=(
  # prom/prometheus and prom/alertmanager are built on the Prometheus
  # project's busybox base image: BusyBox binaries and the shipped program,
  # no package manager. BusyBox publishes no support lifecycle and is not
  # on endoflife.date; its binary is still scanned for known
  # vulnerabilities like everything else in the image.
  "prometheus busybox"
  "alertmanager busybox"
)

osless=$(printf '%s\n' ${OSLESS[@]+"${OSLESS[@]}"} | jq -R 'select(. != "")' | jq -cs .)
out=$(jq -nr --argjson osless "$osless" '
  def clean: tostring | gsub("[[:cntrl:]]"; "");
  def cycle:
    if .name == "alpine" and (.version | test("^[0-9]+\\.[0-9]+(\\.[0-9]+)?$")) then
      {product: "alpine-linux", cycle: (.version | capture("^(?<c>[0-9]+\\.[0-9]+)").c)}
    elif .name == "debian" and (.version | test("^[0-9]+(\\.[0-9]+)?$")) then
      {product: "debian", cycle: (.version | capture("^(?<c>[0-9]+)").c)}
    else null end;
  [inputs | {image: (input_filename | split("/") | last | rtrimstr(".json")),
               name: (if type == "object" then .distro.name // "" else "" end | clean),
               version: (if type == "object" then .distro.version // "" else "" end | clean)}
     | . + {to: cycle, listed: ("\(.image) \(.name)" as $k | $osless | index([$k]) != null)}] as $all
  | [$all[] | select(.to == null and .listed) | "\(.image) \(.name)"] as $used
  | [$osless[] | select(. as $e | $used | index([$e]) | not)] as $stale
  | [$all[] | select(.to == null and (.listed | not))] as $unknown
  | "imagecheck: operating systems found by the scan:",
    ($all[] | "  \(.image)\t\(if .name == "" then "(no distribution found)" else "\(.name) \(.version)" end)\t"
      + (if .to != null then "-> \(.to.product) \(.to.cycle)"
         elif .listed then "no published lifecycle (OSLESS)"
         else "FAIL: no end-of-life mapping" end)),
    ($all[] | select(.to != null) | "cycle \(.to.product) \(.to.cycle) \(.image) image (\(.name) \(.version))"),
    (if ($unknown | length) > 0 then "FAIL operating system unknown or not mapped (add a mapping, or list the image in OSLESS with a reason): \($unknown | map(.image) | join(", "))" else empty end),
    (if ($stale | length) > 0 then "FAIL stale OSLESS entries (match no scanned image, remove them): \($stale | join(", "))" else empty end),
    (if ($unknown + $stale | length) > 0 then "imagecheck: operating systems FAIL" else "imagecheck: operating systems mapped" end)
' "$@")
grep -v '^cycle ' <<<"$out"
sed -n 's/^cycle //p' <<<"$out" >"$cycles_out"
[[ "${out##*$'\n'}" == "imagecheck: operating systems mapped" ]]
