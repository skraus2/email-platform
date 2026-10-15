#!/usr/bin/env bash
# The live AWS tests (build tag awslive) send real email and cost money.
# This check keeps them out of every regular gate:
#   1. every Go file mentioning the tag or SES_LIVE_TEST carries the
#      constraint as its first line, so no other build can include it;
#   2. no build, test, lint or CI invocation enables the tag, and GOFLAGS
#      does not either;
#   3. with the tag sets the gates use, no live test file belongs to any
#      package.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() {
	echo "check-awslive: $*" >&2
	exit 1
}

live_files=()
while IFS= read -r f; do
	[ "$(head -1 "$f")" = "//go:build awslive" ] ||
		fail "$f mentions awslive or SES_LIVE_TEST without '//go:build awslive' as its first line"
	live_files+=("$f")
done < <(grep -rlE 'awslive|SES_LIVE_TEST' --include='*.go' cmd internal db || true)
[ "${#live_files[@]}" -gt 0 ] || fail "no live test files found (expected internal/awslive)"

# Nothing may enable the tag (a -tags/build-tags value or GOFLAGS) except the
# manual type-check target, which never runs tests.
if grep -nE -- "tags[= ]+[\"']?[^ ]*awslive|^[[:space:]]*-[[:space:]]*awslive([[:space:]]|$)|GOFLAGS.*awslive" \
	Makefile .golangci.yml .github/workflows/*.yml scripts/*.sh |
	grep -vE '^scripts/check-awslive\.sh:' |
	grep -vxE 'Makefile:[0-9]+:	go vet -tags=awslive \./internal/awslive/'; then
	fail "a gate or script enables the awslive tag (lines above)"
fi
case "$(go env GOFLAGS)" in
*awslive*) fail "GOFLAGS enables the awslive tag" ;;
esac

for tags in "" "integration,load"; do
	listed=$(go list -e -tags="$tags" \
		-f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' ./...)
	for f in "${live_files[@]}"; do
		if grep -qxF "$PWD/$f" <<<"$listed"; then
			fail "$f is built with tags '$tags'"
		fi
	done
done
echo "check-awslive: ${#live_files[@]} live test file(s) excluded from every gate"
