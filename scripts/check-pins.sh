#!/usr/bin/env bash
# Versions pinned in more than one file must agree. Reads files only (no
# network, no Docker) and reports every mismatch, naming both files and
# both values:
#   1. Go: the golang build images in Dockerfile and deploy/caddy/Dockerfile
#      carry the go.mod toolchain version and are the identical ref (tag and
#      digest); deploy/caddy/go.mod and every tools/**/go.mod pin the same
#      version;
#   2. Node: both stages of apps/dashboard/Dockerfile, devEngines.runtime in
#      package.json and node@runtime in pnpm-lock.yaml name one X.Y.Z, and
#      .nvmrc names its major (or the full version);
#   3. Prometheus and Alertmanager: PROMETHEUS_IMAGE / ALERTMANAGER_IMAGE in
#      the Makefile are byte-identical to the image of the service of the
#      same name in deploy/compose.yaml;
#   4. golangci-lint: the version CI's golangci-lint action installs
#      (.github/workflows/ci.yml) is the one tools/go.mod pins for
#      `make lint`.
# Usage: check-pins.sh [repository root]
set -euo pipefail
root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$root"

errors=0
err() {
	echo "check-pins: $*" >&2
	errors=$((errors + 1))
}
# need <file> <what> <value>: a value that could not be read is an error.
need() {
	[ -n "$3" ] && return 0
	err "$1: no $2 found"
	return 1
}

## Go

go_version=$(sed -n 's/^toolchain go\([0-9][0-9.]*\)$/\1/p' go.mod)
need go.mod "'toolchain goX.Y.Z' line" "$go_version" || go_version=""

