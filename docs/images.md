# CARE images

CARE Clinic ships only CARE 3.1. Backend and frontend branches and release
versions are maintainer inputs in `versions.toml`. There is no CARE version
picker or migration from CARE 3.

## Publication and verification

CI publishes backend and frontend runtime images and builder images by digest.
The signed `updates.json` manifest carries the source commits, source hashes,
image digests and frontend build settings. The application verifies the
Ed25519 signature and persists the highest accepted revision before using
image metadata. An invalid signature or rollback is refused.

`app/internal/careversions` adapts this verified metadata for compose.
`InstallDir/care-images.json` records installed and staged images. Existing
images continue to work offline; checking or downloading new images needs
internet. New images are prepared under local `-next` tags and applied after
a safety backup when the backend changes.

## Runtime settings and customization

The backend runtime image can be downloaded and relabelled for the clinic.
Additional backend plugins use the published backend builder image.

`images/frontend/runtime-keys.json` defines frontend settings filled in at
container startup. `images/scripts/frontend-env.mjs` compiles placeholders
for these settings and records `runtime_keys` and `build_env` in publication
metadata. Runtime-only changes do not trigger a frontend rebuild.
Changing another compiled setting uses the frontend builder image.

The frontend wrapper consists of `images/frontend/Dockerfile`,
`40-care-clinic-config.sh`, `nginx.conf` and the Go configuration helper.
Keep the placeholder format synchronized with
`app/internal/compose.PlaceholderPrefix`.

## Validation

Run `go test ./internal/compose ./internal/careversions` from `app/`,
and `go test ./...` from `images/frontend/configure/`.
`images/scripts/verify-frontend.sh` validates a published frontend image.
