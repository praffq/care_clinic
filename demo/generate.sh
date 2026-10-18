#!/bin/sh
# Regenerate the demo feed in demo/feed from dist/ (engine/build.sh output)
# and dist/images/ (image tarballs). Missing artifacts become placeholders.
# Extra flags are passed to `release demo`, e.g. --base-url.
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo/app"
exec go run ./cmd/release demo \
	--key "$repo/demo/dev.key" \
	--out "$repo/demo/feed" \
	--engine-dist "$repo/dist" \
	--images-dist "$repo/dist/images" \
	"$@"
