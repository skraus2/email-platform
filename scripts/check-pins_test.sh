#!/usr/bin/env bash
# Tests scripts/check-pins.sh against copies of the repository's own pin
# files (no network, no Docker): the current tree passes, and every kind of
# drift fails with a message naming both files and both values.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

files="go.mod Dockerfile deploy/caddy/Dockerfile deploy/caddy/go.mod apps/dashboard/Dockerfile
package.json pnpm-lock.yaml .nvmrc Makefile deploy/compose.yaml compose.yaml .github/workflows/ci.yml"
tool_mods=$(cd "$repo" && find tools -name go.mod | sort)
# fresh <case>: a copy of the pin files in $work/<case>
fresh() {
  local d="$work/$1" f
  for f in $files $tool_mods; do
    mkdir -p "$d/$(dirname "$f")"
    cp "$repo/$f" "$d/$f"
  done
}
# edit <case> <file> <sed expression>: the expression must change the file.
edit() {
  local f="$work/$1/$2"
  sed "$3" "$f" >"$f.new"
  if cmp -s "$f" "$f.new"; then fail "$1: '$3' did not change $2"; fi
  mv "$f.new" "$f"
}
# expect <PASS|FAIL> <case>; output in $work/out
expect() {
  local got=PASS
  "$here/check-pins.sh" "$work/$2" >"$work/out" 2>&1 || got=FAIL
  [[ "$got" == "$1" ]] || fail "$2: got $got, want $1: $(cat "$work/out")"
}
# says <case> <fixed string>...: the last output contains every string.
says() {
  local name="$1" s; shift
  for s in "$@"; do
    grep -qF -- "$s" "$work/out" || fail "$name: output lacks '$s': $(cat "$work/out")"
  done
}

go_ver=$(sed -n 's/^toolchain go//p' "$repo/go.mod")
node_ver=$(sed -n 's/.*"version": "^\(.*\)",/\1/p' "$repo/package.json")
golang_ref=$(awk '$1 == "FROM" && $2 ~ /^golang:/ { print $2 }' "$repo/Dockerfile")
prom_ref=$(sed -n 's/^PROMETHEUS_IMAGE *:= *//p' "$repo/Makefile")
am_ref=$(sed -n 's/^ALERTMANAGER_IMAGE *:= *//p' "$repo/Makefile")
lint_ver=$(sed -n 's|^[[:space:]]*github.com/golangci/golangci-lint/v2 \(v[0-9.]*\).*|\1|p' "$repo/tools/go.mod")
pg_ref=$(sed -n 's|^    image: \(postgres:.*\)|\1|p' "$repo/compose.yaml")
mp_ref=$(sed -n 's|^    image: \(axllent/mailpit:.*\)|\1|p' "$repo/compose.yaml")
[[ -n "$go_ver" && -n "$node_ver" && -n "$golang_ref" && -n "$prom_ref" && -n "$am_ref" && -n "$lint_ver" &&
  -n "$pg_ref" && -n "$mp_ref" ]] ||
  fail "could not read the current pins"
other_digest=sha256:0000000000000000000000000000000000000000000000000000000000000000

# 1. The repository itself and an untouched copy pass.
"$here/check-pins.sh" >"$work/out" 2>&1 || fail "repository tree: $(cat "$work/out")"
fresh clean
expect PASS clean
says clean "Go $go_ver" "Node $node_ver" "CI service images (postgres, mailpit) consistent"

# 2. Application build image on another Go version than the toolchain.
fresh go-app
edit go-app Dockerfile "s/^FROM golang:$go_ver-/FROM golang:1.0.1-/"
expect FAIL go-app
says go-app "Dockerfile builds with golang:1.0.1-" "(Go 1.0.1) but go.mod pins toolchain go$go_ver"

# 3. Caddy build image with another digest than the application's.
fresh go-digest
edit go-digest deploy/caddy/Dockerfile "s/@sha256:[0-9a-f]*/@$other_digest/"
expect FAIL go-digest
says go-digest "Dockerfile builds with $golang_ref but deploy/caddy/Dockerfile builds with ${golang_ref%@*}@$other_digest"

# 4. Build image without a digest.
fresh go-nodigest
edit go-nodigest deploy/caddy/Dockerfile "s/@sha256:[0-9a-f]* / /"
expect FAIL go-nodigest
says go-nodigest "deploy/caddy/Dockerfile: ${golang_ref%@*} is not pinned by digest"

# 5. Toolchain bumped in go.mod only: every other Go pin is reported.
fresh go-toolchain
edit go-toolchain go.mod "s/^toolchain go.*/toolchain go9.9.9/"
expect FAIL go-toolchain
says go-toolchain "Dockerfile builds with $golang_ref (Go $go_ver) but go.mod pins toolchain go9.9.9" \
  "deploy/caddy/Dockerfile builds with $golang_ref (Go $go_ver) but go.mod pins toolchain go9.9.9" \
  "deploy/caddy/go.mod pins go $go_ver but go.mod pins toolchain go9.9.9" \
  "tools/go.mod pins toolchain go$go_ver but go.mod pins toolchain go9.9.9"

# 6. Caddy module and a tool module on another Go version.
fresh go-modules
edit go-modules deploy/caddy/go.mod "s/^go .*/go 1.0.1/"
edit go-modules tools/sqlc/go.mod "s/^toolchain go.*/toolchain go1.0.1/"
expect FAIL go-modules
says go-modules "deploy/caddy/go.mod pins go 1.0.1 but go.mod pins toolchain go$go_ver" \
  "tools/sqlc/go.mod pins toolchain go1.0.1 but go.mod pins toolchain go$go_ver" "2 mismatch(es)"

