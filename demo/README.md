# Demo update feed

A local, signed `updates.json` feed that walks a client through every update
scenario, for testing the update flow end to end on one machine. Feed format
and rules: [`docs/updates-feed.md`](../docs/updates-feed.md).

> [!WARNING]
> **`dev.key` is a deliberately public, development-only signing key.** It is
> committed so anyone can build demo feeds. Only builds tagged `-tags devfeed`
> trust its public half (`app/internal/feed/devkeys/dev.pub`); release builds
> never do. Do not use it for anything else.

## Local loop

```sh
# 1. (optional) build real engine bundles; otherwise placeholders are used
engine/build.sh arm64            # -> dist/care-engine-<ver>-arm64.tar.gz

# 2. (optional) put image tarballs in dist/images/<name>.tar (a later slice
#    builds them); see "Artifacts" below

# 3. generate demo/feed/rev-1 .. rev-7, signed with demo/dev.key
demo/generate.sh                 # extra flags go to `release demo`, e.g. --base-url

# 4. serve one revision at http://localhost:8787/current/updates.json
demo/serve.sh 1

# 5. run a devfeed build of the app against it (app side: later slice)
CARE_UPDATES_URL=http://localhost:8787/current/updates.json ...
```

Step through the scenarios by stopping the server and running
`demo/serve.sh <n>` with the next revision. Check a revision by hand with:

```sh
cd app && go run ./cmd/release verify --pub internal/feed/devkeys/dev.pub \
  --in ../demo/feed/rev-7/updates.json --allow-insecure [--min-revision 4]
```

## Scenarios

| Rev | Contents (`revision`) | Expected client behaviour |
| --- | --- | --- |
| 1 | desktop 0.2.0, engine 0.1.0, CARE `3.1.0+be.A.fe.A` (1) | fresh install: engine 0.1.0, then CARE be.A |
| 2 | + CARE `be.B` (newer `released_at`) (2) | CARE update be.A → be.B offered |
| 3 | + engine 0.1.1 (3) | engine update 0.1.0 → 0.1.1 offered |
| 4 | + CARE `be.C`, backend image intentionally broken (4) | update offered; fails its health check once applied |
| 5 | rev 3 content republished later, still revision 3 | rejected as a rollback after seeing revision 4 |
| 6 | rev 4 with one signature byte flipped | rejected: bad signature |
| 7 | + engine 0.2.0 requiring desktop `>=0.3.0`, not published (7) | engine 0.2.0 held back; no path when otherwise up to date |

`TestDemo` in `app/cmd/release` generates the feed and asserts every row with
`feed.Verify` and `feed.ResolvePlan`.

## Output layout

```
demo/feed/                       (git-ignored)
  rev-1/updates.json(.sig) .. rev-7/
  current -> rev-N               (set by serve.sh; generate sets rev-1)
  files/engine/care-engine-<ver>-<arch>.tar.gz   (links to dist/)
  files/images/<name>.tar                        (links to dist/images/)
  ARTIFACTS.txt                  which artifacts are real and which are placeholders
```

## Artifacts

URLs point at `<base-url>/files/...` (default `http://localhost:8787`).

- **Engine**: `dist/care-engine-<ver>-<arch>.tar.gz` from `engine/build.sh`
  for versions 0.1.0, 0.1.1 and 0.2.0 on arm64 and amd64. Present files are
  linked into `files/engine/` with their real SHA-256 and size.
- **Images**: `dist/images/<name>.tar` for `care-backend-A`, `care-backend-B`,
  `care-backend-C-broken`, `care-frontend-A`, `care-caddy`, `care-backup`,
  `postgres`, `redis` and `silo`. The digest is read from `<name>.tar.digest`
  (`sha256:<hex>`), else the tarball's OCI `index.json` (`docker save`). A
  tarball with neither is an error (a hash of the file would never match the
  digest of the loaded image). Present tarballs get a `tar` URL in the feed;
  placeholders are used only when a tarball is absent.
- **Desktop** installers are always placeholders.

Anything missing gets a deterministic placeholder digest (SHA-256 of a fixed
string) and size 1, and no file is served behind its URL: the feed still
verifies and resolves, but downloading a placeholder fails. `ARTIFACTS.txt`
lists what was used. Timestamps are fixed (published from 2026-10-01, expiring
2036-01-01), so regenerating with the same inputs gives identical files.
