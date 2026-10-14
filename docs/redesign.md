# CARE Clinic redesign

This is the agreed target architecture and source of truth. There is **no
backward-compatibility requirement**. The proposal is background only. Future
phases below are requirements, not claims that the first PR implements them.

## Implementation phases (one stacked PR each)

1. Versions, signed manifest and CI (points 1–14).
2. Update coordinator, journal, pre-flight checks and logs bundle (15–24, 43).
3. Env defaults layer, frontend option A and generated secrets (30–42).
4. Backups: keep latest, verification, WAL and optional extra copy (44–54).
5. Agent API replacing host Docker, compose, relay and sync (25–29).

## Versions and releases

1. Rename release selections to CARE versions. Only CARE 3.1 ships, with no
   version picker. Remove the 3-to-3.1 upgrade flow and all selection files,
   packages and configuration.
2. Root `versions.toml` is the **only human-edited release file**. It has
   `schema=1`; `[desktop] version, requires_engine`; `[engine] version,
   requires_desktop`; `[care] version, requires_desktop, requires_engine`;
   `[care.backend] repo, branch`; `[care.frontend] repo, branch`;
   `[support_images]` pinned postgres, redis, caddy and silo tags; and `[tools]`
   platform tool URL and SHA-256 pins.
3. Parse TOML with `pelletier/go-toml/v2` and `DisallowUnknownFields`; validate
   semantic versions and requirements with `Masterminds/semver/v3`. Reject
   unknown keys, invalid versions and invalid constraints.
4. Track branches, not commits. CI resolves each branch head at run start and
   compares it with the commit in the last published manifest.
5. There is no candidate/beta/stable process; CI verifies builds and packaging
   only. There is no `agent_api` version: the agent ships in the engine bundle
   and uses the engine version.
6. Build only changed components. Publish images to GHCR and binaries as
   immutable GitHub Release assets; never overwrite an asset.
7. CI generates `updates.json` and detached Ed25519 `updates.json.sig`, published
   together at `https://<owner>.github.io/<repo>/updates.json`. Include schema,
   monotonically increasing revision, UTC `published_at`; desktop and engine
   version/requirements/platform assets (URL, SHA-256, size); CARE
   version/requirements/backend and frontend commit, image and builder digests;
   frontend `build_env`; support image digests; and `blocked[]`. Embed current
   and next public keys in desktop code. Keep the private key in a GitHub
   Actions secret in an environment requiring reviewer approval.
8. Requirements are copied from `versions.toml`; CI never decides requirements.
9. Serialize publishing with a concurrency group and no cancellation. Publish
   nothing half-done. Clinics reject any revision below the highest seen.
10. Exactly four release workflows: `care-images.yml` (backend/frontend runtime and
    builder), `engine.yml`, `desktop.yml`, `publish.yml` (manifest/sign/deploy).
    A shared script or Go tool generates the manifest from TOML and build
    outputs. A separate manual `desktop-dev.yml` creates unsigned Windows dev
    prereleases (`desktop-dev-v*` tags) and never touches the manifest.
11. Check CARE branches daily by cron, on `workflow_dispatch`, and on pushes
    changing `versions.toml` or `env-defaults.yml`. Only a manual run from
    `main` with `publish` enabled publishes; other runs validate and build.
12. Changing the `fe` section in `env-defaults.yml` rebuilds the published frontend
    image, because these settings are compiled in.
13. Seed current pins from `deployments/.env`, moving versions and pins into
    `versions.toml`.
14. Remove obsolete release-selection machinery and terminology throughout
    docs, UI, configuration and tests.

## Updates on the clinic

15. There are three update types: CARE images, engine and desktop.
16. One Go coordinator (for example `app/internal/updates`) is the **only** code
    allowed to change installed versions. UI, `Start()` and background checker
    only ask it.
17. Atomically write `updates/journal.json` (temp file then rename) before and
    after every step. Fields: `plan_id`, `created_at`, `updated_at`,
    `manifest_revision`, `state`
    (`pending_approval|running|rolling_back|done|rolled_back|failed`),
    `approved_by/at`, `from` and `to` (desktop, engine, CARE backend/frontend
    digests, local image IDs, settings hash, file paths and SHA-256), `rollback`
    (`backup_id`, `db_migrated`, `engine_previous`, `images_kept`, previous app
    path), and `steps[]` (name, status
    `pending|running|done|failed|skipped|undone`, started/finished timestamps,
    attempts, error, undo). Read the journal first on startup and resume or roll
    back. Stop after three attempts on a step and roll back. Store hashes,
    never secrets or settings values.
18. Summarize finished plans into `updates/history.json`, then clear the journal.
19. **No automatic updates.** The background checker downloads and verifies in
    advance and notifies staff. Apply nothing until staff click **Update**.
20. Pre-flight every update: free disk including backup space, mains power where
    detectable, sane clock, and healthy containers/engine.
21. Combine desktop and engine requirements into a dependency-ordered plan. Use
    a bridge release when no compatible step exists.
22. Keep the previous version until the new one is healthy. Back up before
    backend migrations; restoring that backup is data rollback. Remove
    `PruneDangling` after retag and deletion of the previous application slot.
23. `Start()` never applies staged updates.
24. Keep the last verified manifest and highest revision, installed versions and
    digests, previous artifacts and journal.

## Engine and VM

