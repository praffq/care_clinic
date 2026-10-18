#!/bin/sh
# Builds two ad-hoc signed CARE Clinic app versions in a temp dir and runs the
# real self-update verification and helper between them (macOS only). Nothing
# outside the temp dir is touched and nothing is launched.
set -eu
cd "$(dirname "$0")/../app"
exec go test -count=1 -run 'TestMacSelfUpdateSwap|TestSwapScript|TestMacCopiedBundle' -v .
