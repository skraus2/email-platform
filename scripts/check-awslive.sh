#!/usr/bin/env bash
# The live AWS tests (build tag awslive) send real email and cost money.
# This check keeps them out of every regular gate:
#   1. every Go file mentioning the tag or SES_LIVE_TEST carries the
#      constraint as its first line, so no other build can include it;
#   2. build, lint, CI and container files mention the tag only in a fixed
#      list of known lines (the manual type-check target never runs tests),
#      and GOFLAGS does not enable it;
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
done < <(git ls-files --cached --others --exclude-standard -- '*.go' | xargs grep -lE 'awslive|SES_LIVE_TEST' || true)
[ "${#live_files[@]}" -gt 0 ] || fail "no live test files found (expected internal/awslive)"

# Every mention of the tag in build, lint, CI and container files must be one
# of these known lines; anything else (a tag list, a variable, GOFLAGS) fails.
allowed=(
	'Makefile:lint: lint-go lint-web lint-ci sqlc-check lint-awslive ## Run all linters'
	'Makefile:.PHONY: lint-awslive'
	'Makefile:lint-awslive: ## Prove the live AWS tests are excluded from every gate'
	'Makefile:	./scripts/check-awslive.sh'
	'Makefile:.PHONY: vet-awslive'
	'Makefile:vet-awslive: ## Type-check the live AWS tests (never runs them, no AWS access)'
	'Makefile:	go vet -tags=awslive ./internal/awslive/'
	'.github/workflows/ci.yml:        run: ./scripts/check-awslive.sh'
)
unexpected=$(
	git ls-files --cached --others --exclude-standard -- Makefile '*.mk' '.golangci*' '.github/*' 'scripts/*' \
		Dockerfile 'compose*' 'tools/*' '.env.example' |
		grep -vx 'scripts/check-awslive.sh' | xargs grep -H 'awslive' |
		grep -vxF -f <(printf '%s\n' "${allowed[@]}") || true
)
[ -z "$unexpected" ] || fail "unexpected mention of the awslive tag:
$unexpected"
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
