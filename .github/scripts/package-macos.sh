#!/usr/bin/env bash
set -euo pipefail

mkdir -p ci/signing incoming/app release-assets staging
ditto -x -k incoming/macos/desktop-macos-unsigned.zip incoming/app
apps=(incoming/app/*.app)
test "${#apps[@]}" -eq 1 && test -d "${apps[0]}"
app="${apps[0]}"
actual=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app/Contents/Info.plist")
test "$actual" = "$VERSION"
codesign --verify --strict "$app"

cleanup() {
  rm -f ci/signing/cert.p12 ci/signing/notary.p8
  if [ -f ci/signing/signing.keychain-db ]; then
    security delete-keychain "$PWD/ci/signing/signing.keychain-db"
  fi
}
trap cleanup EXIT

configured=0
for value in "${CERT_P12:-}" "${CERT_PASSWORD:-}" "${SIGN_IDENTITY:-}" \
  "${NOTARY_KEY:-}" "${NOTARY_KEY_ID:-}" "${NOTARY_ISSUER:-}"; do
  if [ -n "$value" ]; then configured=$((configured + 1)); fi
done
if [ "$configured" -ne 0 ] && [ "$configured" -ne 6 ]; then
  echo "::error::macOS signing is partially configured"
  exit 1
fi
notarize() {
  local status
  status=$(xcrun notarytool submit "$1" --key "$PWD/ci/signing/notary.p8" \
    --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER" --wait --timeout 2h \
    --output-format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
  test "$status" = Accepted
}
if [ "$configured" -eq 6 ]; then
  umask 077
  keychain="$PWD/ci/signing/signing.keychain-db"
  keychain_pw=$(uuidgen)
  security create-keychain -p "$keychain_pw" "$keychain"
  security set-keychain-settings -lut 21600 "$keychain"
  security unlock-keychain -p "$keychain_pw" "$keychain"
  printf '%s' "$CERT_P12" | base64 --decode > ci/signing/cert.p12
  security import ci/signing/cert.p12 -k "$keychain" -P "$CERT_PASSWORD" \
    -T /usr/bin/codesign -T /usr/bin/security
  security set-key-partition-list -S apple-tool:,apple:,codesign: \
    -s -k "$keychain_pw" "$keychain" >/dev/null
  printf '%s' "$NOTARY_KEY" > ci/signing/notary.p8
  codesign --force --timestamp --options runtime \
    --entitlements app/build/darwin/entitlements.plist \
    --keychain "$keychain" --sign "$SIGN_IDENTITY" "$app"
  codesign --verify --strict "$app"
  ditto -c -k --keepParent "$app" incoming/notarize.zip
  notarize incoming/notarize.zip
  xcrun stapler staple "$app"
  xcrun stapler validate "$app"
else
  echo "::warning::macOS signing is not configured; retaining ad-hoc signature."
fi
ditto "$app" "staging/CARE Clinic.app"
ln -s /Applications staging/Applications
dmg="release-assets/CARE-Clinic-$VERSION-macos.dmg"
hdiutil create -volname "CARE Clinic" -srcfolder staging -format UDZO "$dmg"
if [ "$configured" -eq 6 ]; then
  codesign --force --timestamp --keychain "$keychain" --sign "$SIGN_IDENTITY" "$dmg"
  notarize "$dmg"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
  spctl -a -vvv -t open --context context:primary-signature "$dmg"
fi
