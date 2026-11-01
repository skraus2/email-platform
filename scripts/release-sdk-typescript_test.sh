#!/usr/bin/env bash
# Tests scripts/release-sdk-typescript.sh without network or registry:
# its checks on hand-built tarballs (expected file set passes; an extra or a
# missing file, a missing entry point, a wrong name or version, an install
# script and wrong exports fail with a message naming them), the tag and
# URL parsers, the integrity string against node:crypto, and the whole run
# against throwaway repositories with a local bare repository as origin and
# a stub pnpm: a pushed annotated or lightweight tag releases; a missing,
# unpushed or moved tag, a tag off origin's main, a version mismatch, a
# build that changes the commit's files, a rejected tarball, the wrong Node
# and a non-GitHub origin fail; every run leaves no worktree or temp dir and
# no tarball unless it passed; a dry run is marked as one.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/release-sdk-typescript.sh"
# Sourced first: it sets its own globals (work among them) and runs nothing.
# shellcheck source=scripts/release-sdk-typescript.sh
source "$script"

work="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
# says <case> <fixed string>: the last output contains the string.
says() {
  grep -qF -- "$2" "$work/out" || fail "$1: output lacks '$2': $(cat "$work/out")"
}
lacks() {
  if grep -qF -- "$2" "$work/out"; then fail "$1: output has '$2': $(cat "$work/out")"; fi
}
node_major="$(node -p 'process.versions.node.split(".")[0]')"
six='export const ApiError = 1, EmailPlatform = 1, TransportError = 1, WebhookVerificationError = 1, paginate = 1, verifyWebhook = 1;'

# --- checks on hand-built tarballs -----------------------------------------

dist_files="dist/index.js dist/index.d.ts dist/browser.js dist/browser.d.ts dist/generated/openapi.d.ts"
# fixture <case> [version] [package.json]: a package dir $work/<case>/pkg with
# a build output, and a stage dir $work/<case>/stage/package with the same
# files plus package.json, README.md and LICENSE.
fixture() {
  local d="$work/$1" version="${2:-0.1.0}" manifest="${3:-}" f
  if [[ -z "$manifest" ]]; then
    manifest="{\"name\":\"@email-platform/sdk\",\"version\":\"$version\",\"type\":\"module\",\"exports\":{\".\":\"./dist/index.js\"}}"
  fi
  mkdir -p "$d/pkg" "$d/stage/package"
  for f in $dist_files; do
    mkdir -p "$d/pkg/$(dirname "$f")" "$d/stage/package/$(dirname "$f")"
    echo "// $f" >"$d/pkg/$f"
    cp "$d/pkg/$f" "$d/stage/package/$f"
  done
  echo "$six" >"$d/stage/package/dist/index.js"
  printf '%s\n' "$manifest" >"$d/stage/package/package.json"
  echo readme >"$d/stage/package/README.md"
  echo license >"$d/stage/package/LICENSE"
}
# pack <case>: $work/<case>/t.tgz from the stage dir, files only, like pnpm
# pack (COPYFILE_DISABLE: no AppleDouble entries from macOS tar).
pack() {
  local d="$work/$1"
  (cd "$d/stage" && find package -type f | LC_ALL=C sort | COPYFILE_DISABLE=1 tar -czf "$d/t.tgz" -T -)
}
# expect <PASS|FAIL> <case> [version]; output in $work/out
expect() {
  local got=PASS
  (check_tarball "$work/$2/t.tgz" "$work/$2/pkg" "${3:-0.1.0}") >"$work/out" 2>&1 || got=FAIL
  [[ "$got" == "$1" ]] || fail "$2: got $got, want $1: $(cat "$work/out")"
}

fixture ok
pack ok
expect PASS ok

fixture extra
echo 'EP_API_KEY=ep_x' >"$work/extra/stage/package/.env"
pack extra
expect FAIL extra
says extra "> package/.env"

fixture no-license
rm "$work/no-license/stage/package/LICENSE"
pack no-license
expect FAIL no-license
says no-license "< package/LICENSE"

fixture stale-dist
echo '// built earlier' >"$work/stale-dist/stage/package/dist/old.js"
pack stale-dist
expect FAIL stale-dist
says stale-dist "> package/dist/old.js"

fixture unpacked
echo '// new' >"$work/unpacked/pkg/dist/extra.js"
pack unpacked
expect FAIL unpacked
says unpacked "< package/dist/extra.js"

