#!/bin/sh
# Build the CARE engine VM bundle with Docker only.
# Usage: engine/build.sh <arm64|amd64>
set -eu
arch=${1:-}
case "$arch" in
arm64 | amd64) ;;
*) echo "usage: $0 <arm64|amd64>" >&2; exit 2 ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
version=$(tr -d '[:space:]' <"$here/VERSION")
dist="$repo/dist"
name="care-engine-$version-$arch"
mkdir -p "$dist"

docker buildx build \
	--platform "linux/$arch" \
	--build-arg "ENGINE_VERSION=$version" \
	--build-context "app=$repo/app" \
	--target bundle \
	--output "type=local,dest=$dist" \
	"$here"

# Rebuild SHA256SUMS from the per-bundle checksum files.
cat "$dist"/care-engine-*.tar.gz.sha256 | sort -k2 >"$dist/SHA256SUMS"
echo
echo "built $dist/$name.tar.gz ($(du -h "$dist/$name.tar.gz" | cut -f1))"
cat "$dist/SHA256SUMS"
