#!/usr/bin/env bash
# Builds the TypeScript SDK release tarball from a pushed tag
# (packages/sdk-typescript/vX.Y.Z) and prepares its GitHub release:
#
#   bash scripts/release-sdk-typescript.sh 0.1.0 [out-dir]
#   bash scripts/release-sdk-typescript.sh --dry-run [out-dir]   # from HEAD
#   (or make release-sdk-ts VERSION=0.1.0 / DRY_RUN=1, OUT=<dir>)
#
# The tag must be on origin, point to the same commit there, and that commit
# must be on origin's main. The build runs in a fresh git worktree of the
# tagged commit (no hooks, no line-ending conversion) with
# `pnpm install --frozen-lockfile`, never in the working checkout: pnpm pack
# ignores .git/info/exclude, so files that exist only locally could end up in
# the tarball. The build must leave the commit's files unchanged; the tarball
# must hold exactly package.json, README.md, LICENSE and the build output,
# carry no install script, and its entry point must import under the
# package's Node. Only then is it moved to the out-dir, with release notes
# (source commit, Node and pnpm versions, sha512), and the script prints the
# `gh release create` command; it publishes nothing itself. A dry run builds
# HEAD's committed state, skips the tag checks and marks its notes as such.
set -euo pipefail

pkg_rel=packages/sdk-typescript
pkg_name=@email-platform/sdk
tag_prefix=packages/sdk-typescript/v
exports='ApiError EmailPlatform TransportError WebhookVerificationError paginate verifyWebhook'
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

die() {
  echo "release: $*" >&2
  exit 1
}

# json_field <field>: one top-level field of the JSON on stdin.
json_field() {
  FIELD="$1" node -e 'let s = ""; process.stdin.on("data", (d) => (s += d)).on("end", () => console.log(JSON.parse(s)[process.env.FIELD]));'
}

# version_at <commit>: the package's version in that commit.
version_at() {
  git show "$1:$pkg_rel/package.json" | json_field version
}

# tag_commit <ref>: the commit a tag points to, from `git ls-remote` lines on
# stdin. An annotated tag lists its own object and, as <ref>^{}, the commit;
# a lightweight tag only the commit. Empty when the tag is not listed.
tag_commit() {
  TAG="$1" awk '{ sha[$2] = $1 } END { t = ENVIRON["TAG"]; print (sha[t "^{}"] != "" ? sha[t "^{}"] : sha[t]) }'
}

# github_repo <url>: owner/name of a GitHub remote URL, empty for others.
github_repo() {
  sed -nE 's#^(https://github\.com/|ssh://git@github\.com/|git@github\.com:)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)$#\2#p' <<<"${1%.git}"
}

# sri <file>: the file's integrity string as lockfiles record it.
sri() {
  echo "sha512-$(openssl dgst -sha512 -binary "$1" | openssl base64 -A)"
}

# check_tarball <tgz> <package dir> <version>: the tarball holds exactly
# package.json, README.md, LICENSE and every file of the package's dist/,
# the entry points among them; its package.json names this version and has
# no script that runs on install.
check_tarball() {
  local tgz="$1" dir="$2" version="$3" want got f packed hooks
  for f in dist/index.js dist/index.d.ts dist/browser.js; do
    [[ -f "$dir/$f" ]] || die "the build did not produce $f"
  done
  want="$(
    {
      printf 'package/%s\n' package.json README.md LICENSE
      (cd "$dir" && find dist -type f | sed 's|^|package/|')
    } | LC_ALL=C sort
  )"
  got="$(tar -tzf "$tgz" | LC_ALL=C sort)" || die "cannot list $tgz"
  if [[ "$got" != "$want" ]]; then
    echo "release: the tarball's files differ from the expected list (< expected only, > tarball only):" >&2
    while IFS= read -r f; do
      grep -qxF -- "$f" <<<"$got" || echo "  < $f" >&2
    done <<<"$want"
    while IFS= read -r f; do
      grep -qxF -- "$f" <<<"$want" || echo "  > $f" >&2
    done <<<"$got"
    exit 1
  fi
  packed="$(tar -xzOf "$tgz" package/package.json | json_field name) $(tar -xzOf "$tgz" package/package.json | json_field version)"
  [[ "$packed" == "$pkg_name $version" ]] ||
    die "the packed package.json says '$packed', expected '$pkg_name $version'"
  hooks="$(tar -xzOf "$tgz" package/package.json | node -e '
    let s = ""; process.stdin.on("data", (d) => (s += d)).on("end", () => {
      const scripts = JSON.parse(s).scripts ?? {};
      console.log(["preinstall", "install", "postinstall", "prepare"].filter((n) => n in scripts).join(" "));
    });')"
  [[ -z "$hooks" ]] || die "the packed package.json has install-time scripts: $hooks"
}

