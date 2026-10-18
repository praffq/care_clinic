# CARE Clinic

Self-contained, offline [CARE](https://github.com/ohcnetwork/care) for a small clinic.
One installer on one computer runs the whole EMR — backend, web app, database and file
storage — and staff connect from other computers using CARE Clinic in client mode on
the clinic Wi-Fi. The core clinic can run without internet after installation.
App updates and configured online services still need connectivity.

**Download:** [releases](https://github.com/ohcnetwork/care_desktop/releases)
— `.dmg` for macOS, `-setup.exe` for Windows 64-bit. Setting up a clinic server
needs a Mac with Apple silicon; Windows can connect as a client but cannot host
a clinic yet.

**Previously hosted CARE on this computer, but now cannot open another clinic?**
See [client recovery and earlier-install cleanup](docs/client-recovery.md).

## What it does

- **First-run choice** — **Start setup** to host a clinic, or **Connect to an
  existing server on the local network**. The start screen and its buttons save
  nothing: the server role is recorded when the setup screen begins, before any
  setup settings are saved. The client role is recorded only when a connection
  is attempted. Once written, ordinary use does not switch roles.
- **Server setup wizard** — computer checks (virtualization, memory, disk and
  the CARE Clinic service), clinic address and admin password, then install. If
  the background service is missing, CARE installs it with one macOS
  administrator prompt. Installation shows real activity rather than an
  invented percentage and opens the control panel directly when it succeeds.
  See [server onboarding](docs/onboarding.md).
- **Runs without the app** — a boot-time service, `care-clinic-service`, runs the
  clinic in a small Linux virtual machine, forwards its ports, announces
  `<clinic>.local` and starts the clinic after a reboot. The app is only the
  user interface; closing it leaves the clinic running.
- **Interrupted installation** — **Try again** retries while the original
  desktop process remains open. If that attempt is no longer available, CARE
  removes the unfinished clinic and setup starts over.
- **Server control panel** — clinic status from the service, start/stop/restart,
  clinic address and phone/tablet setup QR, with Home access, Updates and
  Advanced pages.
- **Native client setup** — enter the clinic's `.local` address shown on its
  server. **Find clinic** checks it and changes nothing on the computer;
  **Connect** is the step that does. Nothing runs or advertises on the client.
  CARE downloads and validates the public certificate, verifies the
  server's TLS hostname before installation, requests OS administrator approval
  when needed, and automatically checks HTTPS before opening CARE. Later
  connections use the saved certificate pin rather than silently downloading a
  replacement.
- **Connecting cleans up after the last time** — it removes CARE's leftover
  address overrides and every other `CARE Clinic Local CA` certificate this
  computer trusts, then installs the current clinic's. No extra confirmation:
  your computer asks for permission once (on a Mac, sometimes twice — once for
  the address, once for the certificate). A computer that used to host a clinic
  can therefore become an ordinary client without a separate cleanup, and the
  certificate being installed is never removed by its own cleanup, so
  reconnecting to the same clinic changes nothing.
- **Connected screen** — once connected, CARE Clinic keeps a light check on
  whether the clinic is answering, so staff can see at a glance that it is up
  before they click through. It only looks; it changes nothing.
- **Back** — walks through earlier setup steps; from the first step it can undo
  an unused setup. Client Back remains available until connected. Running work
  blocks navigation; the backend rejects clearing a setup or connection already
  in use.
- **Disconnect** — disconnect a client and remove only the certificate
  it installed, without touching server data. Successful uninstall clears its role
  and returns to the Server/Client choice, as does successful server uninstall.
  Remove access before connecting to another clinic or uninstalling the desktop
  app through the operating system. Trusted certificates that did not come from
  CARE remain and may still permit browser access. Uninstall the setup in CARE
  Clinic before removing the executable through the operating system.
- **Advanced** — clinic settings, CARE Clinic password change, log and
  uninstall. Less-used local options keep their defaults or saved values and
  can be overridden in **Extra settings (for support)**. Advanced locks 15
  minutes after unlocking, or sooner when you leave the tab; protected native
  actions still check the password. Email, SMS/patient sign-in and MFA
  configuration are not exposed in this editor.
- **Removed in v2** — backups and restore, plugins, storage monitoring, CARE
  software updates, recovery codes, web password reset and start at login.
  They return later via engine agent ops.
- **Short permission prompts** — CARE's in-window confirmations explain the
  next action without a technical checklist. System password and certificate
  approvals still appear in the operating system's own dialogs.

## How it works

Client setup uses trust on first use over the local network: the initial
certificate download is HTTP, not authenticated HTTPS. Use a trusted clinic
network and the address supplied by its administrator. Automatic certificate and
TLS checks do not prove that an attacker did not impersonate the server during
that first download. There is no manual fingerprint-comparison step.
Phones and tablets use `http://<clinic>.local/setup` for iOS/Android certificate
steps, opened from **Connect phone or tablet** in the server panel. Windows/Mac
clients keep using the desktop app; downloadable scripts remain retired. Separate clinic
servers with unique names remain valid; CARE does not enforce a signed,
network-wide single-clinic rule.

A Go / [Wails](https://wails.io) desktop app with a React UI talks to the root
`care-clinic-service` over a local control socket. The service runs an engine VM
(Apple Virtualization.framework) whose agent runs the Docker Compose stack: the
CARE backend and workers, the CARE frontend, PostgreSQL, Redis, Silo for files,
and Caddy with the Coraza WAF as the HTTPS front door. Nothing else needs to be
installed on the clinic computer. See [architecture](docs/architecture.md).

| Path | What lives there |
|---|---|
| `app/` | The desktop app (Wails bindings in `*.go`, React UI in `frontend/`), the service and engine agent in `cmd/`, shared Go packages in `internal/` |
| `engine/` | The engine VM image build |
| `deployments/` | The server kit: compose file, Caddyfile, env files and public certificate bootstrap route |
| `scripts/` | macOS bundle hook, developer service install, dev image builds |
| `.github/workflows/` | CI and releases |

**Using the app:** [Desktop workflows](docs/desktop-workflows.md) covers setup,
connection, installation, each control-panel tab and update handoffs.

**Technical documentation:** [Start with `docs/README.md`](docs/README.md) for
the architecture, file map, Wails API, configuration, lifecycle, native
integrations, safe UI tests and release workflow.

**Releases:** [Preparing and publishing a version](docs/releases.md), including
automatic tags, the macOS app bundle layout, and macOS and Windows signing configuration.

## Code signing policy

Free code signing provided by [SignPath.io](https://signpath.io), certificate by
[SignPath Foundation](https://signpath.org).

Windows releases are built by [GitHub Actions](.github/workflows/release.yml)
from a commit of this repository and signed by SignPath only after it has
verified that the file came from that build.

| Role | Members |
|---|---|
| Authors — commit to this repository | [@praffq](https://github.com/praffq) |
| Reviewers — review pull requests | [@praffq](https://github.com/praffq) |
| Approvers — approve signing of a release | [@praffq](https://github.com/praffq) |

### Privacy

CARE Clinic has no telemetry, analytics or crash reporting. Network requests
are made for setup, configured features and updates, including automatic update
checks:

- **Server setup** installs the service and engine shipped inside the app and
  loads the clinic images supplied to it; it downloads nothing itself.
- **The Windows installer** contains Microsoft's WebView2 bootstrapper, which
  downloads the WebView2 runtime from Microsoft if the computer lacks it.
- **Clinic features** the operator turns on, such as SMS sign-in codes and email,
  send data to the provider the operator configured.
- **Updates** check GitHub release metadata for CARE Clinic.

On the clinic network the server's service announces `<clinic>.local` with mDNS so
staff computers can find it; clients do not advertise a clinic. Clients retrieve
the public certificate over local HTTP and check the clinic over HTTPS. That
traffic stays on the local network. The container images and WebView2 are
covered by their own privacy policies.

Build with `cd app && node frontend/scripts/stage-install.mjs && wails build`
(needs Go, Node 22 and the Wails CLI). On macOS the build also puts the service,
and an engine bundle if one is available, inside the `.app`; see
[bundle layout](docs/releases.md#macos-app-bundle-layout).

Run `cd app && wails dev` to review the real desktop application. This uses the
native backend and can operate an existing clinic on this computer; use an
isolated environment for destructive installation and removal tests. See
[development](docs/development-and-release.md#a-running-development-app-has-real-effects)
for the environment variables that stand in for the bundled files.

Run `cd app/frontend && npm run test:ui` for the Playwright scenarios
(requires Playwright's Chromium browser; install it with `npx playwright install chromium`
if it is missing). They cover navigation, native-contract gates,
installation and update events, panel actions, settings, errors,
keyboard access, and layout at 1100×700 and 720×560 using a simulated host under
`app/frontend/tests/fixtures/`. The runner starts its own loopback server on port
41783. These fixtures require Vite test mode, perform no native operations, and
are excluded from the production entry point.

MIT licensed — see [LICENSE](LICENSE). Part of the [Open Healthcare Network](https://ohc.network).
