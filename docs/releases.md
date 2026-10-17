# Releases

`versions.toml` is the maintainer source of truth for desktop, engine and CARE
versions, dependency constraints, upstream branches, tools and support-image
pins. `deployments/.env` contains only local image tag names. Build staging
merges the TOML configuration into the embedded deployment; edit source configuration,
not generated `app/install/` files.

Only CARE 3.1 ships. CARE image publication and desktop/engine packaging are
independent builds. The desktop UI does not expose a CARE version selector.
See [CARE images](images.md) and [development and release](development-and-release.md).

## Publication

The shared release dispatcher detects changed inputs and prepares desktop,
engine, CARE and support-image artifacts as needed. It publishes digest-pinned
images, installer checksums, provenance hashes and component requirements in
the update feed. Installers are published as stable (not prerelease) versions,
including approved unsigned development releases. Published assets and tags
are never replaced.

Signing is optional **only when no settings are configured**:

| Settings | Present and complete | Absent |
| --- | --- | --- |
| Windows: secret `SIGNPATH_API_TOKEN`; variables `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG` | Sign application, uninstaller, then rebuilt NSIS installer. | Publish unsigned Windows installer (SmartScreen may warn). |
| macOS secrets: `MACOS_CERT_P12`, `MACOS_CERT_PASSWORD`, `MACOS_SIGN_IDENTITY`, `APPSTORE_PRIVATE_KEY`, `APPSTORE_KEY_ID`, `APPSTORE_ISSUER_ID` | Developer ID sign and notarize application and DMG. | Retain Wails' ad-hoc signature (not trusted by Gatekeeper). |
| Repository variable `UPDATES_SIGNING_ENABLED=true`; protected `release-signing` environment secret `UPDATES_SIGNING_KEY` | Build signed-feed clients; after existing environment approval, publish `updates.json` and `updates.json.sig`. | With the variable absent or `false`, build explicit `unsigned-development` clients; publish only `updates-unsigned-development.json`. |

Partially configured installer signing fails, matching the original Windows
release behavior. Failed signing/notarization never falls back to unsigned.
The non-secret variable selects update trust before building; the private key
remains accessible only to the protected signed-publication job. Keep existing
`release-signing` reviewers/protection and store `UPDATES_SIGNING_KEY` there,
not in repository secrets. Unsigned publication uses the unprotected
`unsigned-development` environment without a new manual gate or signing secrets.
If signing is selected but the protected key is missing or invalid, publication
fails without downgrading. The
Ed25519 private key must match an embedded public trust anchor; the checked-in
bootstrap anchors have no retained private keys and must be replaced with
operator-held public keys before enabling signed releases. Never commit a private
key. An invalid/mismatched key fails publication rather than producing an
unsigned feed.

## Runtime verification

By default, `app/internal/release` verifies exact manifest bytes before decoding
or using advertised metadata. CI embeds `unsigned-development` with a linker
flag only when `UPDATES_SIGNING_ENABLED` is absent or `false`; this is independent of
numeric version stability or the `-dev` version suffix. It requests a separate
HTTPS feed without fetching a signature. It still enforces full manifest
validation, digest-pinned images, asset SHA-256 checks, provenance and revision
checks, but **does not authenticate the publisher**. Local Wails builds default
to signed mode; to test the unsigned development feed explicitly, run:

```sh
cd app
wails build -ldflags "-X github.com/ohcnetwork/care_desktop/app/internal/release.updateTrust=unsigned-development"
```

Both modes persist the highest accepted revision atomically. Their cache files
are separate (`updates/verified.json` and `updates/unsigned-development.json`).
Lower revisions and changed content reusing a revision are rejected. A signed
client never automatically downgrades after a missing/invalid signature or a
network error. Changing trust mode requires a new desktop version and installer;
publishing a new Pages artifact replaces the previous feed, so signed clients
will fail closed if only the development feed is deployed.

CARE images are prepared under local `-next` tags. A backend update takes an
encrypted safety backup before applying images or migrating the database.
Current image records support offline startup.

## Recovering the initial fork release

Desktop `v0.0.1` embedded all-zero engine bundle hashes and required signed
metadata. Its missing feed cannot be fixed by publishing unsigned metadata:
those installed clients will reject it, and their bootstrap pins cannot change
server-side. Install the replacement `v0.0.2` desktop after this change is merged
and released. Do not overwrite the immutable `v0.0.1` assets/tag.

The replacement embeds actual `care-engine-v0.1.3` bundle hashes from the fork's
published asset digests and `SHA256SUMS`. Native Linux agents use the same
release's native-asset hashes/sizes from validated metadata. On first successful
feed publication, the planner reuses an existing engine only if its recorded
source hash matches the current engine inputs, all required asset digests/sizes
are present, and VM bundle digests match bootstrap pins. A mismatch fails and
requires a new engine version; it never replaces assets or invents provenance.

After merging, allow the existing Publish workflow on `main` (or dispatch it
there) to produce the new installers and atomically deploy the matching feed.
GitHub Pages must use GitHub Actions as its source. Without signing secrets,
this is an explicitly unsigned development release, not a production-authenticated
distribution. Download the new installer from the fork release and replace the
old desktop before retrying engine installation. Neither merging nor local
tests prove successful native installation/activation; check macOS and Windows
on isolated machines before deploying to a real clinic.

## Validation

From `app/`, run `go test ./internal/release ./internal/compose`.
From `app/frontend/`, run `npm run build`.
Validate workflow YAML with `actionlint` when changing publication workflows.
