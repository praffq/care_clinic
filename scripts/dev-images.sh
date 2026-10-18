#!/usr/bin/env bash
# Build the CARE Clinic images. Locally (Rancher Desktop / Docker Desktop) it
# writes docker-save tarballs into dist/images/; with --push (CI, see
# .github/workflows/images.yml) the same definitions are built and pushed to a
# registry. See docs/dev-images.md.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSIONS="$ROOT/versions.yml"
DEPLOY="$ROOT/deployments"
OUT="$ROOT/dist/images"
CACHE="$ROOT/dist/.cache"

ALL=(care-backend-A care-backend-B care-backend-C-broken care-frontend-A
     care-caddy care-backup postgres redis silo)
# Images we build (and, with --push, publish). The demo ones are opt-in there.
OWNED=(care-backend-A care-frontend-A care-caddy care-backup)
DEMO=(care-backend-B care-backend-C-broken)

usage() {
  cat <<EOF
Usage: scripts/dev-images.sh [--arch arm64|amd64] [--only NAME]... [--force] [--verify]
       scripts/dev-images.sh --push PREFIX [--tag TAG] [--arch arm64|amd64] [--only NAME]...

Builds/pulls the CARE Clinic images for linux/<arch> with the local docker,
writes dist/images/<name>.tar and <name>.tar.digest (sha256 image ID).

  --arch     engine architecture (default: host arch)
  --only     build just NAME (repeatable). Names: ${ALL[*]}
  --force    re-save tarballs even if the image ID is unchanged
  --verify   after building: docker image rm, docker load < tar, and check
             the loaded image ID equals .digest

Push mode (CI): build the images we own (default: ${OWNED[*]};
demo images via --only) with a docker-container buildx builder, push each as
PREFIX/<repo>:TAG-<arch> (registry cache at PREFIX/<repo>:buildcache-<arch>)
and write dist/images/push/<repo>-<arch>.json with the pushed digest. No
tarballs. Repos: $(for n in "${OWNED[@]}" "${DEMO[@]}"; do printf '%s ' "$(repo_of "$n")"; done)

  --push     registry prefix, e.g. ghcr.io/praffq/care_clinic
  --tag      tag base (default: dev)
EOF
}

# repo_of NAME: the registry repository an owned image is pushed to.
repo_of() {
  case "$1" in
    care-backend-A)        echo care-backend ;;
    care-frontend-A)       echo care-frontend ;;
    care-backend-B)        echo care-backend-demo-b ;;
    care-backend-C-broken) echo care-backend-demo-c-broken ;;
    *)                     echo "$1" ;;
  esac
}

case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *)             ARCH=$(uname -m) ;;
esac
ONLY=()
FORCE=0
VERIFY=0
PUSH=""
TAG=dev
while [ $# -gt 0 ]; do
  case "$1" in
    --arch)   ARCH=${2:?--arch needs a value}; shift 2 ;;
    --push)   PUSH=${2:?--push needs a registry prefix}; shift 2 ;;
    --tag)    TAG=${2:?--tag needs a value}; shift 2 ;;
    --only)   ONLY+=("${2:?--only needs a value}"); shift 2 ;;
    --force)  FORCE=1; shift ;;
    --verify) VERIFY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$ARCH" in arm64|amd64) ;; *) echo "unsupported --arch: $ARCH" >&2; exit 2 ;; esac
PLATFORM="linux/$ARCH"
for n in ${ONLY[@]+"${ONLY[@]}"}; do
  case " ${ALL[*]} " in *" $n "*) ;; *) echo "unknown image: $n" >&2; exit 2 ;; esac
  if [ -n "$PUSH" ]; then
    case " ${OWNED[*]} ${DEMO[*]} " in *" $n "*) ;;
      *) echo "--push publishes only images we build, not $n" >&2; exit 2 ;; esac
  fi
done
if [ -n "$PUSH" ]; then
  [ "$VERIFY" = 0 ] || { echo "--verify does not apply to --push" >&2; exit 2; }
  case "$PUSH" in *[A-Z]*|*/) echo "--push: want a lowercase prefix without a trailing /" >&2; exit 2 ;; esac
  case "$TAG" in *[!A-Za-z0-9_.-]*|"") echo "--tag: invalid tag: $TAG" >&2; exit 2 ;; esac