fixture no-browser
rm "$work/no-browser/pkg/dist/browser.js" "$work/no-browser/stage/package/dist/browser.js"
pack no-browser
expect FAIL no-browser
says no-browser "did not produce dist/browser.js"

fixture old-version 0.0.0
pack old-version
expect FAIL old-version
says old-version "says '@email-platform/sdk 0.0.0', expected '@email-platform/sdk 0.1.0'"

fixture other-name 0.1.0 '{"name":"@other/sdk","version":"0.1.0"}'
pack other-name
expect FAIL other-name
says other-name "'@other/sdk 0.1.0'"

fixture postinstall 0.1.0 '{"name":"@email-platform/sdk","version":"0.1.0","scripts":{"build":"tsc","postinstall":"x","prepare":"y"}}'
pack postinstall
expect FAIL postinstall
says postinstall "install-time scripts: postinstall prepare"

# check_import on the unpacked tarball
(check_import "$work/ok/t.tgz" "$work/ok" "$(command -v node)") >"$work/out" 2>&1 ||
  fail "check_import ok: $(cat "$work/out")"
fixture seven
echo "$six export const internal = 1;" >"$work/seven/stage/package/dist/index.js"
pack seven
if (check_import "$work/seven/t.tgz" "$work/seven" "$(command -v node)") >"$work/out" 2>&1; then
  fail "check_import seven: passed"
fi
says seven 'the entry exports "ApiError EmailPlatform TransportError WebhookVerificationError internal paginate verifyWebhook"'

# --- parsers and integrity ---------------------------------------------------

t=refs/tags/packages/sdk-typescript/v0.1.0
annotated="$(printf 'aaa\t%s\nccc\t%s^{}\n' "$t" "$t" | tag_commit "$t")"
[[ "$annotated" == ccc ]] || fail "tag_commit annotated: got '$annotated', want ccc"
light="$(printf 'ccc\t%s\n' "$t" | tag_commit "$t")"
[[ "$light" == ccc ]] || fail "tag_commit lightweight: got '$light', want ccc"
other="$(printf 'ddd\t%s1\n' "$t" | tag_commit "$t")"
[[ -z "$other" ]] || fail "tag_commit other tag: got '$other', want empty"
none="$(tag_commit "$t" </dev/null)"
[[ -z "$none" ]] || fail "tag_commit none: got '$none', want empty"

for pair in "https://github.com/o/r.git=o/r" "https://github.com/o/r=o/r" "git@github.com:o/r.git=o/r" \
  "ssh://git@github.com/o/r.git=o/r" "https://evil.example/github.com/o/r=" "https://github.com/o/r/x=" \
  "https://github.com.evil.example/o/r=" "/srv/git/r.git="; do
  got="$(github_repo "${pair%%=*}")"
  [[ "$got" == "${pair#*=}" ]] || fail "github_repo ${pair%%=*}: got '$got', want '${pair#*=}'"
done

printf 'release tarball\n' >"$work/blob"
want="sha512-$(node -e 'process.stdout.write(require("node:crypto").createHash("sha512").update(require("node:fs").readFileSync(process.argv[1])).digest("base64"))' "$work/blob")"
[[ "$(sri "$work/blob")" == "$want" ]] || fail "sri: got $(sri "$work/blob"), want $want"

# bad versions are refused with the usage line, before git is consulted
for v in "" v0.1.0 0.1 0.1.0-rc.1 01.2.3 "0.1.0 x" "0.1.0;id"; do
  got=0
  (cd "$work" && bash "$script" "$v") >"$work/out" 2>&1 || got=$?
  [[ "$got" == 1 ]] || fail "version '$v': exit $got, want 1"
  says "version '$v'" "usage:"
done

# --- whole runs against throwaway repositories --------------------------------

