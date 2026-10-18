# Plugins

[Documentation index](README.md)

CARE Clinic manages CARE **backend** plugins: Django apps that CARE loads from
`ADDITIONAL_PLUGS`. A clinic can enable catalog presets or add any plugin from
PyPI or a git repository. CARE Onboarding, a frontend plugin, is always on;
other frontend plugins are turned on by a CARE administrator in CARE's own
admin dashboard (see [Frontend plugins](#frontend-plugins)).

## How CARE loads a backend plugin

`ADDITIONAL_PLUGS` is a JSON list of CARE's `Plug` fields:

```json
[{"name": "care_multihost_files_be",
  "package_name": "git+https://github.com/ohcnetwork/care_multihost_files_be.git",
  "version": "@main",
  "configs": {"ALLOWED_HOSTS": ["*.local", "*.ts.net"]}}]
```

At startup CARE's `plug_config.py` adds each `name` to `INSTALLED_APPS`, mounts
`api/<name>/`, and exposes `configs` as `settings.PLUGIN_CONFIGS[name]`. The
package must already be installed in the backend image; CARE only pip-installs
plugins at image build time.

## Bundled and custom plugins

- **Bundled**: the CARE backend image CARE Clinic publishes (`care-backend`)
  is built with every catalog plugin already installed (S16,
  `scripts/dev-images.sh` and CI). Enabling or disabling a bundled plugin only
  changes `ADDITIONAL_PLUGS`.
- **Custom**: any other plugin, or a preset pinned to another source, is
  built into a derived image `care-clinic/backend-plugins` inside the engine VM
  by the agent's `image.build` op (`kind: backend-plugins`). The build needs
  internet access in the VM.

The derived image is a two-stage build: `FROM <care-backend-builder>` (CARE's
`prod.Dockerfile` builder stage at the same commit, with the curated plugins)
pip-installs each custom `package_name + version` into `/app/.venv`, then
copies that venv onto the CARE backend image. Its digest is pinned in
`stack.env` like any other image. A label holds a hash of the base, builder
and plugin specs, so an identical set reuses the existing image.

The builder comes from the verified feed (`care[].builders["backend-builder"]`,
an `ImageRef` with `ref` and `digest`) for the installed CARE release. In
development pass `--images dist/images` (or set `CARE_CLINIC_DEV_IMAGES`) to use
`dist/images/care-backend-builder.tar` from `scripts/dev-images.sh --builders`.
Without a builder only bundled plugins can be enabled.

## Catalog

[`app/internal/plugins/catalog.yml`](../app/internal/plugins/catalog.yml) holds
the presets: `id` (Django app name), `package`, `version`, default `configs`,
`bundled`, `default` (enabled on a new clinic), `required_configs`, and a
label and description for the panel.

| Preset | Default | Needs |
| --- | --- | --- |
| `care_multihost_files_be` | yes | `ALLOWED_HOSTS` (defaults to `["*.local","*.ts.net"]`) so file links work from home access too |
| `care_filly` | no | `MEDISPEAK_API_KEY`, `MEDISPEAK_BASE_URL` |

## Where the set lives

| File | Holds |
| --- | --- |
| `plugins.env` in the clinic state dir (rendered by `stack.Render`, 0600) | `ADDITIONAL_PLUGS='<json>'`; the compose `x-backend` env_file lists it last, so it overrides `deployments/backend.env` |
| `installed.json` (update state dir) | `plugins`: the set, the CARE backend base image and the derived image |
| `journal.json` | the target set and image while a change is in flight |

A new clinic (`ctl clinic install`, onboarding) writes the catalog defaults.
Installing again keeps the recorded set (`installed.json`): the install
writes its bundled plugins with the images, and `Coordinator.Reinstalled`
then records the installed images (forgetting the CARE release when the
images are not its own) and re-applies the custom plugins as one journaled
change, building them on the installed backend. If that change fails it
rolls back, CARE keeps running with the bundled plugins, and `ctl clinic
install` (or onboarding) reports the error.
Configs whose key contains `KEY`, `SECRET`, `TOKEN`, `PASSWORD`, `PASSWD` or
`CREDENTIAL` are secrets: they are only stored in the 0600 files above, never
logged or sent to the build, and are shown as `********`. Sending `********`
back keeps the stored value.

## A change is one journaled operation

`updates.Coordinator.SetPlugins` (and `EnablePlugin` / `DisablePlugin`) runs a
`plugins` journal through the same machinery as CARE updates
([updates.md](updates.md)):

| Step | Does |
| --- | --- |
| `preflight` | disk space and a healthy stack |
| `backup` | `backup.run` and export to the host |
| `build` | ensure the builder image, `image.build` when the set has custom plugins (skipped otherwise) |
| `stop-app` | stop backend and celery |
| `write-env` | `stack.env` with the backend digest (CARE's or the derived one) and `plugins.env` |
| `migrate` | `migrations.run` (plugins add tables) |
| `start-stack`, `health` | start and check `https://localhost/ping/` |

A failure rolls back like a CARE update: the previous `stack.env` and
`plugins.env` go back, and the database is restored from the step's backup
if migrations started. The previous image is kept, so rollback needs no
download or build.

A CARE update runs the `build` step too: after a new CARE base the derived
image is rebuilt with the same plugin set from the new release's builder
before the env is written, so custom plugins survive updates.

## Using it

**Panel**: Advanced → unlock with the CARE Clinic admin password → Plugins.
Presets prompt for their required settings; **Add plugin…** takes a name
(Django app), a pip package or `git+https://…` URL, an optional version
(`==1.2.0`, `@main`) and `KEY=value` settings (JSON values allowed). Each
change restarts CARE.

**Command line** (as for `ctl update`, flags before arguments):

```sh
care-clinic-service ctl plugins list
care-clinic-service ctl plugins catalog
care-clinic-service ctl plugins enable care_filly MEDISPEAK_API_KEY=… MEDISPEAK_BASE_URL=https://…
care-clinic-service ctl plugins add --images dist/images care_abdm git+https://github.com/ohcnetwork/care_abdm.git @main
care-clinic-service ctl plugins disable care_filly
```

## Frontend plugins

The `care-frontend` image is built with `REACT_MFE_REGISTERED_COMPONENTS=*`
(`deployments/frontend.env`) and serves the curated remotes
(`care_onboarding_fe`, see `versions.yml`) same-origin at
`/plugins/<slug>/assets/remoteEntry.js`.

The `frontend` section of `app/internal/plugins/catalog.yml` lists them.
`required` ones are registered at every clinic install (`ctl clinic install`
and onboarding): the agent's `manage` command `plug_config` upserts the
`PlugConfig` row `<slug>` with meta
`{"url": "https://<clinic host>/plugins/<slug>/assets/remoteEntry.js",
"config": {...}}` and clears CARE's `care_plug_viewset_list` cache. Keys an
admin changed in the row are kept; only `url` follows the clinic host, and
missing `config` keys are added. CARE Onboarding is required with
`{"redirect_after_login": true}`: after the first superuser login, a clinic
with no facility opens `/admin/onboarding` (this needs `UserDashboard` in
`REACT_MFE_REGISTERED_COMPONENTS`, which `*` covers). The Plugins card lists
it as "Always on".

Other frontend plugins are not managed by CARE Clinic. A CARE administrator enables one in
CARE's admin dashboard (Apps / `PlugConfig`) with slug `<slug>` and meta
`{"url": "https://<clinic>.local/plugins/<slug>/assets/remoteEntry.js"}`; the
URL must be absolute because care_fe validates it as a URL. A frontend plugin
that also has a backend part (such as Filly) needs its backend plugin enabled
here as well.

## Frontend settings (`REACT_*`)

CARE's frontend has its `REACT_*` settings built in when `care-frontend` is
built (`deployments/frontend.Dockerfile` copies `deployments/frontend.env` to
`.env.local`). The feed's CARE release publishes those values as `build_env`
(CI reads `deployments/frontend.env`; a development feed without it falls
back to the kit's or the app's embedded `frontend.env`).

When a clinic's settings differ from the release's `build_env`, CARE Clinic
rebuilds the frontend inside the VM with the agent's `image.build` op
(`kind: frontend-rebuild`), never from git:

1. `FROM <care-frontend-builder>` (the release's `builders["frontend-builder"]`:
   care_fe source at `/app` with `node_modules`): every effective setting
   (release ∪ overrides) is an `ARG`, passed as `--build-arg KEY` with the
   value in the build's environment (exec form, never in a shell or argv),
   then `npm run build`.
2. The curated remotes the published image serves (`/plugins/`, such as
   `care_onboarding_fe`) are copied from the published `care-frontend` image,
   so frontend plugins keep working.
3. The result replaces `/usr/share/nginx/html` on the published image.

The derived image `care-clinic/frontend-rebuild` is pinned in `stack.env` by
its local image ID and labelled with a hash of the base, builder and
settings, so the same settings reuse it. With no overrides the published
image is used.

Any key matching `^REACT_[A-Z0-9_]{1,60}$` can be overridden, except the
keys CARE Clinic manages (`REACT_CARE_API_URL`,
`REACT_MFE_REGISTERED_COMPONENTS`, `REACT_ENABLED_APPS`,
`REACT_DISABLE_PATIENT_LOGIN`, `REACT_RECAPTCHA_SITE_KEY`). Known keys
(`protocol.FrontendBuildKeys`) are checked against their own pattern
(booleans, numbers, locale and visit-type lists, UUIDs, and plain one-line
text). Any other key takes a generic value: at most 200 characters, with no
quotes, `<`, `>`, backslash, `$`, backticks or control characters. Anything
else is rejected before a build.

`Coordinator.SetFrontendSettings` runs a `frontend` journal: `preflight`,
`build-frontend`, `write-env`, `start-stack`, `health`. A failure rolls back
to the previous frontend image. The clinic's overrides are recorded in
`installed.json` (`frontend`) and survive plugin changes and reinstalls; a
CARE update runs `build-frontend` after `build` and rebuilds them on the new
release's base and builder.

**Panel**: Advanced → Clinic settings (and the support editor). Saving a
change to `frontend.env` runs **ApplySettings**, which rebuilds and restarts
CARE (a backend-only change just restarts). The saved file is the clinic's
setting: a panel save replaces the overrides with its contents, including
any set with `ctl frontend` (which does not edit `frontend.env`).

**Command line**:

```sh
care-clinic-service ctl frontend show
care-clinic-service ctl frontend keys
care-clinic-service ctl frontend set --images dist/images REACT_APP_TITLE='Sunrise Clinic' REACT_AUTO_REFRESH_INTERVAL=60
care-clinic-service ctl frontend unset REACT_AUTO_REFRESH_INTERVAL
care-clinic-service ctl frontend reset
```

In development `--images dist/images` uses
`dist/images/care-frontend-builder.tar` (`scripts/dev-images.sh --builders`).

## Build failures

The agent classifies a failed `image.build` from the tail of its output.
Network errors and timeouts (DNS, connection reset or refused, TLS
handshake, registry 5xx, `ETIMEDOUT`, …) stay retryable: the coordinator
retries the step 3 times, as it does for output it doesn't recognise.
Deterministic errors, such as pip's `No matching distribution found`, a git
repository or ref that doesn't exist, a package that fails to build, or an
npm/vite build error, are permanent (`build_failed`, `image build failed:
<line>`); a network marker anywhere in the output wins. The change rolls back
at once and the panel shows the reason ("The build
failed and won't succeed by retrying: …"). Invalid plugin specs and
settings are rejected before anything runs.

## Known gaps

- Building other frontend remotes in the VM is not supported; only the
  curated remotes baked into `care-frontend` are served.
- `image.build` is bounded by the agent's 30 minute op limit.
