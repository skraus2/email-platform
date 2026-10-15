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

# git grep (tracked and untracked, not ignored files), failing on any error
# rather than reading it as "no match".
search() {
	local out rc=0
	out=$(git grep --untracked "$@") || rc=$?
	[ "$rc" -le 1 ] || fail "git grep failed (exit $rc)"
	[ -z "$out" ] || printf '%s\n' "$out"
}

live_files=()
while IFS= read -r f; do
	[ "$(head -1 "$f")" = "//go:build awslive" ] ||
		fail "$f mentions awslive or SES_LIVE_TEST without '//go:build awslive' as its first line"
	live_files+=("$f")
done < <(search -l -E 'awslive|SES_LIVE_TEST' -- '*.go')
[ "${#live_files[@]}" -gt 0 ] || fail "no live test files found (expected internal/awslive)"

# Every literal mention of the tag in build, lint, CI and container files
# (at any depth) must be one of these known lines; anything else (a tag
# list, a variable, GOFLAGS) fails. A tag hidden behind an indirection that
# never spells "awslive" (a variable set outside the repo) is not detected;
# SES_LIVE_TEST=1 and the complete SES configuration still guard a run.
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
mentions=$(search -e awslive -- '*Makefile' '*.mk' '*.golangci*' '.github/' 'scripts/' '*Dockerfile*' \
	'*compose*.y*ml' 'tools/' '*.env*' ':!scripts/check-awslive.sh')
unexpected=$(grep -vxF -f <(printf '%s\n' "${allowed[@]}") <<<"$mentions" || true)
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
