#!/usr/bin/env bash
set -euo pipefail

cleanup() {
  if [ "$SIGN_MACOS" = "true" ]; then
    rm -f "$RUNNER_TEMP/cert.p12" "$RUNNER_TEMP/notary.p8"
    if [ -f "$RUNNER_TEMP/signing.keychain-db" ]; then
      security delete-keychain "$RUNNER_TEMP/signing.keychain-db"
    fi
  fi
}
trap cleanup EXIT

if [ "$SIGN_MACOS" = "true" ]; then
  umask 077
  keychain="$RUNNER_TEMP/signing.keychain-db"
  keychain_pw=$(uuidgen)
  security create-keychain -p "$keychain_pw" "$keychain"
  security set-keychain-settings -lut 21600 "$keychain"
  security unlock-keychain -p "$keychain_pw" "$keychain"
  printf '%s' "$CERT_P12" | base64 --decode > "$RUNNER_TEMP/cert.p12"
  security import "$RUNNER_TEMP/cert.p12" -k "$keychain" -P "$CERT_PASSWORD" \
    -T /usr/bin/codesign -T /usr/bin/security
  rm -f "$RUNNER_TEMP/cert.p12"
  security set-key-partition-list -S apple-tool:,apple:,codesign: \
    -s -k "$keychain_pw" "$keychain" > /dev/null
  security list-keychains -d user -s "$keychain" \
    $(security list-keychains -d user | tr -d '"')
  security find-identity -v -p codesigning "$keychain"
  SIGNING_KEYCHAIN="$keychain"
fi

mkdir -p incoming/app release-assets staging
ditto -x -k incoming/macos/desktop-macos-unsigned.zip incoming/app
app=incoming/app/care-clinic.app
test -d "$app"
actual=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app/Contents/Info.plist")
test "$actual" = "$VERSION"
codesign --verify --strict "$app"
if [ "$SIGN_MACOS" = "true" ]; then
  : "${SIGN_IDENTITY:?Missing MACOS_SIGN_IDENTITY}"
  : "${NOTARY_KEY:?Missing APPSTORE_PRIVATE_KEY}"
  : "${NOTARY_KEY_ID:?Missing APPSTORE_KEY_ID}"
  : "${NOTARY_ISSUER:?Missing APPSTORE_ISSUER_ID}"
  notary_key="$RUNNER_TEMP/notary.p8"
  (umask 077; printf '%s' "$NOTARY_KEY" > "$notary_key")
  notary=(--key "$notary_key" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER")
  field() { python3 -c 'import sys,json; print(json.load(sys.stdin)["'"$1"'"])'; }
  notarize() {
    local id status
    id=$(xcrun notarytool submit "$1" "${notary[@]}" \
      --no-wait --output-format json | field id)
    [ -n "$id" ] || { echo "::error::No notarization submission ID"; exit 1; }
    echo "submitted $1 -> $id"
    for attempt in 1 2 3 4 5 6; do
      xcrun notarytool wait "$id" "${notary[@]}" --timeout 2h && break
      echo "::warning::Notarization poll $attempt failed; retrying."
    done
    status=$(xcrun notarytool info "$id" "${notary[@]}" \
      --output-format json | field status)
    if [ "$status" != "Accepted" ]; then
      echo "::error::Notarization status: $status"
      xcrun notarytool log "$id" "${notary[@]}"
      exit 1
    fi
  }
  codesign --force --timestamp --options runtime \
    --keychain "$SIGNING_KEYCHAIN" --sign "$SIGN_IDENTITY" "$app"
  codesign --verify --strict "$app"
  ditto -c -k --keepParent "$app" incoming/notarize.zip
  notarize incoming/notarize.zip
  xcrun stapler staple "$app"
  xcrun stapler validate "$app"
else
  echo "::warning::MACOS_CERT_P12 is absent; macOS retains its ad-hoc signature only."
fi
ditto "$app" "staging/CARE Clinic.app"
ln -s /Applications staging/Applications
dmg="release-assets/CARE-Clinic-$VERSION-macos.dmg"
hdiutil create -volname "CARE Clinic" -srcfolder staging -ov -format UDZO \
  "$dmg"
if [ "$SIGN_MACOS" = "true" ]; then
  codesign --force --timestamp \
    --keychain "$SIGNING_KEYCHAIN" --sign "$SIGN_IDENTITY" "$dmg"
  notarize "$dmg"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
  spctl -a -vvv -t open --context context:primary-signature "$dmg"
fi
