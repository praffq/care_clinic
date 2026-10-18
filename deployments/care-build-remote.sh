#!/bin/sh
# care-build-remote SLUG GIT_URL REF OUTDIR
#
# Builds a CARE frontend plugin (a module-federation remote) for serving
# same-origin under /plugins/SLUG/: clones GIT_URL at REF (branch, tag or
# commit), installs its dependencies and runs `vite build --base
# /plugins/SLUG/` into OUTDIR, which then holds assets/remoteEntry.js.
# REACT_* variables in the environment reach the remote's build (Vite
# envPrefix). The remote's own npm "build" script is not run: some add
# typechecks and tests that belong to the plugin's CI.
#
# Part of care-frontend-builder (deployments/frontend.Dockerfile); the engine
# uses it to build doctor-added plugins, the frontend image to pre-bake the
# curated ones.
set -eu

[ $# -eq 4 ] || { echo "usage: care-build-remote SLUG GIT_URL REF OUTDIR" >&2; exit 2; }
slug=$1 url=$2 ref=$3 out=$4
case "$slug" in
  [a-z]*) ;;
  *) echo "care-build-remote: invalid slug: $slug" >&2; exit 2 ;;
esac
case "$slug" in
  *[!a-z0-9_]*) echo "care-build-remote: invalid slug: $slug" >&2; exit 2 ;;
esac
case "$ref" in
  -*|"") echo "care-build-remote: invalid ref: $ref" >&2; exit 2 ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git -C "$work" init -q
git -C "$work" remote add origin "$url"
git -C "$work" fetch -q --depth 1 origin "$ref"
git -C "$work" checkout -q --detach FETCH_HEAD
echo "care-build-remote: $slug $url@$ref ($(git -C "$work" rev-parse HEAD))" >&2

cd "$work"
if [ -f package-lock.json ]; then
  npm ci --ignore-scripts --no-audit --no-fund
else
  npm install --ignore-scripts --no-audit --no-fund
fi
mkdir -p "$out"
npx --no-install vite build --base "/plugins/$slug/" --outDir "$out" --emptyOutDir
[ -f "$out/assets/remoteEntry.js" ] ||
  { echo "care-build-remote: $slug produced no assets/remoteEntry.js" >&2; exit 1; }