fi

command -v jq >/dev/null || { echo "jq is required (brew install jq)" >&2; exit 1; }

log() { printf '\033[1m==> %s\033[0m\n' "$*" >&2; }

# pin SECTION KEY [FIELD]: read a value from versions.yml. With FIELD, KEY's
# value is a flow map ({ repo: "...", ref: "..." }) and FIELD is picked from it.
pin() {
  awk -v s="$1" -v k="$2" -v f="${3:-}" '
    /^[^ #]/ { sec = $0; sub(/:.*/, "", sec); next }
    sec == s && $0 ~ "^  " k ":" {
      line = $0
      if (f == "") {
        sub(/^[^:]*:[ ]*/, "", line); sub(/[ ]+#.*$/, "", line); gsub(/"/, "", line)
        print line
      } else if (match(line, f ": *\"[^\"]*\"")) {
        v = substr(line, RSTART, RLENGTH); sub(/^[^"]*"/, "", v); sub(/"$/, "", v)
        print v
      }
      exit
    }' "$VERSIONS"
}

# env_pin KEY: read a value from deployments/.env (pins not yet in versions.yml).
env_pin() { sed -n "s/^$1=//p" "$DEPLOY/.env" | tail -n 1; }

need() { [ -n "$2" ] || { echo "missing pin: $1" >&2; exit 1; }; }

BE_REPO=$(pin care backend repo);  need care.backend.repo "$BE_REPO"
BE_REF=$(pin care backend ref);    need care.backend.ref "$BE_REF"
FE_REPO=$(pin care frontend repo); need care.frontend.repo "$FE_REPO"
FE_REF=$(pin care frontend ref);   need care.frontend.ref "$FE_REF"
POSTGRES=$(pin images postgres);   need images.postgres "$POSTGRES"
REDIS=$(pin images redis);         need images.redis "$REDIS"
CADDY=$(pin images caddy);         need images.caddy "$CADDY"
SILO=$(pin images silo);           need images.silo "$SILO"
CORAZA=$(env_pin CORAZA_VERSION);  need CORAZA_VERSION "$CORAZA"

# Local tags; the tarballs carry these names, the engine uses the digest.
# With --push: the registry ref the image is pushed as.
tag() {
  if [ -n "$PUSH" ]; then echo "$PUSH/$(repo_of "$1"):$TAG-$ARCH"
  else echo "care-clinic-dev/$(echo "$1" | tr "[:upper:]" "[:lower:]"):$ARCH"; fi
}

# source REPO REF LABEL: shallow checkout of github.com/REPO at REF into the
# cache; prints the directory. Re-fetches each run, so branch refs move.
source_dir() {
  local repo=$1 ref=$2 dir="$CACHE/src/$3"
  if [ ! -d "$dir/.git" ]; then
    git init -q "$dir"
    git -C "$dir" remote add origin "https://github.com/$repo.git"
  fi
  git -C "$dir" remote set-url origin "https://github.com/$repo.git"
  log "Fetching $repo@$ref"
  git -C "$dir" fetch -q --depth 1 origin "$ref" >&2
  git -C "$dir" checkout -q --force --detach FETCH_HEAD
  git -C "$dir" clean -qfdx
  echo "$dir"
}

# The image definitions below are shared by local builds and CI (--push):
# only the output (--load vs --push) and the cache differ.
BUILD_FLAGS=(--platform "$PLATFORM" --provenance=false --sbom=false)
if [ -n "$PUSH" ]; then BUILD_FLAGS+=(--push); else BUILD_FLAGS+=(--load); fi
# The repository these images are built from; GHCR links packages to it.
SELF_REPO=${GITHUB_REPOSITORY:-praffq/care_clinic}

empty_ctx() { local d="$CACHE/empty"; mkdir -p "$d"; echo "$d"; }

# Upstream commits the CARE images were built from (for --push metadata).
BE_SHA=""
FE_SHA=""

# buildx NAME ARGS...: docker buildx build of owned image NAME, tagged and
# labelled the same way locally and in CI.
buildx() {
  local name=$1 extra=() cache
  shift
  if [ -n "$PUSH" ]; then
    cache="$PUSH/$(repo_of "$name"):buildcache-$ARCH"
    mkdir -p "$CACHE/metadata"
    extra=(--cache-from "type=registry,ref=$cache"
           --cache-to "type=registry,ref=$cache,mode=max,image-manifest=true,oci-mediatypes=true"
           --metadata-file "$CACHE/metadata/$name-$ARCH.json")
  fi
  docker buildx build "${BUILD_FLAGS[@]}" ${extra[@]+"${extra[@]}"} \
    --label "org.opencontainers.image.source=https://github.com/$SELF_REPO" \
    --label "org.opencontainers.image.title=$(repo_of "$name")" \
    -t "$(tag "$name")" "$@"
}

backend_a_built=0
build_backend_a() {
  [ "$backend_a_built" = 1 ] && return
  local src
  src=$(source_dir "$BE_REPO" "$BE_REF" backend)
  BE_SHA=$(git -C "$src" rev-parse HEAD)
  buildx care-backend-A -f "$src/docker/prod.Dockerfile" \
    --label "network.ohc.care.upstream=https://github.com/$BE_REPO" \
    --label "network.ohc.care.upstream.revision=$BE_SHA" "$src"
  backend_a_built=1
}

build() {
  case "$1" in
    care-backend-A) build_backend_a ;;
    care-backend-B|care-backend-C-broken)
      build_backend_a
      local d="$ROOT/demo/images/${1#care-}"
      buildx "$1" --build-arg "BASE=$(tag care-backend-A)" \
        --label "network.ohc.care.upstream=https://github.com/$BE_REPO" \
        --label "network.ohc.care.upstream.revision=$BE_SHA" "$d" ;;
    care-frontend-A)
      local src
      src=$(source_dir "$FE_REPO" "$FE_REF" frontend)
      FE_SHA=$(git -C "$src" rev-parse HEAD)
      cp "$DEPLOY/frontend.env" "$src/.env.local"
      buildx "$1" \
        --label "network.ohc.care.upstream=https://github.com/$FE_REPO" \
        --label "network.ohc.care.upstream.revision=$FE_SHA" "$src" ;;
    care-caddy)
      buildx "$1" -f "$DEPLOY/caddy.Dockerfile" \
        --build-arg "CADDY_IMAGE=$CADDY" --build-arg "CORAZA_VERSION=$CORAZA" \
        "$(empty_ctx)" ;;
    care-backup)
      buildx "$1" -f "$DEPLOY/backup.Dockerfile" \
        --build-arg "POSTGRES_IMAGE=$POSTGRES" \
        "$(empty_ctx)" ;;
    postgres) docker pull -q --platform "$PLATFORM" "$POSTGRES" >/dev/null ;;
    redis)    docker pull -q --platform "$PLATFORM" "$REDIS" >/dev/null ;;
    silo)     docker pull -q --platform "$PLATFORM" "$SILO" >/dev/null ;;
  esac
}

