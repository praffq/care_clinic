#!/usr/bin/env bash
# Build the CARE Clinic dev image tarballs into dist/images/ using the local
# docker (Rancher Desktop / Docker Desktop). See docs/dev-images.md.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSIONS="$ROOT/versions.yml"
DEPLOY="$ROOT/deployments"
OUT="$ROOT/dist/images"
CACHE="$ROOT/dist/.cache"

ALL=(care-backend-A care-backend-B care-backend-C-broken care-frontend-A
     care-caddy care-backup postgres redis silo)

usage() {
  cat <<EOF
Usage: scripts/dev-images.sh [--arch arm64|amd64] [--only NAME]... [--force] [--verify]

Builds/pulls the CARE Clinic images for linux/<arch> with the local docker,
writes dist/images/<name>.tar and <name>.tar.digest (sha256 image ID).

  --arch     engine architecture (default: host arch)
  --only     build just NAME (repeatable). Names: ${ALL[*]}
  --force    re-save tarballs even if the image ID is unchanged
  --verify   after building: docker image rm, docker load < tar, and check
             the loaded image ID equals .digest
EOF
}

case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *)             ARCH=$(uname -m) ;;
esac
ONLY=()
FORCE=0
VERIFY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --arch)   ARCH=${2:?--arch needs a value}; shift 2 ;;
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
done

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
tag() { echo "care-clinic-dev/$(echo "$1" | tr "[:upper:]" "[:lower:]"):$ARCH"; }

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

BUILD_FLAGS=(--platform "$PLATFORM" --load --provenance=false --sbom=false)

empty_ctx() { local d="$CACHE/empty"; mkdir -p "$d"; echo "$d"; }

backend_a_built=0
build_backend_a() {
  [ "$backend_a_built" = 1 ] && return
  local src sha
  src=$(source_dir "$BE_REPO" "$BE_REF" backend)
  sha=$(git -C "$src" rev-parse HEAD)
  docker buildx build "${BUILD_FLAGS[@]}" -f "$src/docker/prod.Dockerfile" \
    --label "org.opencontainers.image.source=https://github.com/$BE_REPO" \
    --label "org.opencontainers.image.revision=$sha" \
    -t "$(tag care-backend-A)" "$src"
  backend_a_built=1
}

build() {
  case "$1" in
    care-backend-A) build_backend_a ;;
    care-backend-B|care-backend-C-broken)
      build_backend_a
      local d="$ROOT/demo/images/${1#care-}"
      docker buildx build "${BUILD_FLAGS[@]}" --build-arg "BASE=$(tag care-backend-A)" \
        -t "$(tag "$1")" "$d" ;;
    care-frontend-A)
      local src sha
      src=$(source_dir "$FE_REPO" "$FE_REF" frontend)
      sha=$(git -C "$src" rev-parse HEAD)
      cp "$DEPLOY/frontend.env" "$src/.env.local"
      docker buildx build "${BUILD_FLAGS[@]}" \
        --label "org.opencontainers.image.source=https://github.com/$FE_REPO" \
        --label "org.opencontainers.image.revision=$sha" \
        -t "$(tag "$1")" "$src" ;;
    care-caddy)
      docker buildx build "${BUILD_FLAGS[@]}" -f "$DEPLOY/caddy.Dockerfile" \
        --build-arg "CADDY_IMAGE=$CADDY" --build-arg "CORAZA_VERSION=$CORAZA" \
        -t "$(tag "$1")" "$(empty_ctx)" ;;
    care-backup)
      docker buildx build "${BUILD_FLAGS[@]}" -f "$DEPLOY/backup.Dockerfile" \
        --build-arg "POSTGRES_IMAGE=$POSTGRES" \
        -t "$(tag "$1")" "$(empty_ctx)" ;;
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
[ ${#ONLY[@]} -gt 0 ] && targets=("${ONLY[@]}")

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
