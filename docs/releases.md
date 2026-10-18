# Releasing CARE Clinic

[Documentation index](README.md)

A desktop release starts on its own when a change to `desktop.version` in
[`versions.yml`](../versions.yml) is merged into `main`, and can also be
started by hand from GitHub Actions. Do not create or push a tag to start a
build. The workflow reads the selected commit's `versions.yml`, runs CI with
the pinned engine bundled into the macOS app, packages the CI-built
applications, creates the tag `desktop-v<version>`, and creates a **draft
prerelease**. Nothing is published to clinic users automatically.

## Signing

Signing uses the same secrets and variables as
[ohcnetwork/care_clinic](https://github.com/ohcnetwork/care_clinic/blob/main/.github/workflows/release.yml);
each platform is signed only when its settings are present:

| Platform | Enabled by | Without it |
| --- | --- | --- |
| Windows | Secret `SIGNPATH_API_TOKEN` and variables `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG` (see [Windows signing](#windows-signing-signpath)). All four or none: a partial set fails the release. | `::warning::`, the SignPath jobs are skipped and the CI-built installer ships unsigned. |
| macOS | Secret `MACOS_CERT_P12`, plus `MACOS_CERT_PASSWORD`, `MACOS_SIGN_IDENTITY`, `APPSTORE_PRIVATE_KEY`, `APPSTORE_KEY_ID`, `APPSTORE_ISSUER_ID` (see [macOS signing](#macos-signing-configuration)). | `::warning::`, the app is ad-hoc signed (`codesign -s -`), the nested service first with its virtualization entitlement, and nothing is notarized. |

None of these are set on `praffq/care_clinic`, so its releases are unsigned.
The draft's notes mark each unsigned platform **UNSIGNED** with what the user
will see (SmartScreen, Gatekeeper's "Open Anyway"), and `release-manifest.json`
records `signing.macos` (`developer-id-notarized` or `ad-hoc`) and
`signing.windows` (`signpath-foundation`, `signpath-test` or `unsigned`). Do
not tell users to disable OS protection.

The update feed is signed separately with `FEED_SIGNING_KEY`; see
[CI publishing](ci-publishing.md#feed-signing).

## Prepare a version

1. Create a PR that sets `desktop.version` in `versions.yml` to a new
   `X.Y.Z` (no `-dev`), for example `0.2.1`.
2. Make sure `engine.version` names an engine release that exists
   (`engine-v<version>`, built by `engine.yml`, with
   `care-engine-<version>-arm64.tar.gz` and `SHA256SUMS`). Its tarball is
   bundled into the macOS app.
3. Review the PR, let CI pass, and merge it into `main`. The merge starts the
   release; see [Build the release](#build-the-release).

`versions.yml` `desktop.version` is the only desktop version. The staging
script (`app/frontend/scripts/stage-install.mjs`) writes it to `app/wails.json`
`info.productVersion` and, as `CARE_CLINIC_VERSION`, to the top of the staged
`app/install/.env`; `deployments/.env` must not set it.

Do not add passwords, signing credentials, or clinic-specific data to
`versions.yml`: it is attached to the release.

## Build the release

Merging the version PR into `main` runs **Release CARE Clinic**. Its first
job, **Decide whether to release**, compares `desktop.version` with the
commit before the push: when it changed, the release continues; when only
other pins changed, every other job is skipped. Pushes that do not touch
`versions.yml` do not start the workflow at all.

1. Open **Actions -> Release CARE Clinic** and find the run for the merge.
2. Wait for every job to finish.
3. Open the resulting draft in the repository's **Releases** page.

To start a release by hand, for example to retry after a failure that needed a
new commit, select **Run workflow**, choose the approved branch (normally
`main`), and start the run. A manual run always releases; the version comes
from `versions.yml`. GitHub CLI equivalent:

```sh
gh workflow run release.yml --ref main
```

All jobs use the workflow run's selected source commit, even if the branch moves
while the build is running. The tag is created only after packaging succeeds.
For `desktop.version: "0.2.1"`, the tag is `desktop-v0.2.1`.

## What the workflow does

| Stage | Behavior |
| --- | --- |
| Gate | On a push to `main`, continue only when `desktop.version` differs from the previous commit; otherwise skip every job. A manual run always continues. |
| Validate | Require one `X.Y.Z` `desktop.version` and `engine.version`, reject a desktop version that already has a draft or published release, require the `engine-v<engine.version>` release with its arm64 tarball, and read the [SignPath configuration](#signing). |
| Check and build | Call the same CI workflow used by PRs (`ci.yml`, with `engine-tag`): lint, race tests, frontend checks, and real macOS (universal) and Windows native builds. The macOS build downloads the engine tarball, checks it against the release's `SHA256SUMS`, and bundles it with the service (see [bundle layout](#macos-app-bundle-layout)). |
| Windows signing | With the SignPath configuration, submit the CI-built `CARE Clinic.exe` and `uninstall.exe` for signing in one request, rebuild the NSIS installer around the signed files with the installer inputs CI produced, and submit the installer for signing. Each request waits for an approver. Without the configuration, these jobs are skipped and the CI installer is used unsigned. |
| macOS signing | Check the bundled service and engine. With `MACOS_CERT_P12`, sign the nested `care-clinic-service` with hardened runtime and its virtualization entitlement, then the app, notarize/staple it, then sign and notarize/staple the DMG. Without it, ad-hoc sign the service (with the entitlement) and the app. Either way, fail if the service lost the entitlement. |
| Package | Wrap the macOS app in a DMG and copy the signed (or CI-built unsigned) Windows installer. Verify macOS metadata matches the release version. |
| Record | Save `versions.yml`, the source commit, engine version, workflow run URL, signing status, and SHA-256 file checksums. |
| Draft | Verify the complete asset set, create/reuse the tag at the exact source commit, and create a draft prerelease whose notes state each platform's signing. Never replace an existing release. Runs after an unsigned package too: its condition allows the Windows signing jobs to be skipped, which GitHub's default job condition would not. |

Release runs are serialized and do not cancel an active release. CI invoked by
a release has a separate concurrency group from ordinary PR/main CI.

For version `0.2.1`, the draft has these five assets:

```text
CARE-Clinic-0.2.1-macos.dmg
CARE-Clinic-0.2.1-windows-amd64-setup.exe
versions.yml
release-manifest.json
SHA256SUMS
```

The manifest's `source_commit` identifies the desktop source and `engine` the
bundled engine; `versions.yml` records every release pin. Checksums detect
altered or incomplete downloads; they are **not** publisher signatures or
clinic TLS certificate fingerprints.

Damaged downloads are rejected without anyone comparing checksums by hand:

- **First install on Windows:** `project.nsi` sets `CRCCheck force`, so the
  installer verifies itself before it runs. A truncated or corrupted copy stops
  with NSIS's "Installer integrity check has failed… obtain a new copy" message,
  and `/NCRC` cannot skip the check. A test fails if the setting is removed.
- **First install on macOS:** the UDZO disk image carries its own checksums, and
  macOS refuses to open a damaged one.
- **In-app updates:** the app checks the installer against `SHA256SUMS`, retries
  a bad download once, then deletes it and asks the operator to update again. See
  [app updates](wails-application.md).

The draft's notes include a "Verify your download" section with the
`Get-FileHash` and `shasum -a 256` commands for checking a file by hand.

The combined package is also retained as the `care-release-assets` workflow
artifact for 14 days.

## Review and publish

Before publishing:

1. Check both installers and their checksums. Use a disposable clinic to check
   fresh setup (including the service install), browser access, and operation
   without internet after provisioning.
2. Add release notes describing visible changes, dependency changes, known
   limitations. Say so when the
   bundled service changed: machines that already have it need a service
   reinstall (see [the care-clinic-service](development-and-release.md#the-care-clinic-service)).
3. An application upgrade must preserve the clinic's CA identity and persistent
   data; client devices should not need to reinstall certificate trust.
4. Decide how to publish. The draft is created as a prerelease. The in-app
   updater only offers GitHub's **latest** release, which never includes
   prereleases, so installed clinics are offered this version only if you
   untick **Set as a pre-release** and publish it as the latest release. Leave
   it ticked to share a build with testers without offering it to clinics.
5. If distributing the current preview to testers, publish it **as a
   prerelease**, retaining the unsigned-download warnings.

A draft is not public, and a prerelease is not the normal GitHub "latest stable"
download. Publishing a stable release is a separate maintainer decision.

Once published, do not move the tag or replace its files. Corrections require a
new version and another `versions.yml` PR. A database downgrade is not guaranteed
by reinstalling an older desktop binary.

## Failure and retry

| Failure | Action |
| --- | --- |
| Validation, CI, or packaging failed | Fix the issue. Rerun the failed job for a transient problem, or start a new run for the corrected commit. No tag/release is created before the draft job. |
| Version already has a release | Bump `desktop.version` in `versions.yml`. Never delete a published release to reuse its version. |
| Engine release missing | Release `engine-v<engine.version>` with `engine.yml` first, or pin an existing engine version. |
| Existing tag points to another commit | Choose a new version. The workflow will not move the tag. |
| Tag created, but draft creation failed | Rerun from the same commit. A matching existing tag can be reused if no release exists. |
| Draft created, but asset upload failed | Inspect it. Delete only that incomplete, unpublished draft, retain its matching tag, and rerun from the same commit. Never publish a partial draft. |
| Tag creation rejected | Check repository tag rules and Actions permissions. Only the final job requests `contents: write`; configure repository rules to allow the intended release actor without allowing tag replacement. |

Do not restart a release from a different commit using an already reserved
version/tag. The selected commit and configuration are part of the release's
identity.

## Scope and current limitations

The process does not deploy to clinics or prebuild the upstream CARE container
images. Initial clinic setup still needs the source and image downloads
described in [onboarding](onboarding.md). Image tags and downstream dependency
downloads are not guaranteed immutable.

PR CI builds the macOS app without an engine; release builds bundle the
`engine.version` tarball (see [bundle layout](#macos-app-bundle-layout)).
Without one, setup uses an engine already installed in the service or fails
with a message naming `ctl install`. Windows releases contain the app only:
there is no Windows service yet, so a Windows computer cannot be set up as a
server.

CARE Clinic itself is offered from this release page under Advanced ->
Updates, and is always operator-initiated. CARE backend and frontend updates
were removed in v2 and return later via engine agent ops.

## macOS app bundle layout

[`scripts/bundle-macos.sh`](../scripts/bundle-macos.sh) runs as the Wails
`postBuildHooks` entry for `darwin/*` in [`app/wails.json`](../app/wails.json),
after every macOS `wails build`. It adds:

```text
CARE Clinic.app/Contents/
|-- MacOS/
|   |-- CARE Clinic
|   `-- care-clinic-service                     # the host service
`-- Resources/engine/                           # optional
    |-- care-engine-<version>-arm64.tar.gz
    `-- SHA256SUMS
```

| Item | Source |
| --- | --- |
| `care-clinic-service` | Built from `app/cmd/care-clinic-service` with CGO (`lipo` of arm64 and amd64 for `darwin/universal`), or copied from `CARE_CLINIC_SERVICE_BINARY`. Ad-hoc signed with [`care-clinic-service.entitlements`](../app/cmd/care-clinic-service/care-clinic-service.entitlements) (`com.apple.security.virtualization`). |
| Engine tarball | `CARE_CLINIC_ENGINE_BUNDLE`, else the newest `dist/care-engine-*-arm64.tar.gz`, else none. Its `SHA256SUMS` line is taken from a `SHA256SUMS` beside the tarball, or computed. |

The app is then re-signed ad-hoc without `--deep` (which would drop the
service's entitlements) and checked with `codesign --verify --strict`. The
release workflow re-signs both, the service first with the same entitlements,
then the app: with the Developer ID, `--options runtime` and a timestamp when
`MACOS_CERT_P12` is set, otherwise ad-hoc again.

At run time [`internal/bundle`](../app/internal/bundle) reads these files, and
the same two variables override them, for example under `wails dev`, where
there is no bundle. Onboarding copies the service binary into place with one
administrator prompt ([onboarding](onboarding.md)); setup uploads the engine
tarball unless the same or a newer version is already active.

A service binary that changed does not replace an installed service. On a
machine where it is installed, reinstall it with
`sudo scripts/service-macos.sh install-built <binary>`.

## Windows signing (SignPath)

Windows releases are signed with a certificate issued to
[SignPath Foundation](https://signpath.org) under its free open-source program.
The obligations that come with it are published in the README's
[code signing policy](../README.md#code-signing-policy); keep that section
accurate when the team or the app's network behaviour changes.

### Configuration

| Setting | Kind | Purpose |
| --- | --- | --- |
| `SIGNPATH_API_TOKEN` | secret | API token of a SignPath user with the Submitter role. |
| `SIGNPATH_ORGANIZATION_ID` | variable | SignPath organization ID. |
| `SIGNPATH_PROJECT_SLUG` | variable | SignPath project slug. |
| `SIGNPATH_SIGNING_POLICY_SLUG` | variable | Slug of the signing policy: `test-signing` or `release-signing`. |

Set all four or none. With none, the release warns and ships the unsigned
installer. A partial configuration fails the release at validation instead of
silently producing unsigned installers.

### SignPath project

- Project `care_desktop`, created by SignPath Foundation. Its repository URL
  must match this repository's current name, `https://github.com/ohcnetwork/care_clinic.git`.
  Trusted build system GitHub.com.
- Artifact configuration:
  [`.github/signpath/artifact-configuration.xml`](../.github/signpath/artifact-configuration.xml).
  It signs the PE files inside a GitHub artifact (at most two) and rejects them
  unless product name, company name and product version match `app/wails.json`
  and the release version, which is the same check CI runs. Both requests use it.
- `release-signing` policy: the production certificate, origin verification on,
  allowed branch `main`, manual approval required.
- `test-signing` policy: SignPath's self-signed test certificate, no approval or
  origin checks. Used to prove the pipeline before the production certificate is
  issued.
- The workflow passes `version` as a parameter to both.

### During a release

1. **Sign Windows application** submits the CI-built `CARE Clinic.exe` and
   `uninstall.exe` and waits up to an hour for an approver.
2. **Build installer around the signed application** verifies both signatures,
   then runs makensis with the installer inputs CI produced, the signed application
   and `-DSIGNED_UNINSTALLER`, so the installer copies the signed uninstaller
   instead of generating an unsigned one at install time.
3. **Sign Windows installer** submits the installer; approve that request too.
4. Packaging continues with the signed installer, and the manifest records
   `"windows": "signpath-foundation"`.

### Test signing

With `SIGNPATH_SIGNING_POLICY_SLUG=test-signing` the release runs the same jobs,
with three differences:

- Step 2 also accepts a signature whose root certificate Windows does not trust
  (`UnknownError` or `NotTrusted`). A missing signature or `HashMismatch` still
  fails.
- The manifest records `"windows": "signpath-test"`.
- The draft notes say it is a test build. Test-signed installers still trigger
  SmartScreen, so never publish that draft; delete it instead. Its version is
  used up, so the next release takes the following version.

A rejected or timed-out request fails the release. Fix the cause and rerun the
same run; SignPath evaluates up to three re-runs of a build. Never submit a file
built anywhere other than this workflow.

### Signed uninstaller

NSIS normally writes `uninstall.exe` on the user's computer during installation,
so it could never be signed. `project.nsi` has two build modes for this:

| Define | Result |
| --- | --- |
| `-DWRITE_UNINSTALLER=<path>` | Builds `uninstall-writer.exe`, which only writes the uninstaller to `<path>` and exits. CI runs it and adds the uninstaller to the `desktop-windows-app` artifact. |
| `-DSIGNED_UNINSTALLER=<path>` | Packs the uninstaller at `<path>` and writes the Uninstall registry entries itself; the script's own uninstall section is left out. |

Without either define, as in `wails build -nsis`, the installer generates its
uninstaller as before. In every mode the uninstaller first asks the app whether
the clinic setup is gone; see
[removing the desktop app](cleanup-and-uninstall.md#3-removing-the-desktop-app). Keep the registry entries in the `SIGNED_UNINSTALLER`
branch in step with Wails' `wails.writeUninstaller` macro when upgrading Wails.

### Before the first signed release

1. Add the artifact configuration to the SignPath project as its default, then
   store the four settings above with the policy slug `test-signing`.
2. Bump `desktop.version` and let the [test-signed release](#test-signing)
   run. Check that the manifest says `signpath-test`, then delete the draft.
3. Ask SignPath to review the setup. Once the production certificate is in
   `release-signing`, change the policy slug variable to `release-signing`.
4. Bump `desktop.version` again (versions are never reused), run the
   release, and approve both signing requests.
5. Check that `release-manifest.json` says `signpath-foundation`, then publish
   the draft.

## macOS signing configuration

The secret names are the same as upstream:

| Secret | Purpose |
| --- | --- |
| `MACOS_CERT_P12` | Base64-encoded Developer ID signing certificate and private key. Its presence enables macOS signing. |
| `MACOS_CERT_PASSWORD` | Password used to import that PKCS#12 file. |
| `MACOS_SIGN_IDENTITY` | Existing Developer ID signing identity. |
| `APPSTORE_PRIVATE_KEY` | Existing App Store Connect API private key used by `notarytool`. |
| `APPSTORE_KEY_ID` | API key ID. |
| `APPSTORE_ISSUER_ID` | API issuer ID. |

The workflow keeps the existing `SIGN_MACOS`, `SIGNING_KEYCHAIN`, `CERT_P12`,
`CERT_PASSWORD`, `SIGN_IDENTITY`, `NOTARY_KEY`, `NOTARY_KEY_ID`, and
`NOTARY_ISSUER` environment mappings. It imports into a temporary keychain,
retains the separate submit/wait/status-check notarization flow, and validates
the stapled application and DMG. Cleanup runs even after failure.

If signing is enabled, incomplete credentials or a rejected notarization fail
the release; they do not silently produce an unsigned substitute. The job allows
up to six hours for Apple's queue, subject to the hosted runner limit.
When `MACOS_CERT_P12` is absent, the workflow warns, ad-hoc signs the service
(with its virtualization entitlement) and the app, skips notarization, and the
manifest says `ad-hoc`. Gatekeeper then blocks the first launch of the
downloaded app until it is allowed under System Settings, Privacy & Security.
