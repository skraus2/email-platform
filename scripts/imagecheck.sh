#!/usr/bin/env bash
# Known-vulnerability scan of every container image the production stack
# runs: the images built from this repository (app, dashboard, caddy, netns)
# and the third-party images deploy/compose.yaml pins by digest. Each image
# is exported with `docker save` and scanned by grype from that archive
# (no Docker socket in the scanner); scripts/imagecheck-eval.sh decides the
# result against the ignore rules in .grype.yaml.
#
# The app image's own Go binaries are excluded: govulncheck (make vulncheck)
# scans them with reachability analysis. Their base layer is scanned.
#
# Needs Docker and network (vulnerability database, ~200 MB download,
# ~3 GB unpacked, cached in GRYPE_DB_DIR and updated when a newer one is
# published).
set -euo pipefail
cd "$(dirname "$0")/.."

GRYPE_IMAGE=anchore/grype:v0.121.0@sha256:621d56d1d68106b787022c81546f7e0a36202a25d1239f0c20195fab8c16f7ea
DB_DIR="${GRYPE_DB_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/email-platform/grype-db}"
mkdir -p "$DB_DIR"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The scanner parses untrusted image archives: it runs without network,
# capabilities or a writable root, as the invoking user, with the database
# mounted read-only. Only the database update step has network access.
grype() { # [docker run options...] -- grype arguments...
  local opts=()
  while [[ "$1" != -- ]]; do opts+=("$1"); shift; done
  shift
  docker run --rm --user "$(id -u):$(id -g)" --read-only --tmpfs /tmp:rw,mode=1777 \
    --cap-drop ALL --security-opt no-new-privileges --memory 4g --pids-limit 512 \
    -e GRYPE_DB_CACHE_DIR=/db -e GRYPE_CHECK_FOR_APP_UPDATE=false \
    -v "$PWD/.grype.yaml:/config/grype.yaml:ro" ${opts[@]+"${opts[@]}"} \
    "$GRYPE_IMAGE" -c /config/grype.yaml "$@"
}

# name, then `docker build` arguments
build() {
  local name="$1"; shift
  echo "imagecheck: building $name"
  docker build -q -t "email-platform-$name:imagecheck" "$@" >/dev/null
  images+=("$name=email-platform-$name:imagecheck")
  built+=" $name "
}
images=()
built=""
build app -f Dockerfile .
build dashboard -f apps/dashboard/Dockerfile .
build caddy deploy/caddy
build netns deploy/netns
# Every image the stack runs is either built above or a third-party image
# pinned by digest, which is scanned exactly as pinned.
while read -r ref; do
  if [[ "$ref" =~ email-platform-([a-z]+):local ]]; then
    [[ "$built" == *" ${BASH_REMATCH[1]} "* ]] || { echo "imagecheck: deploy/compose.yaml runs $ref, which is not built here" >&2; exit 1; }
    continue
  fi
  [[ "$ref" =~ ^[^$@\ ]+@sha256:[0-9a-f]{64}$ ]] || { echo "imagecheck: image not pinned by digest: $ref" >&2; exit 1; }
  name="${ref%%[:@]*}"; name="${name##*/}"
  [[ " ${images[*]} " != *" $name="* ]] || { echo "imagecheck: two images named $name" >&2; exit 1; }
  echo "imagecheck: pulling $ref"
  docker pull -q "$ref" >/dev/null
  images+=("$name=$ref")
done < <(sed -n 's/^ *image: *//p' deploy/compose.yaml | sed 's/ *#.*//; s/^"//; s/"$//' | sort -u)
[[ ${#images[@]} -ge 6 ]] || { echo "imagecheck: expected at least 6 images, found ${#images[@]}" >&2; exit 1; }

echo "imagecheck: updating the vulnerability database"
grype -v "$DB_DIR:/db" -- db update >/dev/null

for entry in "${images[@]}"; do
  name="${entry%%=*}" ref="${entry#*=}"
  exclude=()
  [[ "$name" == app ]] && exclude=(--exclude '/app/*')
  echo "imagecheck: scanning $name ($ref)"
  docker save -o "$work/$name.tar" "$ref"
  grype --network none -e GRYPE_DB_AUTO_UPDATE=false -v "$DB_DIR:/db:ro" -v "$work/$name.tar:/in/image.tar:ro" -- \
    -q -o json ${exclude[@]+"${exclude[@]}"} docker-archive:/in/image.tar >"$work/$name.json"
  rm -f "$work/$name.tar"
done

# Optional copy of the raw grype reports for inspection.
if [[ -n "${IMAGECHECK_REPORTS:-}" ]]; then mkdir -p "$IMAGECHECK_REPORTS" && cp "$work"/*.json "$IMAGECHECK_REPORTS"/; fi
./scripts/imagecheck-eval.sh "$work"/*.json
