#!/bin/sh
# Build care-clinic-service and ad-hoc sign it with the virtualization
# entitlement, which Virtualization.framework requires.
# Usage: app/cmd/care-clinic-service/build.sh [output]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here/../../../dist/care-clinic-service}
mkdir -p "$(dirname "$out")"
# ctl embeds the deployment kit (app/install): stage the current one.
node "$here/../../frontend/scripts/stage-install.mjs"
version=$(sed -n '/^desktop:/,/^[^ ]/s/^ *version: *"\(.*\)".*/\1/p' "$here/../../../versions.yml" | head -n 1)
cd "$here"
CGO_ENABLED=1 go build -ldflags "-X main.version=${version:-dev}" -o "$out" .
codesign --force -s - --entitlements "$here/care-clinic-service.entitlements" "$out"
echo "built $out"