mkdir -p "$work/bin"
cat >"$work/bin/pnpm" <<'STUB'
#!/usr/bin/env bash
# Stands in for pnpm in a release run: no network, a tiny build output.
set -euo pipefail
case "$*" in
  --version) echo 10.0.0-stub ;;
  "install "*) ;;
  "--filter @email-platform/sdk generate:check") ;;
  "--filter @email-platform/sdk test")
    if [[ -n "${STUB_DIRTY:-}" ]]; then echo changed >>packages/sdk-typescript/README.md; fi ;;
  "--filter @email-platform/sdk build")
    d=packages/sdk-typescript/dist
    mkdir -p "$d"
    echo "$STUB_SIX" >"$d/index.js"
    echo 'export {};' >"$d/index.d.ts"
    cp "$d/index.js" "$d/browser.js" ;;
  "--filter @email-platform/sdk exec node --version") echo "${STUB_NODE:-$(node --version)}" ;;
  "--filter @email-platform/sdk exec node "*) shift 4; exec node "$@" ;;
  "pack --pack-destination "*)
    v="$(node -p 'require("./package.json").version')"
    stage="$(mktemp -d "$TMPDIR/stage.XXXXXX")"
    mkdir "$stage/package"
    cp package.json README.md LICENSE "$stage/package/"
    cp -R dist "$stage/package/"
    if [[ -n "${STUB_EXTRA:-}" ]]; then echo x >"$stage/package/.env"; fi
    (cd "$stage" && find package -type f | LC_ALL=C sort | COPYFILE_DISABLE=1 tar -czf "$3/email-platform-sdk-$v.tgz" -T -)
    rm -rf "$stage" ;;
  *) echo "stub pnpm: unexpected arguments: $*" >&2; exit 9 ;;
esac
STUB
chmod +x "$work/bin/pnpm"

# repo_fixture <case> [package.json]: $work/<case>/repo on main with one
# commit, pushed to the bare $work/<case>/origin.git, which origin reaches
# through a GitHub-looking URL.
repo_fixture() {
  local d="$work/$1" r="$work/$1/repo" url="https://github.com/test/$1.git" manifest="${2:-}"
  if [[ -z "$manifest" ]]; then
    manifest="{\"name\":\"@email-platform/sdk\",\"version\":\"0.1.0\",\"type\":\"module\",\"exports\":{\".\":\"./dist/index.js\"},\"engines\":{\"node\":\"^$node_major\"}}"
  fi
  mkdir -p "$d/tmp"
  git init -q --bare "$d/origin.git"
  git init -q -b main "$r"
  git -C "$r" config user.name test
  git -C "$r" config user.email test@example.test
  git -C "$r" config commit.gpgsign false
  git -C "$r" config tag.gpgsign false
  git -C "$r" config core.hooksPath /dev/null
  git -C "$r" remote add origin "$url"
  git -C "$r" config "url.$d/origin.git.insteadOf" "$url"
  mkdir -p "$r/packages/sdk-typescript"
  echo license >"$r/LICENSE"
  printf 'node_modules/\npackages/sdk-typescript/dist/\n' >"$r/.gitignore"
  echo readme >"$r/packages/sdk-typescript/README.md"
  printf '%s\n' "$manifest" >"$r/packages/sdk-typescript/package.json"
  git -C "$r" add -A
  git -C "$r" commit -q -m init
  git -C "$r" push -q origin main
}
# run <case> [args...]: the script in that repo; output in $work/out, exit
# status in $status.
status=0
run() {
  local d="$work/$1"
  shift
  status=0
  (cd "$d/repo" && PATH="$work/bin:$PATH" TMPDIR="$d/tmp" STUB_SIX="$six" bash "$script" "$@") >"$work/out" 2>&1 || status=$?
}
# clean <case>: no worktree and no temp dir are left, apart from out-dirs.
clean() {
  local d="$work/$1" n left
  n="$(git -C "$d/repo" worktree list | wc -l | tr -d ' ')"
  [[ "$n" == 1 ]] || fail "$1: $n worktrees left: $(git -C "$d/repo" worktree list)"
  left="$(find "$d/tmp" -mindepth 1 -maxdepth 1 -name 'tmp.*')"
  [[ -z "$left" ]] || fail "$1: temp dirs left: $left"
}
# refused <case> <message> [args...]: exit 1 with the message, clean, and no
# tarball in the out-dir.
refused() {
  local name="$1" msg="$2"
  shift 2
  run "$name" "$@"
  [[ "$status" == 1 ]] || fail "$name: exit $status, want 1: $(cat "$work/out")"
  says "$name" "$msg"
  clean "$name"
  if [[ -n "$(find "$work/$name" -name '*.tgz' -not -path '*/origin.git/*')" ]]; then
    fail "$name: a tarball was left: $(find "$work/$name" -name '*.tgz')"
  fi
}
tag=packages/sdk-typescript/v0.1.0

