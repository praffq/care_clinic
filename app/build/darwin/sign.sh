#!/usr/bin/env bash
set -euo pipefail

directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
codesign --force --sign - --entitlements "$directory/entitlements.plist" "${1:?Missing Wails build output}"
