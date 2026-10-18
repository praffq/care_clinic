# Plugins

[Documentation index](README.md)

CARE Clinic manages CARE **backend** plugins: Django apps that CARE loads from
`ADDITIONAL_PLUGS`. A clinic can enable catalog presets or add any plugin from
PyPI or a git repository. Frontend plugins are turned on by a CARE
administrator in CARE's own admin dashboard (see [Frontend plugins](#frontend-plugins)).

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

CARE Clinic does not manage frontend plugins. The `care-frontend` image is
built with `REACT_MFE_REGISTERED_COMPONENTS` and serves the curated remotes
(`care_onboarding_fe`, see `versions.yml`) same-origin at
`/plugins/<slug>/assets/remoteEntry.js`. A CARE administrator enables one in
CARE's admin dashboard (Apps / `PlugConfig`) with slug `<slug>` and meta
`{"url": "https://<clinic>.local/plugins/<slug>/assets/remoteEntry.js"}`; the
URL must be absolute because care_fe validates it as a URL. A frontend plugin
that also has a backend part (such as Filly) needs its backend plugin enabled
here as well.

## Known gaps

- Building other frontend remotes in the VM (`image.build` kind
  `frontend-rebuild`) is not implemented.
- `image.build` is bounded by the agent's 30 minute op limit.