repo_fixture annotated
git -C "$work/annotated/repo" tag -a -m v0.1.0 "$tag"
git -C "$work/annotated/repo" push -q origin "$tag"
run annotated 0.1.0 "$work/annotated/out"
[[ "$status" == 0 ]] || fail "annotated: exit $status: $(cat "$work/out")"
clean annotated
[[ -f "$work/annotated/out/email-platform-sdk-0.1.0.tgz" ]] || fail "annotated: no tarball in the out-dir"
says annotated "gh release create $tag $work/annotated/out/email-platform-sdk-0.1.0.tgz --repo test/annotated --verify-tag"
commit="$(git -C "$work/annotated/repo" rev-parse HEAD)"
grep -qF "Source commit: \`$commit\`" "$work/annotated/out/release-notes.md" || fail "annotated: notes lack the commit"
grep -qF "Integrity: \`$(sri "$work/annotated/out/email-platform-sdk-0.1.0.tgz")\`" "$work/annotated/out/release-notes.md" ||
  fail "annotated: notes lack the tarball's integrity"
if grep -q "DRY RUN" "$work/annotated/out/release-notes.md"; then fail "annotated: notes say dry run"; fi

repo_fixture lightweight
git -C "$work/lightweight/repo" tag "$tag"
git -C "$work/lightweight/repo" push -q origin "$tag"
run lightweight 0.1.0 "$work/lightweight/out"
[[ "$status" == 0 ]] || fail "lightweight: exit $status: $(cat "$work/out")"
clean lightweight

repo_fixture no-tag
refused no-tag "tag $tag does not exist here" 0.1.0 "$work/no-tag/out"

repo_fixture unpushed
git -C "$work/unpushed/repo" tag -a -m v0.1.0 "$tag"
refused unpushed "tag $tag is not on origin" 0.1.0 "$work/unpushed/out"

repo_fixture moved
git -C "$work/moved/repo" tag -a -m v0.1.0 "$tag"
git -C "$work/moved/repo" push -q origin "$tag"
git -C "$work/moved/repo" commit -q --allow-empty -m later
git -C "$work/moved/repo" push -q origin main
git -C "$work/moved/repo" tag -f -a -m v0.1.0 "$tag" >/dev/null
refused moved "points to" 0.1.0 "$work/moved/out"

repo_fixture off-main
git -C "$work/off-main/repo" checkout -q -b side
git -C "$work/off-main/repo" commit -q --allow-empty -m side
git -C "$work/off-main/repo" tag -a -m v0.1.0 "$tag"
git -C "$work/off-main/repo" push -q origin "$tag"
refused off-main "is not on origin's main" 0.1.0 "$work/off-main/out"

repo_fixture mismatch
git -C "$work/mismatch/repo" tag -a -m v0.2.0 packages/sdk-typescript/v0.2.0
git -C "$work/mismatch/repo" push -q origin packages/sdk-typescript/v0.2.0
refused mismatch "says version 0.1.0, not 0.2.0" 0.2.0 "$work/mismatch/out"

repo_fixture not-github
git -C "$work/not-github/repo" remote set-url origin "$work/not-github/origin.git"
git -C "$work/not-github/repo" tag -a -m v0.1.0 "$tag"
git -C "$work/not-github/repo" push -q origin "$tag"
refused not-github "origin is not a GitHub repository URL" 0.1.0 "$work/not-github/out"

repo_fixture exists
git -C "$work/exists/repo" tag -a -m v0.1.0 "$tag"
git -C "$work/exists/repo" push -q origin "$tag"
mkdir "$work/exists/out"
refused exists "exists; remove it" 0.1.0 "$work/exists/out"

for c in dirty extra-file old-node; do
  repo_fixture "$c"
  git -C "$work/$c/repo" tag -a -m v0.1.0 "$tag"
  git -C "$work/$c/repo" push -q origin "$tag"
done
STUB_DIRTY=1 refused dirty "changed files of the commit" 0.1.0 "$work/dirty/out"
STUB_EXTRA=1 refused extra-file "> package/.env" 0.1.0 "$work/extra-file/out"
STUB_NODE=v1.2.3 refused old-node "the build ran on Node v1.2.3, the package needs ^$node_major" 0.1.0 "$work/old-node/out"

repo_fixture dry
run dry --dry-run
[[ "$status" == 0 ]] || fail "dry: exit $status: $(cat "$work/out")"
clean dry
says dry "dry run, nothing to publish"
lacks dry "gh release create"
[[ "$(head -1 "$work/dry/tmp/sdk-typescript-release-0.1.0-dry-run/release-notes.md")" == "DRY RUN of HEAD — not for publishing" ]] ||
  fail "dry: notes are not marked: $(head -1 "$work/dry/tmp/sdk-typescript-release-0.1.0-dry-run/release-notes.md")"

if ((failures > 0)); then
  echo "release-sdk-typescript_test: $failures failure(s)" >&2
  exit 1
fi
echo "release-sdk-typescript_test: all checks passed"
