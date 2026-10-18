#!/bin/sh
# Point demo/feed/current at rev-<n> and serve demo/feed on localhost.
# Usage: demo/serve.sh <1-7>   (PORT defaults to 8787)
set -eu
rev=${1:-}
feed=$(cd "$(dirname "$0")" && pwd)/feed
case "$rev" in
[1-7]) ;;
*) echo "usage: $0 <1-7>" >&2; exit 2 ;;
esac
if [ ! -d "$feed/rev-$rev" ]; then
	echo "$feed/rev-$rev missing; run demo/generate.sh first" >&2
	exit 1
fi
ln -sfn "rev-$rev" "$feed/current"
port=${PORT:-8787}
echo "serving $feed on http://localhost:$port (current -> rev-$rev)"
echo "feed URL: http://localhost:$port/current/updates.json"
exec python3 -m http.server --bind 127.0.0.1 --directory "$feed" "$port"
