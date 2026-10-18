#!/bin/sh
# Build vzboot and ad-hoc sign it with the virtualization entitlement, which
# Virtualization.framework requires. Usage: app/cmd/vzboot/build.sh [output]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here/../../../dist/vzboot}
mkdir -p "$(dirname "$out")"
cd "$here"
CGO_ENABLED=1 go build -o "$out" .
codesign --force -s - --entitlements "$here/vzboot.entitlements" "$out"
echo "built $out"