# 7. Dashboard build stage and runtime stage, each on another Node version.
for stage in bookworm alpine; do
  fresh "node-$stage"
  edit "node-$stage" apps/dashboard/Dockerfile "s/^FROM node:$node_ver-$stage/FROM node:1.0.1-$stage/"
  expect FAIL "node-$stage"
  says "node-$stage" "apps/dashboard/Dockerfile uses node:1.0.1-$stage" \
    "(Node 1.0.1) but package.json devEngines.runtime.version is ^$node_ver"
done

# 8. devEngines bumped alone: images, lockfile and .nvmrc major are reported.
fresh node-engines
edit node-engines package.json "s/\"version\": \"^$node_ver\"/\"version\": \"^99.0.0\"/"
expect FAIL node-engines
says node-engines "apps/dashboard/Dockerfile uses node:$node_ver-" \
  "pnpm-lock.yaml resolves node@runtime:$node_ver but package.json devEngines.runtime.version is ^99.0.0" \
  ".nvmrc names Node ${node_ver%%.*} but package.json devEngines.runtime.version is ^99.0.0 (expected 99)"

# 9. Lockfile resolved to another Node version.
fresh node-lock
edit node-lock pnpm-lock.yaml "s/node@runtime:$node_ver/node@runtime:1.0.1/g"
expect FAIL node-lock
says node-lock "pnpm-lock.yaml resolves node@runtime:1.0.1 but package.json devEngines.runtime.version is ^$node_ver"

# 10. .nvmrc: another major fails; a full version must match exactly; v-prefix is accepted.
fresh nvmrc-major
echo 1 >"$work/nvmrc-major/.nvmrc"
expect FAIL nvmrc-major
says nvmrc-major ".nvmrc names Node 1 but package.json devEngines.runtime.version is ^$node_ver"
fresh nvmrc-full
echo "${node_ver%.*}.999" >"$work/nvmrc-full/.nvmrc"
expect FAIL nvmrc-full
fresh nvmrc-ok
echo "v$node_ver" >"$work/nvmrc-ok/.nvmrc"
expect PASS nvmrc-ok

# 11. Prometheus image changed in the Makefile only, Alertmanager in Compose only.
fresh prom
edit prom Makefile "s|^\(PROMETHEUS_IMAGE *:= *\).*|\1${prom_ref%@*}@$other_digest|"
expect FAIL prom
says prom "Makefile PROMETHEUS_IMAGE is ${prom_ref%@*}@$other_digest but deploy/compose.yaml service prometheus uses $prom_ref"
fresh am
edit am deploy/compose.yaml "s|image: $am_ref|image: prom/alertmanager:v0.0.1|"
expect FAIL am
says am "Makefile ALERTMANAGER_IMAGE is $am_ref but deploy/compose.yaml service alertmanager uses prom/alertmanager:v0.0.1"

# 12. A pin that cannot be found is an error, not a pass.
fresh missing
edit missing deploy/compose.yaml "s|image: $prom_ref|image:|"
edit missing go.mod "/^toolchain /d"
expect FAIL missing
says missing "deploy/compose.yaml: no image for service prometheus found" "go.mod: no 'toolchain goX.Y.Z' line found"

# 13. golangci-lint bumped in tools/go.mod only (as a tools update pull
# request does), and the CI version missing.
fresh lint
edit lint tools/go.mod "s|golangci-lint/v2 $lint_ver|golangci-lint/v2 v9.9.9|"
expect FAIL lint
says lint ".github/workflows/ci.yml installs golangci-lint $lint_ver but tools/go.mod pins v9.9.9"
fresh lint-missing
edit lint-missing .github/workflows/ci.yml "s|^\([[:space:]]*\)version: $lint_ver|\1verzion: $lint_ver|"
expect FAIL lint-missing
says lint-missing ".github/workflows/ci.yml: no golangci-lint-action version found"

# 14. CI service image changed in one job only (the go job's mailpit).
fresh ci-svc
edit ci-svc .github/workflows/ci.yml "1,\\|image: $mp_ref|s|image: $mp_ref|image: axllent/mailpit:v0.0.1|"
expect FAIL ci-svc
says ci-svc ".github/workflows/ci.yml job go service mailpit uses axllent/mailpit:v0.0.1 but compose.yaml service mailpit uses $mp_ref" \
  "1 mismatch(es)"

# 15. compose.yaml bumped alone (as an update pull request does): every job
# running that service is reported.
fresh ci-compose
edit ci-compose compose.yaml "s|image: $pg_ref|image: postgres:99.0-alpine|"
expect FAIL ci-compose
says ci-compose ".github/workflows/ci.yml job go service postgres uses $pg_ref but compose.yaml service postgres uses postgres:99.0-alpine" \
  ".github/workflows/ci.yml job e2e service postgres uses $pg_ref but compose.yaml service postgres uses postgres:99.0-alpine" \
  "2 mismatch(es)"

# 16. A service image missing on either side, or no services at all, is an error.
fresh ci-missing
edit ci-missing .github/workflows/ci.yml "/^  e2e:/,\$s|image: $pg_ref|imagex: $pg_ref|"
edit ci-missing compose.yaml "s|image: $mp_ref|image:|"
expect FAIL ci-missing
says ci-missing ".github/workflows/ci.yml: no image for service postgres of job e2e found" \
  "compose.yaml: no image for service mailpit found"
fresh ci-none
edit ci-none .github/workflows/ci.yml "s|^    services:|    servicez:|"
expect FAIL ci-none
says ci-none ".github/workflows/ci.yml: no job services: found"

if [[ $failures -gt 0 ]]; then echo "check-pins_test: $failures failure(s)" >&2; exit 1; fi
echo "check-pins_test: ok"