ref_of() {
  case "$1" in
    postgres) echo "$POSTGRES" ;;
    redis)    echo "$REDIS" ;;
    silo)     echo "$SILO" ;;
    *)        tag "$1" ;;
  esac
}

image_id() { docker image inspect --format '{{.Id}}' "$1"; }

size_of() {
  awk -v b="$(wc -c <"$1" | tr -d ' ')" 'BEGIN {
    split("B KiB MiB GiB", u); i = 1
    while (b >= 1024 && i < 4) { b /= 1024; i++ }
    printf("%.1f %s", b, u[i]) }'
}

# tar_id TAR: the image ID `docker load` will report for TAR - the manifest
# that index.json names with io.containerd.image.name. For built images this
# equals the local image ID; for pulled multi-arch images `docker save
# --platform` narrows the index to that platform's manifest, so it is the
# per-platform manifest digest (not the index digest the local store shows).
tar_id() {
  local ids
  ids=$(tar -xOf "$1" index.json |
    jq -r '[.manifests[] | select(.annotations["io.containerd.image.name"])] |
           if length == 1 then .[0].digest else error("want 1 named manifest, got \(length)") end')
  if printf '%s' "$ids" | grep -Eqx 'sha256:[0-9a-f]{64}'; then echo "$ids"
  else echo "unexpected image ID in $1: $ids" >&2; return 1; fi
}

# save NAME: docker save into dist/images unless the tarball was already
# written from this local image ID for this platform.
save() {
  local name=$1 ref id tar="$OUT/$1.tar" stamp="$CACHE/stamps/$1"
  ref=$(ref_of "$name")
  id=$(image_id "$ref")
  if [ "$FORCE" = 0 ] && [ -f "$tar" ] && [ -f "$tar.digest" ] && [ -f "$stamp" ] &&
     [ "$(cat "$stamp")" = "$PLATFORM $id" ]; then
    log "$name unchanged ($(cat "$tar.digest"))"
    return
  fi
  log "Saving $ref -> dist/images/$name.tar"
  rm -f "$tar.digest" "$stamp"
  docker save --platform "$PLATFORM" -o "$tar.tmp" "$ref"
  tar_id "$tar.tmp" >"$tar.tmp.digest"
  mv "$tar.tmp" "$tar"
  mv "$tar.tmp.digest" "$tar.digest"
  mkdir -p "$CACHE/stamps"
  printf '%s %s\n' "$PLATFORM" "$id" >"$stamp"
}

verify() {
  local name=$1 ref want got
  ref=$(ref_of "$name")
  want=$(cat "$OUT/$name.tar.digest")
  docker image rm "$ref" >/dev/null
  docker load -q -i "$OUT/$name.tar" >/dev/null
  got=$(image_id "$ref")
  if [ "$got" != "$want" ]; then
    echo "VERIFY FAILED $name: loaded $got, .digest says $want" >&2
    return 1
  fi
  log "verified $name: docker load -> $got"
}

mkdir -p "$OUT" "$CACHE"
targets=("${ALL[@]}")
[ -n "$PUSH" ] && targets=("${OWNED[@]}")
[ ${#ONLY[@]} -gt 0 ] && targets=("${ONLY[@]}")

# record_push NAME: write dist/images/push/<repo>-<arch>.json for a pushed image.
record_push() {
  local name=$1 repo digest revision=""
  repo=$(repo_of "$name")
  case "$name" in
    care-backend-*)  revision=$BE_SHA ;;
    care-frontend-*) revision=$FE_SHA ;;
  esac
  digest=$(jq -r '."containerimage.digest" // empty' "$CACHE/metadata/$name-$ARCH.json")
  printf '%s' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' ||
    { echo "no pushed digest for $name in buildx metadata" >&2; return 1; }
  mkdir -p "$OUT/push"
  jq -n --arg name "$name" --arg repo "$repo" --arg ref "$PUSH/$repo" --arg arch "$ARCH" \
    --arg tag "$(tag "$name")" --arg digest "$digest" --arg revision "$revision" \
    '{name: $name, repo: $repo, ref: $ref, arch: $arch, tag: $tag, digest: $digest, revision: $revision}' \
    >"$OUT/push/$repo-$ARCH.json"
  log "pushed $(tag "$name") ($digest)"
}

if [ -n "$PUSH" ]; then
  for name in "${targets[@]}"; do
    log "[$PLATFORM] $name -> $(tag "$name")"
    build "$name"
    record_push "$name"
  done
  exit 0
fi

declare -a secs
i=0
for name in "${targets[@]}"; do
  log "[$PLATFORM] $name"
  start=$(date +%s)
  build "$name"
  save "$name"
  secs[$i]=$(( $(date +%s) - start ))
  i=$((i + 1))
done

failed=0
if [ "$VERIFY" = 1 ]; then
  for name in "${targets[@]}"; do verify "$name" || failed=1; done
fi

printf '\n%-22s %-71s %10s %7s\n' NAME "DIGEST ($PLATFORM)" SIZE TIME
i=0
for name in "${targets[@]}"; do
  printf '%-22s %-71s %10s %6ss\n' "$name" "$(cat "$OUT/$name.tar.digest")" \
    "$(size_of "$OUT/$name.tar")" "${secs[$i]}"
  i=$((i + 1))
done
exit "$failed"
