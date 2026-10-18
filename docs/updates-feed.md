# Update feed: `updates.json`

Desktop clients never read [`versions.yml`](versions.md). They are meant to
read a signed `updates.json` feed, generated from it by the release tooling,
that lists every desktop, engine and CARE release a client may move to.

> Not consumed by the app yet. Today the app updates itself from GitHub's latest
> release ([app updates](desktop-workflows.md#app-updates)); engine and CARE
> updates were removed in v2 and return later via engine agent ops. The format and its
verification live in `app/internal/feed`; a complete example is
[`app/internal/feed/testdata/updates.json`](../app/internal/feed/testdata/updates.json),
which the tests sign and verify.

## Format

```json
{
  "schema": 1,
  "revision": 42,
  "published_at": "2026-10-01T00:00:00Z",
  "expires": "2026-11-01T00:00:00Z",
  "desktop": [{ "version": "0.2.0", "requires": { "engine": ">=0.1.0 <0.2.0" },
                "assets": { "darwin-arm64": { "url": "https://...", "sha256": "<64 hex>", "size": 1 } } }],
  "engine":  [{ "version": "0.1.1", "requires": { "desktop": ">=0.2.0" }, "data_schema": 1,
                "assets": { "arm64": { "url": "https://...", "sha256": "<64 hex>", "size": 1 } } }],
  "care":    [{ "version": "3.1.0+be.4f2a1c.fe.9b33d0", "released_at": "2026-09-28T10:00:00Z",
                "requires": { "desktop": ">=0.2.0", "engine": ">=0.1.0" },
                "migrations": true,
                "images": { "backend":  { "ref": "ghcr.io/...@sha256:<hex>", "digest": "sha256:<hex>" },
                            "frontend": { ... }, "caddy": { ... }, "backup": { ... } },
                "build_env": { "REACT_...": "..." } }],
  "support_images": { "postgres": { ... }, "redis": { ... }, "silo": { ... } },
  "blocked": ["engine:0.1.0"]
}
```

- `schema` must be `1`. `revision` is a positive integer that increases with
  every published feed. `published_at` must not be after `expires`.
- Versions are strict semver (`+build` allowed). Desktop and engine versions
  must be bumped for every release: no two entries may have the same precedence
  (build metadata is ignored for that check).
- CARE entries are versioned `<core>+be.<sha>.fe.<sha>` and CI adds one per
  commit without bumping the core, so several entries may share precedence
  (e.g. `3.1.0+be.A…` and `3.1.0+be.B…`). Each CARE entry therefore has a
  required `released_at` (RFC 3339, not after `published_at`) that is unique
  among CARE entries, and only the exact version string must be unique.
  CARE releases are ordered by semver precedence, then `released_at`
  (`CareRelease.Newer`).
- `requires` uses the comparator ranges from `versions.yml`. Desktop entries
  must require `engine`, engine entries must require `desktop`, CARE entries
  must require both; no component may require itself.
- Desktop asset keys are `darwin-arm64`, `darwin-amd64`, `windows-amd64`;
  engine asset keys are `arm64`, `amd64`. Each asset has a URL, a lowercase
  hex `sha256` and a positive `size`.
- `data_schema` is the engine's on-disk data layout version (>= 1).
- Every image is pinned by digest: `ref` must embed `@sha256:...` equal to
  `digest`. CARE releases need exactly the `backend`, `frontend`, `caddy` and
  `backup` roles; `support_images` exactly `postgres`, `redis` and `silo`.
  An optional `tar` URL points at an image tarball for development feeds that
  side-load locally built images; it must be saved under the `ref`'s
  repository (its `index.json` image name), which the coordinator checks.
- `build_env` is the `REACT_*` settings the release's frontend was built with
  (CI publishes `deployments/frontend.env`); keys and values follow the
  engine's frontend allowlist (`protocol.FrontendBuildKeys`). The desktop
  rebuilds the frontend only when a clinic's settings differ from it.
- `blocked` entries are `<desktop|engine|care>:<version>`; the update resolver treats them as withdrawn and never
  selects them (see [Update plans](#update-plans)).
- Unknown fields anywhere are errors.

## Signature

`updates.json.sig` is a detached Ed25519 signature over the exact bytes of
`updates.json`:

```json
{"key_id":"<16 hex chars>","sig":"<base64 signature>"}
```

`key_id` is the hex of the first 8 bytes of `sha256(public key)`. The client
looks up the key by id and verifies the signature **before** parsing the
manifest.

## Trust model

- **Release builds** trust only the production public keys embedded as
  `ProductionKeyCurrent` and `ProductionKeyNext` in
  `app/internal/feed/keys.go` (standard-base64 Ed25519 public keys). The
  private key never enters the repository.
- **Unsigned feeds**: both production keys are empty today (no
  `FEED_SIGNING_KEY` on this fork), so `feed.UnsignedFeedAllowed()` is true and
  the service accepts a feed with no `updates.json.sig` (`VerifyOptions.AllowUnsigned`),
  logging and reporting it as unsigned. A signature that is present is still
  verified against whatever keys the build trusts. As soon as a production key
  is compiled in, a missing or bad signature is rejected. See
  [CI publishing](ci-publishing.md#feed-signing).
- **Key rotation**: put the new key in `ProductionKeyNext` and ship a desktop
  release; once clients have it, sign with the new key and move it to
  `ProductionKeyCurrent`.
- **Dev builds** (`-tags devfeed`) additionally trust
  `app/internal/feed/devkeys/dev.pub` and set `feed.DevBuild`, which callers
  pass as `AllowInsecureURLs` so a local feed can use `http://localhost` URLs.
  The matching private key is **committed on purpose** at
  [`demo/dev.key`](../demo/dev.key) (base64 Ed25519 seed) so anyone can build
  and sign demo feeds. It is public and grants nothing: only `-tags devfeed`
  builds trust it. Never trust it in release builds and never use it for
  anything else; `TestDevKeyMatchesDevPub` keeps the pair in sync.

## Verification

`feed.Verify(data, sig, VerifyOptions{Keys, Now, MinRevision, AllowInsecureURLs})`
checks, in order: the signature (`ErrUnknownKey`, `ErrBadSignature`), strict
JSON decoding and `schema`, `revision >= MinRevision` (`ErrRollback`),
`now < expires` (`ErrExpired`), then every content rule above
(`ErrInvalid`, listing every problem with its field path). URLs must be
`https` unless `AllowInsecureURLs` is set.

After a successful verification the client records the feed in
`verified.json` with `feed.State` (`highest_revision`, `last_verified_at`,
`manifest_sha256`) via `LoadState` / `SaveState`, which writes atomically.
`highest_revision` is passed back as `MinRevision` next time so a replayed older
feed is rejected.

## Update plans

`feed.ResolvePlan(installed, manifest)` turns a verified manifest and the
installed versions into a `Plan`: an ordered list of `Step`s (component, from,
to, and the feed entry), at most one per component. It only decides; applying
a plan, one healthy step at a time, belongs to the update coordinator
([updates.md](updates.md)). Updates are never automatic.

Rules:

1. **Candidates.** For each component, the resolver considers releases that
   are newer than the installed one (CARE ordered by `CareRelease.Newer`, so
   `released_at` breaks ties at equal semver), not `blocked`, and that have an
   asset for this machine (`Installed.DesktopAsset` / `Installed.EngineArch`).
   Only the newest `MaxCandidates` (5) per component are tried. Nothing is
   ever downgraded.
2. **Compatibility after every step.** A candidate set is usable if some order
   of its steps keeps every installed component's `requires` satisfied by the
   others after *each* step. Requirements of installed versions are looked up
   in the feed by exact version; if an installed version is not in the feed,
   its requirements are unknown and only the other entries' are checked. CARE
   cannot be installed before an engine.
3. **Choosing a plan.** Every combination of candidates (plus "keep the
   installed one") is tried, and plans are compared by, in order: most
   blocked installed versions replaced; most components at their newest
   candidate; then the newest desktop, then engine, then CARE. Orders are
   tried as permutations of desktop, engine, CARE starting with that order,
   and the first valid one wins. The result is deterministic.
4. **Held back.** `Plan.Held` explains every component left below its newest
   candidate (e.g. `engine 0.2.0 requires desktop >=0.3.0, which is not
   published for this machine`), so CARE may still update while the engine
   waits for a desktop release.
5. **No path.** If newer releases exist but nothing can be installed, the
   resolver returns `*NoPathError` with the same reasons in `Details`. If
   everything is up to date it returns an empty plan.
6. **Fresh install.** With no engine installed (zero `Installed.Engine`) the
   plan installs the newest compatible engine and CARE for the installed
   desktop app; the desktop app itself is not updated during install.
7. **Blocked installed versions.** If an installed version is `blocked`, the
   plan moves off it whenever any path exists and sets `Plan.Urgent`; if none
   exists, that is reported like a held component.

### Example

Installed: desktop `0.1.0`, engine `0.1.0`, CARE `3.0.0+be.111.fe.111`.

| Release | requires |
| --- | --- |
| desktop `0.2.0` | engine `>=0.1.0 <0.2.0` |
| engine `0.1.1` | desktop `>=0.2.0` |
| CARE `3.1.0+be.4f2a1c.fe.9b33d0` | desktop `>=0.2.0`, engine `>=0.1.1` |

All three newest releases are compatible together, and the only order that
stays compatible after every step is:

1. desktop `0.1.0` → `0.2.0` (still accepts engine `0.1.0`),
2. engine `0.1.0` → `0.1.1` (needs desktop `0.2.0`),
3. CARE `3.0.0+be.111.fe.111` → `3.1.0+be.4f2a1c.fe.9b33d0` (needs both).

If the feed later adds engine `0.2.0` requiring desktop `>=0.3.0` while no
such desktop is published, the plan still takes engine `0.1.1` and the new
CARE, and `Held` reports why engine `0.2.0` was skipped. These cases are
covered by `TestResolvePlan` in `app/internal/feed/plan_test.go`.

## Release tooling: `app/cmd/release`

Run from `app/` (`go run ./cmd/release <command>`):

- `keygen --out K.key [--pub K.pub]` creates an Ed25519 key: the private key
  (base64 seed, mode 0600) and the public key (base64, the format of
  `devkeys/dev.pub` and `ProductionKey*`), and prints the key id. Existing
  files are never overwritten.
- `generate --versions versions.yml --artifacts A.json --revision N
  [--published-at T] --expires T [--previous updates.json]
  [--block component:version]... [--allow-insecure] --out updates.json`
  builds a manifest. Versions and `requires` come from `versions.yml`;
  assets, digests and CARE build revs from the artifacts file. With
  `--previous` the new entries are merged into the previous feed (whose
  revision must be lower), each component keeps its newest 5 entries, and
  `--block` appends to `blocked`. The output is checked with `feed.Verify`
  (signed with a throwaway key) before it is written.
- `sign --key K.key --in updates.json` writes `updates.json.sig`.
- `verify --pub K.pub --in updates.json [--min-revision N]
  [--allow-insecure]` prints a summary, or fails with the `Verify` error.
- `demo` builds the local test feed; see [`demo/README.md`](../demo/README.md).

Artifacts file (every section optional; a missing section publishes no new
entry for that component):

```json
{
  "desktop": { "assets": { "darwin-arm64": { "url": "https://...", "sha256": "<hex>", "size": 1 } } },
  "engine":  { "data_schema": 1, "assets": { "arm64": { "url": "https://...", "sha256": "<hex>", "size": 1 } } },
  "care": { "backend_rev": "4f2a1c", "frontend_rev": "9b33d0", "released_at": "2026-09-28T10:00:00Z",
            "migrations": true, "build_env": { "REACT_...": "..." },
            "images": { "backend": { "ref": "ghcr.io/.../care-backend", "digest": "sha256:<hex>", "tar": "https://..." },
                        "frontend": { ... }, "caddy": { ... }, "backup": { ... } } },
  "support_images": { "postgres": { "digest": "sha256:<hex>" }, "redis": { ... }, "silo": { ... } }
}
```

The CARE version is `<care.version>+be.<backend_rev>.fe.<frontend_rev>`.
Image refs may carry a tag (dropped) and may omit the digest (appended);
support images without a `ref` use the repository from `versions.yml`.
