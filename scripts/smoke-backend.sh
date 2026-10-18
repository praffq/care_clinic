#!/usr/bin/env bash
# smoke-backend.sh IMAGE: check that a care-backend image starts with every
# plugin built into it enabled. On a throwaway docker network with postgres
# and redis, runs `manage.py check`, `migrate` and asserts each plugin is in
# INSTALLED_APPS, using deployments/backend.env plus dummy secrets - the
# same settings as a clinic. CI runs it before publishing a release (see
# .github/workflows/images.yml); it never touches an installed clinic.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=${1:?usage: scripts/smoke-backend.sh IMAGE}

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
log() { printf '\033[1m==> %s\033[0m\n' "$*" >&2; }

postgres=$(awk '/^images:/ { s = 1; next } /^[^ #]/ { s = 0 }
  s && /^  postgres:/ { sub(/^[^:]*:[ ]*/, ""); gsub(/"/, ""); print; exit }' "$ROOT/versions.yml")
redis=$(awk '/^images:/ { s = 1; next } /^[^ #]/ { s = 0 }
  s && /^  redis:/ { sub(/^[^:]*:[ ]*/, ""); gsub(/"/, ""); print; exit }' "$ROOT/versions.yml")
[ -n "$postgres" ] && [ -n "$redis" ] || { echo "postgres/redis pins missing in versions.yml" >&2; exit 1; }

# The backend plugins the image was built with (dev-images.sh label).
sources=$(docker image inspect --format '{{ index .Config.Labels "network.ohc.care.sources" }}' "$IMAGE")
[ -n "$sources" ] && [ "$sources" != "<no value>" ] ||
  { echo "$IMAGE has no network.ohc.care.sources label; build it with scripts/dev-images.sh" >&2; exit 1; }
# Enable all of them. Configs a plugin refuses to start without get dummies.
plugs=$(jq -c '
  {
    care_multihost_files_be: {ALLOWED_HOSTS: ["*.local", "*.ts.net"]},
    care_filly: {MEDISPEAK_API_KEY: "smoke-test", MEDISPEAK_BASE_URL: "https://medispeak.invalid"}
  } as $configs
  | [.plugins // {} | to_entries[] | select(.value.kind == "backend")
     | {name: .key, package_name: "git+https://github.com/\(.value.repo).git",
        version: "@\(.value.rev)", configs: ($configs[.key] // {})}]' <<<"$sources")
apps=$(jq -r '[.[].name] | join(" ")' <<<"$plugs")
log "Smoke-testing $IMAGE with plugins: ${apps:-none}"

id="care-smoke-$$"
work=$(mktemp -d)
cleanup() {
  docker rm -f "$id-db" "$id-redis" >/dev/null 2>&1 || true
  docker network rm "$id" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

pw=$(openssl rand -hex 16)
cat >"$work/secrets.env" <<EOF
DJANGO_SECRET_KEY=$(openssl rand -hex 32)
POSTGRES_PASSWORD=$pw
DATABASE_URL=postgres://postgres:$pw@db:5432/care
BUCKET_KEY=smoke
BUCKET_SECRET=smoke-secret
CSRF_TRUSTED_ORIGINS=["https://care.local"]
CORS_ALLOWED_ORIGINS=["https://care.local"]
BUCKET_EXTERNAL_ENDPOINT=https://care.local
EOF
printf 'ADDITIONAL_PLUGS=%s\n' "$plugs" >"$work/plugins.env"

docker network create "$id" >/dev/null
docker run -d --name "$id-db" --network "$id" --network-alias db \
  --env-file "$ROOT/deployments/backend.env" --env-file "$work/secrets.env" "$postgres" >/dev/null
docker run -d --name "$id-redis" --network "$id" --network-alias redis "$redis" >/dev/null

check='
import os, sys
from django.conf import settings
want = os.environ["SMOKE_APPS"].split()
missing = [a for a in want if a not in settings.INSTALLED_APPS]
if missing:
    sys.exit("plugins not in INSTALLED_APPS: " + " ".join(missing))
print("INSTALLED_APPS has:", *want)
'
# shellcheck disable=SC2016 # expanded in the container
docker run --rm --network "$id" \
  --env-file "$ROOT/deployments/backend.env" --env-file "$work/secrets.env" --env-file "$work/plugins.env" \
  -e SMOKE_APPS="$apps" -e SMOKE_CHECK="$check" \
  --entrypoint bash "$IMAGE" -c '
    set -e
    ./wait_for_db.sh
    ./wait_for_redis.sh
    python manage.py check
    python manage.py migrate --noinput
    python manage.py shell -c "$SMOKE_CHECK"
  '
log "smoke test passed: $IMAGE"
