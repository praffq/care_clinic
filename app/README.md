# `app/` — the CARE Clinic control app (Go / Wails)

The Go/Wails application is the clinic's user interface. On a server it
installs and talks to the root [`care-clinic-service`](cmd/care-clinic-service/README.md),
which runs the clinic in the engine VM; it also handles local networking and
certificate trust. Packages under `internal/` never import Wails.

| Directory | What it is |
| --- | --- |
| `*.go`, `frontend/` | The desktop app. |
| `cmd/care-clinic-service` | The root host service and the `ctl` developer CLI. |
| `cmd/care-engine-agent` | The agent inside the engine VM. |
| `cmd/release`, `cmd/vzboot` | Release feed tooling and a developer VM boot tool. |

On a new computer, CARE Clinic first asks whether to set up a clinic server or
connect as a client. A **Back** button on the next screen undoes a misclick as
long as nothing has been installed or connected; after that the selected role
stays locked until successful uninstall returns to the role choice.
Failed-install cleanup retains it. Existing and partial server installations retain
their server role. Clients enter a clinic address, approve the operating system's
certificate trust prompt, and open CARE in their browser; nothing runs on a
client in the background. They can retry the saved address or remove clinic access before
connecting to a different address. No fingerprint confirmation is required.

```bash
wails dev
```

Under `wails dev` set `CARE_CLINIC_SERVICE_BINARY`, `CARE_CLINIC_ENGINE_BUNDLE`
and `CARE_CLINIC_DEV_IMAGES` as needed; see
[development](../docs/development-and-release.md#a-running-development-app-has-real-effects).

For a production build, synchronize installer metadata before Wails starts:

```bash
node frontend/scripts/stage-install.mjs
wails build
```

See [Development and release](../docs/development-and-release.md) for preparing
a fresh checkout, embedded assets, build prerequisites, and platform packaging.
On macOS, `wails build` also bundles the service (and an engine, if available)
into the `.app`; see [bundle layout](../docs/releases.md#macos-app-bundle-layout).
Running a development app can operate an existing clinic on this computer.
For publishing installers, follow [Releasing CARE Clinic](../docs/releases.md).

## Backend documentation

Start at the [documentation index](../docs/README.md), then read the
[architecture](../docs/architecture.md), [file map](../docs/repository-map.md),
[Wails API](../docs/wails-application.md), and
[configuration guide](../docs/configuration-and-settings.md).