golang_ref_file=""
golang_ref=""
for f in Dockerfile deploy/caddy/Dockerfile; do
	refs=$(awk 'toupper($1) == "FROM" && $2 ~ /^golang:/ { print $2 }' "$f")
	need "$f" "FROM golang: build image" "$refs" || continue
	for ref in $refs; do
		case "$ref" in
		golang:*@sha256:*) ;;
		*) err "$f: $ref is not pinned by digest (golang:X.Y.Z-...@sha256:...)" ;;
		esac
		tag_version=${ref#golang:}
		tag_version=${tag_version%%[-@]*}
		if [ -n "$go_version" ] && [ "$tag_version" != "$go_version" ]; then
			err "$f builds with $ref (Go $tag_version) but go.mod pins toolchain go$go_version"
		fi
		if [ -z "$golang_ref" ]; then
			golang_ref_file=$f
			golang_ref=$ref
		elif [ "$ref" != "$golang_ref" ]; then
			err "$golang_ref_file builds with $golang_ref but $f builds with $ref; use the identical image (tag and digest)"
		fi
	done
done

caddy_go=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' deploy/caddy/go.mod)
if need deploy/caddy/go.mod "'go X.Y.Z' line" "$caddy_go" && [ -n "$go_version" ] &&
	[ "$caddy_go" != "$go_version" ]; then
	err "deploy/caddy/go.mod pins go $caddy_go but go.mod pins toolchain go$go_version"
fi

for f in $(find tools -name go.mod | sort); do
	tool_go=$(sed -n 's/^toolchain go\([0-9][0-9.]*\)$/\1/p' "$f")
	if need "$f" "'toolchain goX.Y.Z' line" "$tool_go" && [ -n "$go_version" ] &&
		[ "$tool_go" != "$go_version" ]; then
		err "$f pins toolchain go$tool_go but go.mod pins toolchain go$go_version"
	fi
done

## Node

# devEngines.runtime.version: the first "version" key inside "runtime"
# inside "devEngines"; a caret range is compared by its base version.
engine=$(awk '
	/"devEngines"[[:space:]]*:/ { dev = 1 }
	dev && /"runtime"[[:space:]]*:/ { rt = 1 }
	dev && rt && /"version"[[:space:]]*:/ {
		sub(/.*"version"[[:space:]]*:[[:space:]]*"/, ""); sub(/".*/, ""); print; exit
	}' package.json)
node_version=""
if need package.json "devEngines.runtime.version" "$engine"; then
	node_version=${engine#^}
	case "$node_version" in
	[0-9]*.[0-9]*.[0-9]*) ;;
	*)
		err "package.json: devEngines.runtime.version $engine is not ^X.Y.Z or X.Y.Z"
		node_version=""
		;;
	esac
fi

f=apps/dashboard/Dockerfile
refs=$(awk 'toupper($1) == "FROM" && $2 ~ /^node:/ { print $2 }' "$f")
if need "$f" "FROM node: image" "$refs"; then
	for ref in $refs; do
		tag_version=${ref#node:}
		tag_version=${tag_version%%[-@]*}
		if [ -n "$node_version" ] && [ "$tag_version" != "$node_version" ]; then
			err "$f uses $ref (Node $tag_version) but package.json devEngines.runtime.version is $engine"
		fi
	done
fi

locked=$(sed -n 's/^  node@runtime:\([0-9][0-9.]*\):.*$/\1/p' pnpm-lock.yaml | sort -u)
if need pnpm-lock.yaml "node@runtime:X.Y.Z entry" "$locked" && [ -n "$node_version" ]; then
	for v in $locked; do
		[ "$v" = "$node_version" ] ||
			err "pnpm-lock.yaml resolves node@runtime:$v but package.json devEngines.runtime.version is $engine"
	done
fi

nvmrc=$(tr -d '[:space:]' <.nvmrc)
nvmrc=${nvmrc#v}
if need .nvmrc "Node version" "$nvmrc" && [ -n "$node_version" ]; then
	case "$nvmrc" in
	*.*.*) want=$node_version ;;
	*) want=${node_version%%.*} ;;
	esac
	[ "$nvmrc" = "$want" ] ||
		err ".nvmrc names Node $nvmrc but package.json devEngines.runtime.version is $engine (expected $want)"
fi

## Prometheus and Alertmanager

for pair in PROMETHEUS_IMAGE:prometheus ALERTMANAGER_IMAGE:alertmanager; do
	var=${pair%%:*}
	svc=${pair#*:}
	make_ref=$(sed -n "s/^$var[[:space:]]*:=[[:space:]]*//p" Makefile)
	compose_ref=$(awk -v svc="$svc" '
		/^[^[:space:]#]/ { top = $1; cur = "" }
		top == "services:" && /^  [^[:space:]#][^:]*:/ { cur = $1; sub(/:$/, "", cur) }
		cur == svc && /^    image:/ { sub(/^    image:[[:space:]]*/, ""); print; exit }' deploy/compose.yaml)
	need Makefile "$var := ... assignment" "$make_ref" || continue
	need deploy/compose.yaml "image for service $svc" "$compose_ref" || continue
	[ "$make_ref" = "$compose_ref" ] ||
		err "Makefile $var is $make_ref but deploy/compose.yaml service $svc uses $compose_ref"
done

## golangci-lint

lint_mod=$(sed -n 's|^[[:space:]]*github.com/golangci/golangci-lint/v2 \(v[0-9][0-9.]*\).*|\1|p' tools/go.mod)
lint_ci=$(awk '
	/uses:[[:space:]]*golangci\/golangci-lint-action@/ { in_action = 1; next }
	in_action && /^[[:space:]]*-[[:space:]]/ { exit }
	in_action && /^[[:space:]]*version:/ { sub(/^[[:space:]]*version:[[:space:]]*/, ""); sub(/[[:space:]]*#.*/, ""); print; exit }' .github/workflows/ci.yml)
if need tools/go.mod "github.com/golangci/golangci-lint/v2 requirement" "$lint_mod" &&
	need .github/workflows/ci.yml "golangci-lint-action version" "$lint_ci"; then
	[ "$lint_mod" = "$lint_ci" ] ||
		err ".github/workflows/ci.yml installs golangci-lint $lint_ci but tools/go.mod pins $lint_mod"
fi

if [ "$errors" -gt 0 ]; then
	echo "check-pins: $errors mismatch(es)" >&2
	exit 1
fi
echo "check-pins: Go $go_version (golang image, caddy, tools), Node $node_version (images, devEngines, lockfile, .nvmrc), prometheus and alertmanager images, golangci-lint $lint_mod (tools, CI) consistent"
