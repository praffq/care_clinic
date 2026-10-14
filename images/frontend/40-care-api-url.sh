#!/bin/sh
set -eu

# The sole runtime frontend setting. Only a hostname-derived HTTPS origin is valid.
case "${REACT_CARE_API_URL:-}" in
  https://*) host=${REACT_CARE_API_URL#https://} ;;
  *) echo "REACT_CARE_API_URL must be an HTTPS origin" >&2; exit 1 ;;
esac
case "$host" in
  ""|*[!a-zA-Z0-9.-]*) echo "Invalid clinic API hostname" >&2; exit 1 ;;
esac
cp -a /care-clinic/html/. /usr/share/nginx/html/
find /usr/share/nginx/html -type f \( -name '*.js' -o -name '*.mjs' -o -name '*.html' -o -name '*.css' -o -name '*.json' \) \
  -exec sed -i "s|https://care-clinic.invalid/__CARE_CLINIC_REACT_CARE_API_URL__|$REACT_CARE_API_URL|g" {} +
# Precompressed copies cannot retain the placeholder.
find /usr/share/nginx/html -type f \( -name '*.gz' -o -name '*.br' \) -delete