25. Keep direct download and unpacking of QEMU on Windows (already implemented).
26. Keep transactional engine updates in `app/internal/vm/update.go`; the
    coordinator drives them.
27. The agent API replaces host Docker CLI, compose, Docker relay and Windows
    folder sync. Desktop sends operations over the existing mTLS connection;
    agent runs compose/docker inside the VM. Operations include compose
    up/down/ps/logs/pull, image build (plugin backend/frontend rebuild), image
    inspect/tag/remove, exec (manage.py and PostgreSQL tools), existing file
    put/get, health and streaming job logs. Remove host docker/compose downloads,
    `DOCKER_HOST` wiring, `vm/docker_relay*`, `vm/sync.go`, `pathmap.go` and
    `engine_hook.go` DockerHook.
28. Linux runs the same agent natively.
29. The helper stays only for VM boot and forwarding ports 80/443.

## Environment and configuration

30. Keep the env editor and `backend.env`/`frontend.env`.
31. Priority: **Advanced Settings overrides >= our `env-defaults.yml` > CARE's
    own defaults**.
32. `deployments/env-defaults.yml` contains only deliberately chosen string
    values in `be` and `fe` maps, seeded from current env files, excluding
    secrets and hostname-derived values. Examples include
    `DJANGO_SETTINGS_MODULE: "config.settings.production"`,
    `REACT_DISABLE_PATIENT_LOGIN: "true"` and
    `REACT_MFE_REGISTERED_COMPONENTS: "UserDashboard"`.
33. Move versions and pins from `.env` to `versions.toml`; `.env` keeps only
    compose wiring, or is generated.
34. Clinic name, hostname, languages and operational settings (backup
    schedule/destination, WAF mode) belong in setup UI and Settings, not YAML.
35. Store per-clinic `backend.overrides.env` and `frontend.overrides.env`. Saving
    a value retains an override even when equal to the default; **Reset to
    default** removes it. Explicit empty differs from absent. Generate effective
    env files from defaults, overrides, derived values and secrets.
36. Generate Django key, PostgreSQL password and storage access/secret keys at
    install into separate `secrets.env` (0600), never shown in the editor.
    Compute `CSRF_TRUSTED_ORIGINS`, `BUCKET_EXTERNAL_ENDPOINT` and
    `REACT_CARE_API_URL` from hostname, and `DATABASE_URL` from secrets.
37. Clinic settings changes never start CI or a release.

## Images and builds

38. Publish backend runtime, backend builder, frontend runtime and frontend
    builder for each CARE version.
39. Without plugins, relabel the published backend. With plugins,
    `backend-plugins.Dockerfile` installs them in the builder and copies
    `/app/.venv` onto the runtime. Use `backend.env` only at startup.
40. Frontend **option A** compiles every setting. If any effective value differs
    from release `build_env`, rebuild locally with the builder and
    `frontend-rebuild.Dockerfile`. Delete `images/frontend/runtime-keys.json`,
    `images/frontend/configure/`, `40-care-clinic-config.sh`,
    `images/scripts/verify-frontend.sh`, related tests and `runtime_keys`.
41. The sole runtime exception is `REACT_CARE_API_URL`: one hard-coded
    placeholder replaced at container startup by a tiny sed entrypoint, not a
    configurable list.
42. Reapply plugins and frontend overrides automatically on CARE updates. An
    explicitly empty compiled override still differs; fix `BuildSettings`
    skipping `v == ""`.

## Support

43. **Download logs** produces a ZIP containing application and engine/guest
    logs, journal/history, versions, health and env files **including secrets**.
    First warn: “This file contains clinic passwords. Share it only with the
    CARE support team.”

## Backups

44. Keep only the latest backup set; remove retention-period settings.
45. Order: **new backup -> verify -> only then delete previous**. On any failure,
    retain the previous set and alert.
46. Configure `wal_level=replica`, `archive_mode=on`, `archive_timeout=300`.
    Our archive script compresses, encrypts with `openssl cms -encrypt` using
    the same clinic certificate, writes a temp file then renames into
    `backups/wal/`. Restore decrypts with the recovery key. Make a nightly
    physical full `pg_basebackup` in `backups/base-<ts>/`, encrypted identically.
47. Delete WAL only after a new full copy is verified, and only segments before
    that copy's start LSN. On backup failure retain all WAL.
48. Also keep daily `pg_dump`. Physical restores require the same PostgreSQL
    major and CPU architecture; dumps support Mac ARM/Windows x86 migration.
49. Main backup lives on the computer's own disk. USB, second disk or share is
    an optional extra-copy destination.
50. Disconnecting USB never stops the clinic; PostgreSQL archives only locally.
    Copier reports “not connected, last copied …” and catches up on reconnect.
    Copy to temp files then rename; delete old copied files only after the new
    copies are verified at the destination.
51. Restore into a staging database and swap only after replay and health checks
    succeed. Extend existing restore with point-in-time `recovery_target_time`.

## Backup verification

52. After every backup: SHA-256 checksums in `backups/manifest.json`, decrypt
    test, `pg_restore --list`, `pg_verifybackup`, `pg_waldump` over WAL and a
    gap-free WAL-chain check. Only these checks authorize old-backup deletion.
53. Weekly trial restore into a throwaway database with row-count sanity check,
    only when free space exceeds database size plus margin; otherwise skip
    with notice.
54. Backups page shows last backup, verified status, “changes saved up to
    <time>”, and alerts for failures, WAL gaps, stale WAL and missing USB.
