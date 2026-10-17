#!/usr/bin/env bash
set -euo pipefail

app=${1:?Usage: verify-macos-entitlements.sh app-bundle}
executable=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$app/Contents/Info.plist")
codesign --verify --deep --strict "$app"
for arch in $(lipo -archs "$app/Contents/MacOS/$executable"); do
  entitlements=$(codesign -d --arch "$arch" --entitlements - --xml "$app")
  if [ -z "$entitlements" ]; then
    echo "::error::$arch executable has no entitlements"
    exit 1
  fi
  printf '%s' "$entitlements" | python3 -c '
import plistlib, sys
entitlements = plistlib.loads(sys.stdin.buffer.read())
if entitlements.get("com.apple.security.virtualization") is not True:
    raise SystemExit("missing com.apple.security.virtualization entitlement")
'
  echo "Verified virtualization entitlement: $arch"
done
