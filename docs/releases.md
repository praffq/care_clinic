# Releases

`versions.toml` is the maintainer source of truth for desktop, engine and CARE
versions, dependency constraints, upstream branches, tools and support-image
pins. `deployments/.env` contains only local image tag names. Build staging
merges the TOML configuration into the embedded kit; edit source configuration,
not generated `app/install/` files.

Only CARE 3.1 ships. CARE image publication and desktop/engine packaging are
independent builds. The desktop UI does not expose a CARE version selector.
See [CARE images](images.md) and [development and release](development-and-release.md).

## Publication

The shared release dispatcher detects changed inputs and prepares desktop,
engine, CARE and support-image artifacts as needed. It publishes digest-pinned
images, installer checksums, provenance hashes and component requirements in
`updates.json`, followed by its detached Ed25519 signature.

Manifest publication requires an operator-held signing key matching an embedded
public trust anchor. Never commit a signing key. Unsigned publication is not an
acceptable fallback.

## Runtime verification

`app/internal/release` verifies exact manifest bytes before decoding or using
advertised metadata. It persists the highest accepted revision atomically with
the signed metadata. Lower revisions and changed content reusing a revision
are rejected. Network, signature or provenance failures are failures, not
an "up to date" result.

CARE images are prepared under local `-next` tags. A backend update takes an
encrypted safety backup before applying images or migrating the database.
Current image records support offline startup.

## Validation

From `app/`, run `go test ./internal/release ./internal/compose`.
From `app/frontend/`, run `npm run build`.
Validate workflow YAML with `actionlint` when changing publication workflows.
