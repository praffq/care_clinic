#!/bin/sh
# Put the host service (and, when given, an engine bundle) into the macOS app.
# Wails runs it after `wails build` (postBuildHooks in app/wails.json):
#
#   bundle-macos.sh <app bundle or its executable> <darwin/arm64|darwin/amd64|darwin/universal>
#
# Layout, read back by app/internal/bundle:
#
#   Contents/MacOS/care-clinic-service             the service; onboarding
#                                                  installs it as a LaunchDaemon
#   Contents/Resources/engine/care-engine-<v>-arm64.tar.gz   optional
#   Contents/Resources/engine/SHA256SUMS                     optional
#
# Inputs from the environment:
#   CARE_CLINIC_SERVICE_BINARY  a prebuilt service to ship instead of building one
#   CARE_CLINIC_ENGINE_BUNDLE   an engine tarball to ship; its SHA256SUMS line
#                               is taken from a SHA256SUMS next to it, or
#                               computed. Without it, the newest
#                               dist/care-engine-*-arm64.tar.gz is used, if any.
#
# The service is ad-hoc signed with its virtualization entitlement and the app
# is re-signed (not --deep, which would drop the service's entitlements).
# Release signing re-signs both with the Developer ID, service first.
set -eu

app=${1:?usage: bundle-macos.sh <app> <platform>}
platform=${2:?usage: bundle-macos.sh <app> <platform>}
case "$platform" in
darwin/*) ;;
*) exit 0 ;;
esac
case "$app" in
*/Contents/MacOS/*) app=${app%/Contents/MacOS/*} ;;
esac
[ -d "$app/Contents/MacOS" ] || { echo "bundle-macos: $app is not an app bundle" >&2; exit 1; }

repo=$(cd "$(dirname "$0")/.." && pwd)
src=$repo/app/cmd/care-clinic-service
entitlements=$src/care-clinic-service.entitlements
service=$app/Contents/MacOS/care-clinic-service
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

build() {
	(cd "$src" && CGO_ENABLED=1 GOOS=darwin GOARCH="$1" go build -trimpath -ldflags "-s -w" -o "$2" .)
}

if [ -n "${CARE_CLINIC_SERVICE_BINARY:-}" ]; then
	cp "$CARE_CLINIC_SERVICE_BINARY" "$work/care-clinic-service"
else
	case "$platform" in
	darwin/universal)
		build arm64 "$work/arm64"
		build amd64 "$work/amd64"
		lipo -create -output "$work/care-clinic-service" "$work/arm64" "$work/amd64"
		;;
	*) build "${platform#darwin/}" "$work/care-clinic-service" ;;
	esac
fi
install -m 0755 "$work/care-clinic-service" "$service"
codesign --force --sign - --entitlements "$entitlements" "$service"

engine=${CARE_CLINIC_ENGINE_BUNDLE:-}
if [ -z "$engine" ]; then
	engine=$(find "$repo/dist" -maxdepth 1 -name 'care-engine-*-arm64.tar.gz' 2>/dev/null | sort | tail -n 1)
fi
resources=$app/Contents/Resources/engine
rm -rf "$resources"
if [ -n "$engine" ]; then
	name=$(basename "$engine")
	mkdir -p "$resources"
	cp "$engine" "$resources/$name"
	sum=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1 }' "$(dirname "$engine")/SHA256SUMS" 2>/dev/null || true)
	[ -n "$sum" ] || sum=$(shasum -a 256 "$engine" | awk '{ print $1 }')
	printf '%s  %s\n' "$sum" "$name" >"$resources/SHA256SUMS"
	echo "bundle-macos: shipped $name"
else
	echo "bundle-macos: no engine bundle shipped (set CARE_CLINIC_ENGINE_BUNDLE or build one into dist/)"
fi

codesign --force --sign - "$app"
codesign --verify --strict "$app"
echo "bundle-macos: $service"
