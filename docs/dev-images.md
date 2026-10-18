# Dev image tarballs

`scripts/dev-images.sh` builds every image the engine VM runs with the local
docker (Rancher Desktop or Docker Desktop) and writes `docker save` tarballs
for the dev update path. Production pulls the same images from GHCR by
digest: CI builds them with this same script in `--push` mode (see
[CI publishing](ci-publishing.md)), so local and CI images share one
definition.

```sh
scripts/dev-images.sh                       # all images, host arch
scripts/dev-images.sh --arch amd64          # for an x86_64 engine VM
scripts/dev-images.sh --only care-backend-B # rebuild one (repeatable)
scripts/dev-images.sh --verify              # also round-trip each tarball
```

Requirements: docker with buildx (Docker 29 / containerd image store), git,
jq. No sudo. Output lands in the git-ignored `dist/`:

```text
dist/images/<name>.tar          docker save output (single platform)
dist/images/<name>.tar.digest   sha256:<64 hex>, the image ID after docker load
dist/.cache/src/                shallow CARE checkouts
dist/.cache/stamps/             skip re-saving when the local image is unchanged
```

## Images

| Name | Source |
| --- | --- |
| `care-backend-A` | `care.backend` repo@ref from `versions.yml`, built with its `docker/prod.Dockerfile` (as the desktop app did) |
| `care-backend-B` | **Demo only.** A + one no-op `emr` migration ([`demo/images/backend-B`](../demo/images/backend-B/Dockerfile)), for an update that carries a migration |
| `care-backend-C-broken` | **Demo only.** A whose entrypoint, `start.sh`, `celery_*.sh` and `healthcheck.sh` exit 1 ([`demo/images/backend-C-broken`](../demo/images/backend-C-broken/Dockerfile)), for failed health → rollback |
| `care-frontend-A` | `care.frontend` repo@ref with `deployments/frontend.env` as `.env.local` |
| `care-caddy` | `deployments/caddy.Dockerfile` on `images.caddy` + `CORAZA_VERSION` from `deployments/.env` |
| `care-backup` | `deployments/backup.Dockerfile` on `images.postgres` |
| `postgres`, `redis`, `silo` | `docker pull --platform` of the `images.*` pins |

Built images are tagged `care-clinic-dev/<name>:<arch>` locally; that tag
travels in the tarball but only the digest matters to the engine. Builds use
`--provenance=false --sbom=false` so each is a single manifest.

Built images carry `org.opencontainers.image.source` (this repository, which
GHCR uses to link packages), `org.opencontainers.image.title` (the registry
repo name) and, for CARE images, `network.ohc.care.upstream` and
`network.ohc.care.upstream.revision` (the upstream repo and commit).

## Push mode (CI)

```sh
scripts/dev-images.sh --push ghcr.io/praffq/care_clinic --tag sha-abc --arch arm64 --only care-caddy
```

Builds the images we own (default: `care-backend-A care-frontend-A care-caddy
care-backup`; the demo images only via `--only`) with the same definitions,
pushes `PREFIX/<repo>:<tag>-<arch>` instead of loading/saving, uses a registry
cache at `PREFIX/<repo>:buildcache-<arch>`, and writes
`dist/images/push/<repo>-<arch>.json` with the pushed digest. Repos:
`care-backend`, `care-frontend`, `care-caddy`, `care-backup`,
`care-backend-demo-b`, `care-backend-demo-c-broken`. It needs a
`docker-container` buildx builder and `docker login` to the registry.

## What the digest is

On Docker 29 with the containerd image store, `docker image inspect --format
'{{.Id}}'` returns the manifest (or index) digest, not the config digest. The
`.digest` file is read from the tarball's `index.json` (the manifest annotated
with `io.containerd.image.name`), which is exactly what `docker load` reports:

- **Built images:** identical to the local `.Id` before saving.
- **Pulled multi-arch images:** the local `.Id` is the *index* digest, but
  `docker save --platform` narrows the export to that platform, so after
  `docker load` the ID is the *per-platform manifest* digest. That is also
  the digest `docker pull <repo>@sha256:…` accepts, so dev and prod agree.

The engine must use the containerd image store (the Docker 29 default for new
installs); the classic overlay2 store reports config digests instead.

`--verify` removes each local tag, `docker load`s the tarball and checks the
ID against `.digest`. To check a fresh engine, load into a throwaway dind:

```sh
docker run -d --privileged --name dind -v "$PWD/dist/images:/images:ro" docker:29-dind
docker exec dind docker load -i /images/postgres.tar
docker exec dind docker image inspect --format '{{.Id}}' postgres:17.10-alpine
cat dist/images/postgres.tar.digest   # same value
docker rm -f dind
```