# check_import <tgz> <work dir> <node>: the unpacked tarball resolves as the
# package in a project that has nothing else installed, and its entry
# exports exactly the public names.
check_import() {
  local tgz="$1" work="$2" node_bin="$3" dest="$2/consumer/node_modules/$pkg_name"
  mkdir -p "$dest"
  tar -xzf "$tgz" -C "$dest" --strip-components=1
  printf '{"name":"consumer","private":true,"type":"module"}\n' >"$work/consumer/package.json"
  (cd "$work/consumer" && EXPORTS="$exports" "$node_bin" --input-type=module -e '
    const m = await import("@email-platform/sdk");
    const got = Object.keys(m).sort().join(" ");
    if (got !== process.env.EXPORTS) {
      console.error(`release: the entry exports "${got}", expected "${process.env.EXPORTS}"`);
      process.exit(1);
    }
  ') || exit 1
}

# repo, work and src are global: the EXIT trap runs after main returns.
repo="" work="" src=""
cleanup() {
  if [[ -n "$src" ]]; then
    git -C "$repo" worktree remove --force "$src" >/dev/null 2>&1 || true
  fi
  if [[ -n "$work" ]]; then rm -rf "$work"; fi
  if [[ -n "$repo" ]]; then git -C "$repo" worktree prune >/dev/null 2>&1 || true; fi
}

main() {
  local dry_run=false version="" out="" commit tag="" gh_repo="" pkg tgz notes node_v node_bin pnpm_v engine
  if [[ "${1:-}" == "--dry-run" ]]; then
    dry_run=true
    out="${2:-}"
  else
    version="${1:-}"
    out="${2:-}"
    [[ "$version" =~ $semver ]] ||
      die "usage: $0 <X.Y.Z> [out-dir] | --dry-run [out-dir] (got '${version}')"
  fi
  repo="$(git rev-parse --show-toplevel)"
  cd "$repo"

  if $dry_run; then
    commit="$(git rev-parse HEAD)"
    version="$(version_at "$commit")"
    [[ "$version" =~ $semver ]] || die "$pkg_rel/package.json at HEAD says version '$version', not X.Y.Z"
    [[ -z "$(git status --porcelain -- "$pkg_rel")" ]] ||
      echo "release: dry run builds HEAD; uncommitted changes in $pkg_rel are not included" >&2
  else
    tag="$tag_prefix$version"
    gh_repo="$(github_repo "$(git config --get remote.origin.url || true)")"
    [[ -n "$gh_repo" ]] || die "origin is not a GitHub repository URL"
    commit="$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}")" ||
      die "tag $tag does not exist here; create and push it first"
    local remote main_sha at_tag
    remote="$(git ls-remote --tags origin "refs/tags/$tag" "refs/tags/$tag^{}" | tag_commit "refs/tags/$tag")"
    [[ -n "$remote" ]] || die "tag $tag is not on origin; push it first (git push origin $tag)"
    [[ "$remote" == "$commit" ]] || die "tag $tag points to $remote on origin but to $commit here"
    main_sha="$(git ls-remote origin refs/heads/main | awk '$2 == "refs/heads/main" { print $1 }')"
    [[ -n "$main_sha" ]] || die "origin has no main branch"
    git cat-file -e "$main_sha^{commit}" 2>/dev/null ||
      die "origin's main ($main_sha) is not here; run git fetch origin main"
    git merge-base --is-ancestor "$commit" "$main_sha" ||
      die "$tag ($commit) is not on origin's main; tag a commit of main"
    at_tag="$(version_at "$commit")"
    [[ "$at_tag" == "$version" ]] || die "$pkg_rel/package.json at $tag says version $at_tag, not $version"
  fi

  if $dry_run; then
    out="${out:-${TMPDIR:-/tmp}/sdk-typescript-release-$version-dry-run}"
  else
    out="${out:-${TMPDIR:-/tmp}/sdk-typescript-release-$version}"
  fi
  mkdir -p "$(dirname "$out")"
  mkdir "$out" 2>/dev/null || die "$out exists; remove it or pass another out-dir"
  out="$(cd "$out" && pwd)"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tmp.XXXXXX")"
  src="$work/src"
  trap cleanup EXIT

  echo "release: building $pkg_name $version from $commit in a fresh worktree"
  git -c core.hooksPath=/dev/null -c core.autocrlf=false worktree add --detach --quiet "$src" "$commit"
  pkg="$src/$pkg_rel"
  (
    cd "$src"
    pnpm install --frozen-lockfile --ignore-scripts --filter "$pkg_name"
    pnpm --filter "$pkg_name" generate:check
    pnpm --filter "$pkg_name" test
    pnpm --filter "$pkg_name" build
  )
  [[ -z "$(git -C "$src" status --porcelain)" ]] ||
    die "install, test or build changed files of the commit: $(git -C "$src" status --porcelain | tr '\n' ' ')"
  node_v="$(cd "$src" && pnpm --filter "$pkg_name" exec node --version)"
  node_bin="$(cd "$src" && pnpm --filter "$pkg_name" exec node -p process.execPath)"
  engine="$(git show "$commit:$pkg_rel/package.json" | node -e 'let s = ""; process.stdin.on("data", (d) => (s += d)).on("end", () => console.log(JSON.parse(s).engines.node));')"
  [[ "$engine" =~ ^\^([0-9]+)$ ]] || die "engines.node '$engine' is not of the form ^N"
  [[ "$node_v" == "v${BASH_REMATCH[1]}."* ]] || die "the build ran on Node $node_v, the package needs $engine"
  pnpm_v="$(cd "$src" && pnpm --version)"

  cp "$src/LICENSE" "$pkg/LICENSE"
  mkdir "$work/pack"
  (cd "$pkg" && pnpm pack --pack-destination "$work/pack" >/dev/null)
  tgz="$work/pack/email-platform-sdk-$version.tgz"
  [[ -f "$tgz" ]] || die "pnpm pack did not write $(basename "$tgz")"
  check_tarball "$tgz" "$pkg" "$version"
  check_import "$tgz" "$work" "$node_bin"
  mv "$tgz" "$out/"
  tgz="$out/$(basename "$tgz")"

  notes="$out/release-notes.md"
  {
    if $dry_run; then
      echo "DRY RUN of HEAD — not for publishing"
      echo
    fi
    cat <<EOF
$pkg_name $version

- Source commit: \`$commit\`
- Built in a clean worktree with \`pnpm install --frozen-lockfile\`, Node $node_v, pnpm $pnpm_v
- Tarball: \`$(basename "$tgz")\`
- Integrity: \`$(sri "$tgz")\`

Install with \`pnpm add <URL of the tarball below>\`; your lockfile records
the integrity above. Usage: \`$pkg_rel/README.md\` at this tag.
EOF
  } >"$notes"

  echo "release: $tgz"
  echo "release: $(sri "$tgz")"
  echo "release: notes in $notes"
  if $dry_run; then
    echo "release: dry run, nothing to publish"
  else
    echo "release: check the notes, then publish with:"
    printf '  gh release create %q %q --repo %q --verify-tag --title %q --notes-file %q\n' \
      "$tag" "$tgz" "$gh_repo" "$pkg_name $version" "$notes"
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
