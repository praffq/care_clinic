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

## Generated settings and customization

The backend runtime image can be downloaded and relabelled for the clinic.
Additional backend plugins use the published backend builder image, install
into its virtual environment, then copy `/app/.venv` onto the runtime. Backend
settings are used only at container startup, never as build inputs.

Every frontend setting is compiled. `deployments/env-defaults.yml` supplies
the deliberate release defaults; `releasectl` validates and snapshots its `fe`
map into the run-start plan. `images/scripts/frontend-env.mjs` records exactly
those settings, including empty strings, as manifest `build_env`.

The only exception is `REACT_CARE_API_URL`. Its hard-coded
`https://care-clinic.invalid/__CARE_CLINIC_REACT_CARE_API_URL__` placeholder is
replaced by `images/frontend/40-care-api-url.sh` at startup. The script copies
the pristine bundle, substitutes only this origin, and removes stale compressed
copies. There is no runtime setting registry or general configuration helper.

The clinic compares its effective compiled settings with release `build_env`.
An exact match relabels the published runtime. Any difference (including an
explicit empty value, a removed value or the clinic title/languages) uses the
published frontend builder and `frontend-rebuild.Dockerfile`. Build inputs use
JSON and process environment variables rather than dotenv interpolation, so
literal dollars, quotes and empty strings survive compilation.

Updates prepare these customized images under `-next` tags. Immediately before
an approved update, the clinic checks those tags against the latest settings
and plugins again. No staged update is activated by Start or a settings save.

## Validation

Run `go test ./internal/envconfig ./internal/compose ./internal/careversions`
from `app/`, and `node --test images/scripts/*.test.mjs` from the repository root.
