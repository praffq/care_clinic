# Repository and file map

[Documentation index](README.md)

This map covers the application boundary, major internal packages, desktop
workflows and their build/test contracts. The package trees highlight principal
files rather than exhaustively listing generated output. Detailed subsystem
inventories remain in the linked guides.

## Repository-level structure

```text
care_desktop/
|-- README.md
|-- docs/
|-- scripts/
|   |-- bundle-macos.sh
|   |-- service-macos.sh
|   `-- dev-images.sh
|-- engine/                  # engine VM image build (see engine/README.md)
|-- app/
|   |-- main.go and app_*.go
|   |-- password.go
|   |-- go.mod
|   |-- go.sum
|   |-- wails.json
|   |-- .golangci.yml
|   |-- build/
|   |-- cmd/
|   |   |-- care-clinic-service/   # root host service and `ctl`
|   |   |-- care-engine-agent/     # agent inside the engine VM
|   |   |-- release/               # feed/versions tooling
|   |   `-- vzboot/                # developer VM boot tool
|   |-- frontend/
|   |-- install/
|   `-- internal/
|-- deployments/
|   |-- .env
|   |-- backend.env
|   |-- frontend.env
|   |-- docker-compose.yml
|   |-- Caddyfile
|   |-- caddy.Dockerfile
|   |-- backup.Dockerfile      # not uploaded in v2
|   |-- scripts/backup.sh      # not uploaded in v2
|   |-- minio/entrypoint.sh
|   `-- setup/index.html
`-- .github/
    `-- workflows/
        |-- ci.yml
        `-- release.yml
```

`deployments/` is versioned kit source. `app/install/` is generated build
staging. The clinic computer's install folder is a third location selected by
the application, and the engine's config area inside the VM is a fourth. See
[clinic lifecycle](clinic-lifecycle.md#2-three-copies-of-the-deployment-kit).

## Application boundary: `app/*.go`

All of these files belong to Go `package main`, even though they are organized by feature.

| Source | Responsibility | Main guide |
| --- | --- | --- |
| [`main.go`](../app/main.go) | Embedded filesystems, log initialization, `wails.Run`, callbacks/binding, fatal startup handling. | [Wails application](wails-application.md). |
| [`app.go`](../app/app.go) | `App` state, construction, log/event delivery and error reporting. | [Architecture](architecture.md), [Wails application](wails-application.md). |
| [`app_lifecycle.go`](../app/app_lifecycle.go) | Startup (kit refresh, service watch), shutdown, second launch, close confirmation. | [Wails application](wails-application.md). |
| [`app_confirmation.go`](../app/app_confirmation.go) | ID-scoped permission requests, independent response channel, native fallback and shutdown cancellation. | [Confirmation protocol](wails-application.md#in-window-permission-confirmations). |
| [`app_quit.go`](../app/app_quit.go) | Registered running-job quit requests and guarded responses. | [Closing the application](wails-application.md#closing-the-application). |
| [`app_config.go`](../app/app_config.go) | `Config`, OS configuration path, strict loading, cached/atomic saves, role selection and clearing. | [Configuration](configuration-and-settings.md). |
| [`app_client.go`](../app/app_client.go) | Native client connection, pinned trust/ownership journal, TLS verification, and exact-certificate removal with role reset after success. | [Native integrations](native-integrations.md). |
| [`app_installdir.go`](../app/app_installdir.go) | Install-folder path, unpacking/preservation rules, creation of a configured `Clinic`, dev images. | [Configuration](configuration-and-settings.md). |
| [`app_service.go`](../app/app_service.go) | Service client, bundle layout, status summary and the `service-status` watcher, engine install and readiness. | [Architecture](architecture.md). |
| [`app_onboarding.go`](../app/app_onboarding.go) | `HostChecks`, `InstallService`, `RunSetup`, `RetrySetup`, `CleanupFailedInstall`, name checks. | [Server onboarding](onboarding.md). |
| [`app_actions.go`](../app/app_actions.go) | Read/write job gates, async runner, lifecycle/admin guards, `ClinicAction`, `ClinicStatus`. | [Wails application](wails-application.md), [clinic lifecycle](clinic-lifecycle.md). |
| [`app_status.go`](../app/app_status.go) | State, network, name/password validation, `MDNSStatus`, admin password check and change. | [Wails application](wails-application.md). |
| [`app_home_access.go`](../app/app_home_access.go) | Home access bindings: Tailscale status, enable and disable. | [Home access](home-access.md). |
| [`app_env.go`](../app/app_env.go) | Authorized reads and atomic writes of the two installed environment files. | [Configuration](configuration-and-settings.md). |
| [`app_update.go`](../app/app_update.go), [`app_update_checks.go`](../app/app_update_checks.go), [`app_update_helpers.go`](../app/app_update_helpers.go), [`app_selfupdate.go`](../app/app_selfupdate.go) | GitHub-release desktop updater, the update-check switch, platform helpers, macOS bundle replacement. | [Wails application](wails-application.md), [releases](releases.md). |
| [`app_ui.go`](../app/app_ui.go) | Native URL/folder/log actions and native questions. | [Wails application](wails-application.md). |
| [`app_uninstall.go`](../app/app_uninstall.go) | Authorized uninstall, removal checkpoint, local-state reset and event. | [Cleanup](cleanup-and-uninstall.md). |
| [`app_osremove.go`](../app/app_osremove.go) | `--uninstall`/`--uninstall-check` modes for the Windows uninstaller, the shared "still set up" check, and in-app removal of the desktop app. | [Cleanup](cleanup-and-uninstall.md#3-removing-the-desktop-app). |
| [`password.go`](../app/password.go) | Shared setup password policy. | [Wails application](wails-application.md). |

### Application-boundary regression groups

| Tests | Contract |
| --- | --- |
| [`app_confirmation_test.go`](../app/app_confirmation_test.go), [`app_quit_contract_test.go`](../app/app_quit_contract_test.go), [`app_quit_test.go`](../app/app_quit_test.go), [`app_ui_test.go`](../app/app_ui_test.go) | Consent/quit snapshots, stale responses, cancellation, independent locking and native question handling. |
| [`app_onboarding_test.go`](../app/app_onboarding_test.go), [`app_service_test.go`](../app/app_service_test.go), [`app_fake_service_test.go`](../app/app_fake_service_test.go), [`app_role_test.go`](../app/app_role_test.go) | Role persistence and clearing, computer checks, setup against a fake service, status summaries. |
| [`app_client_test.go`](../app/app_client_test.go), [`app_home_access_test.go`](../app/app_home_access_test.go) | Native client contracts and home access. |
| [`app_update_test.go`](../app/app_update_test.go), [`app_update_access_test.go`](../app/app_update_access_test.go), [`app_update_helpers_test.go`](../app/app_update_helpers_test.go) | Desktop release/update behavior and access outside an installed server. |
| [`app_env_test.go`](../app/app_env_test.go) | Settings access and operation gates. |
| [`app_installdir_test.go`](../app/app_installdir_test.go), [`app_osremove_test.go`](../app/app_osremove_test.go), [`app_log_test.go`](../app/app_log_test.go), [`app_branding_test.go`](../app/app_branding_test.go) | Kit embedding, executable-removal checks, native error logging and branding. |

The complete method signatures and event/result shapes are in the [Wails API reference](wails-application.md#complete-bound-method-reference). Do not infer an exported API just from a filename: Wails binds exported `App` methods, not every function in this package.

## Internal package structure

```text
app/internal/
|-- bundle/          # files shipped inside the .app
|-- clinic/          # clinic Setup/Start/Up/Stop/Uninstall, this computer
|-- engine/
|   |-- agent/       # agent ops inside the VM
|   |-- client/      # agent client
|   |-- forward/     # host-port to vsock forwarding
|   |-- protocol/    # op registry and wire types
|   `-- vm/          # Virtualization.framework VM
|-- feed/            # signed updates.json
|-- hostcheck/       # onboarding computer checks
|-- release/         # release pins
|-- service/
|   |-- advertise/   # mDNS watcher
|   |-- api/         # control-socket types
|   |-- control/     # control-socket client and server
|   |-- hostfwd/     # host listeners for 80/443/8480
|   |-- install/     # one-prompt LaunchDaemon install
|   |-- logfile/
|   |-- settings/
|   |-- slots/       # engine bundle slots, install and activate
|   `-- supervisor/  # engine VM watchdog
|-- stack/           # Runtime: render, upload and run agent ops
|-- versions/        # versions.yml
`-- sys/
    |-- applog/  appremoval/  atomicfile/  diskspace/  elevate/
    `-- hosts/  mdns/  netfix/  proc/  tailscale/  trust/
```

### Where each package is explained

| Package | Responsibility | Detailed explanation |
| --- | --- | --- |
| [`internal/clinic`](../app/internal/clinic) | Clinic operations on top of the stack runtime; no Wails dependency. | [Clinic lifecycle](clinic-lifecycle.md), [cleanup](cleanup-and-uninstall.md). |
| [`internal/stack`](../app/internal/stack) | Renders the kit and generated env files, uploads them, runs agent ops. | [Clinic lifecycle](clinic-lifecycle.md). |
| [`internal/hostcheck`](../app/internal/hostcheck) | Virtualization, memory, disk and service checks. | [Server onboarding](onboarding.md#1-computer-checks). |
| [`internal/bundle`](../app/internal/bundle) | Locates the bundled service binary and engine tarball, with env overrides. | [Bundle layout](releases.md#macos-app-bundle-layout). |
| [`internal/service/*`](../app/internal/service) | The host service's packages and the app's control client. | [care-clinic-service](../app/cmd/care-clinic-service/README.md). |
| [`internal/engine/*`](../app/internal/engine) | Engine VM, agent, protocol and forwarding. | [engine](../engine/README.md). |
| [`internal/feed`](../app/internal/feed), [`internal/versions`](../app/internal/versions) | Signed update feed and release pins file for future engine/CARE updates. | [Update feed](updates-feed.md), [versions](versions.md). |
| [`internal/release`](../app/internal/release) | Validated release manifest and source/image identity. | [Development and release](development-and-release.md#release-identity-and-pins). |
| [`sys/proc`](../app/internal/sys/proc) | Child-process creation, launcher execution, Windows Desktop lookup and PATH repair. | [Native integrations](native-integrations.md). |
| [`sys/atomicfile`](../app/internal/sys/atomicfile) | Durable single-file replacement across OSes. | [Native integrations](native-integrations.md). |
| [`sys/appremoval`](../app/internal/sys/appremoval) | Locating and removing the installed desktop app (macOS Trash, Windows uninstaller), other-instance and other-account checks. | [Cleanup](cleanup-and-uninstall.md#3-removing-the-desktop-app). |
| [`sys/applog`](../app/internal/sys/applog) | Diagnostic sink, native log location, bounded rotation. | [Native integrations](native-integrations.md). |
| [`sys/diskspace`](../app/internal/sys/diskspace) | Platform free-space measurement (used by the disk check). | [Server onboarding](onboarding.md#1-computer-checks). |
| [`sys/elevate`](../app/internal/sys/elevate) | Interpreter quoting and batching privileged native steps. | [Native integrations](native-integrations.md). |
| [`sys/hosts`](../app/internal/sys/hosts) | Owned loopback hostname entries and verified removal. | [Native integrations](native-integrations.md). |
| [`sys/trust`](../app/internal/sys/trust) | Root-CA trust, removal, and native client certificate bootstrap. | [Native integrations](native-integrations.md). |
| [`sys/mdns`](../app/internal/sys/mdns) | LAN address selection, name advertisement (used by the service), name checks and response probing. | [Native integrations](native-integrations.md#lan-names-and-mdns). |
| [`sys/tailscale`](../app/internal/sys/tailscale) | Tailscale CLI discovery, status, and CARE Clinic's own `tailscale serve` handler. | [Home access](home-access.md). |
| [`sys/netfix`](../app/internal/sys/netfix) | Windows network profiles and application-owned firewall rules. | [Native integrations](native-integrations.md). |

### Smaller domain source files

| Source | Role |
| --- | --- |
| [`clinic/home_access.go`](../app/internal/clinic/home_access.go) | Home-access port and Tailscale serve target. |
| [`release/pins.go`](../app/internal/release/pins.go) | `Pins`, required manifest fields, version/source-ref validation, diagnostic summary. |
| [`release/pins_test.go`](../app/internal/release/pins_test.go) | Version agreement, release-versus-development refs, commit-ID validation. |
| [`release/workflow_test.go`](../app/internal/release/workflow_test.go) | Executes the workflow's Node identity validator with synthetic inputs; requires Node. |

## Desktop files that form the backend contract

```text
app/frontend/
|-- package.json
|-- tsconfig.json
|-- vite.config.ts
|-- playwright.config.ts
|-- scripts/
|   |-- stage-install.mjs
|   |-- check-bindings.mjs
|   `-- prune-fonts.mjs
|-- tests/
|   |-- fixtures/
|   `-- *.spec.ts
`-- src/
    |-- App.tsx
    |-- wails.d.ts
    |-- types.ts
    |-- lib/
    |-- state/
    |-- components/
    |-- hooks/
    `-- screens/
        |-- role-screen.tsx
        |-- client-screen.tsx
        |-- remove-screen.tsx
        |-- setup/
        |-- install/
        `-- panel/
```

| Contract file | Why a backend maintainer needs it |
| --- | --- |
| [`wails.d.ts`](../app/frontend/src/wails.d.ts) | Hand-maintained method names, argument lists, and promise result declarations. |
| [`types.ts`](../app/frontend/src/types.ts) | JSON-facing return shapes used by the desktop. |
| [`bridge.ts`](../app/frontend/src/lib/bridge.ts) | Lazy runtime lookup, call dispatch, event subscriptions, host logging. |
| [`care-store.tsx`](../app/frontend/src/state/care-store.tsx) | Async-job completion handling, boot, `service-status` state. |
| [`run-steps.ts`](../app/frontend/src/lib/run-steps.ts) | Progress milestones derived from backend log messages. |
| [`App.tsx`](../app/frontend/src/App.tsx) | Root flow routing and always-mounted permission/quit dialogs. |
| [`role-screen.tsx`](../app/frontend/src/screens/role-screen.tsx), [`client-screen.tsx`](../app/frontend/src/screens/client-screen.tsx) | First-run navigation without persistence, client discovery/connection and saved connection state. |
| [`setup/`](../app/frontend/src/screens/setup) | Computer checks with the service install, clinic name and password steps. |
| [`installing-screen.tsx`](../app/frontend/src/screens/install/installing-screen.tsx), [`failed-screen.tsx`](../app/frontend/src/screens/install/failed-screen.tsx) | Log-backed milestones, retained-attempt retry and explicit cleanup fallback. |
| [`confirmation-dialog.tsx`](../app/frontend/src/components/confirmation-dialog.tsx), [`quit-dialog.tsx`](../app/frontend/src/components/quit-dialog.tsx) | Request registration, stale-response protection, safe consent focus and response errors. |
| [`use-app-update.ts`](../app/frontend/src/hooks/use-app-update.ts), [`panel-update-lock.tsx`](../app/frontend/src/screens/panel/panel-update-lock.tsx) | Shared updater, installer acknowledgement, restart guard and live/stale-operation exclusion. |
| [`panel-screen.tsx`](../app/frontend/src/screens/panel/panel-screen.tsx), [`overview-tab.tsx`](../app/frontend/src/screens/panel/overview-tab.tsx), [`panel-status.ts`](../app/frontend/src/screens/panel/panel-status.ts) | Panel navigation, service status and Start/Stop/Restart. |
| [`home-access.tsx`](../app/frontend/src/screens/panel/home-access.tsx), [`phone-dialog.tsx`](../app/frontend/src/screens/panel/phone-dialog.tsx), [`network-card.tsx`](../app/frontend/src/screens/panel/network-card.tsx) | Home access, the mobile setup QR and the network fix. |
| [`advanced-tab.tsx`](../app/frontend/src/screens/panel/advanced-tab.tsx), [`admin-password.tsx`](../app/frontend/src/screens/panel/admin-password.tsx), [`update-checks.tsx`](../app/frontend/src/screens/panel/update-checks.tsx) | Fixed 15-minute UI unlock, password change, update-check switch and uninstall. |
| [`remove-screen.tsx`](../app/frontend/src/screens/remove-screen.tsx) | The screen the Windows uninstaller opens: server uninstall, client disconnect or unfinished-setup cleanup, then exit. |
| [`env-editor.tsx`](../app/frontend/src/screens/panel/env-editor.tsx), [`env-file.ts`](../app/frontend/src/lib/env-file.ts), [`env-schema.ts`](../app/frontend/src/screens/panel/env-schema.ts), [`env-controls.tsx`](../app/frontend/src/screens/panel/env-controls.tsx), [`advanced-env.ts`](../app/frontend/src/screens/panel/advanced-env.ts) | The clinic settings editor. See [configuration](configuration-and-settings.md#applying-settings). |
| [`tests/fixtures/host.ts`](../app/frontend/tests/fixtures/host.ts) | Simulated native state/events used only by the explicit Vite test entry. |

See [Wails API](wails-application.md) and [configuration](configuration-and-settings.md) for behavior rather than visual layout.

## Deployment kit

| Source | Runtime purpose |
| --- | --- |
| [`deployments/.env`](../deployments/.env) | Release version, image names/bases, upstream repos and refs. |
| [`docker-compose.yml`](../deployments/docker-compose.yml) | Services, dependencies, mounts, health checks; run by the agent in the engine VM. |
| [`backend.env`](../deployments/backend.env) | Initial CARE backend settings; installed copy becomes clinic-specific. |
| [`frontend.env`](../deployments/frontend.env) | CARE frontend build settings, baked into the frontend image. |
| [`Caddyfile`](../deployments/Caddyfile) | HTTPS/local CA, reverse proxy, storage routes, public root bootstrap, WAF behavior. |
| [`caddy.Dockerfile`](../deployments/caddy.Dockerfile) | Builds the proxy with the Coraza module. |
| [`minio/entrypoint.sh`](../deployments/minio/entrypoint.sh) | Starts Silo, waits for readiness, configures credentials and buckets under the retained storage-service identity. |
| [`setup/index.html`](../deployments/setup/index.html) | The phone/tablet certificate setup page. |

`backup.Dockerfile` and `scripts/backup.sh` are still shipped but not uploaded
to the engine; backups return later via engine agent ops.

## Build and automation

| File | Responsibility |
| --- | --- |
| [`app/go.mod`](../app/go.mod), [`app/go.sum`](../app/go.sum) | Go module identity, toolchain requirement, and dependencies. |
| [`app/wails.json`](../app/wails.json) | Desktop/frontend build hooks (including the macOS bundle hook), output identity, installer metadata. |
| [`scripts/bundle-macos.sh`](../scripts/bundle-macos.sh) | Wails post-build hook: puts the service and an engine bundle inside the `.app`. See [bundle layout](releases.md#macos-app-bundle-layout). |
| [`scripts/service-macos.sh`](../scripts/service-macos.sh) | Developer install, status and uninstall of the LaunchDaemon. See [the care-clinic-service](development-and-release.md#the-care-clinic-service). |
| [`scripts/dev-images.sh`](../scripts/dev-images.sh) | Builds and saves dev images for the engine. See [dev images](dev-images.md). |
| [`app/.golangci.yml`](../app/.golangci.yml) | Go lint and formatter policy. |
| [`app/.gitignore`](../app/.gitignore) | Separates generated/staged output from versioned source. |
| [`frontend/package.json`](../app/frontend/package.json), [`package-lock.json`](../app/frontend/package-lock.json) | Desktop frontend scripts and dependency lock. |
| [`frontend/tsconfig.json`](../app/frontend/tsconfig.json) | Type-checking and local import aliases. |
| [`frontend/vite.config.ts`](../app/frontend/vite.config.ts) | Embedded-compatible asset URLs, output directory, placeholder retention. |
| [`frontend/playwright.config.ts`](../app/frontend/playwright.config.ts) | Isolated loopback test server, test-only entry, browser workers and failure traces. |
| [`.pre-commit-config.yaml`](../.pre-commit-config.yaml) | Whitespace/YAML checks, Go formatting and package-wide Go lint before commits. |
| [`stage-install.mjs`](../app/frontend/scripts/stage-install.mjs) | Refreshes the embedded-kit staging tree from `deployments/`. |
| [`check-bindings.mjs`](../app/frontend/scripts/check-bindings.mjs) | Checks desktop declarations against exported Go methods and arities. |
| [`prune-fonts.mjs`](../app/frontend/scripts/prune-fonts.mjs) | Removes specified legacy asset extensions after bundling. |
| [`build/darwin/Info.plist`](../app/build/darwin/Info.plist) | macOS production bundle metadata template. |
| [`build/darwin/Info.dev.plist`](../app/build/darwin/Info.dev.plist) | Development bundle metadata, including local-network transport allowance. |
| [`build/appicon.png`](../app/build/appicon.png) | Wails application-icon source. |
| [`build/windows/info.json`](../app/build/windows/info.json), [`wails.exe.manifest`](../app/build/windows/wails.exe.manifest) | Windows version resource and manifest templates filled from `wails.json`. |
| [`build/windows/installer/project.nsi`](../app/build/windows/installer/project.nsi) | NSIS installer definition; `wails_tools.nsh` beside it is generated. |
| [`ci.yml`](../.github/workflows/ci.yml) | Compilation, package boundary, test, formatting/lint, frontend build checks. |
| [`release.yml`](../.github/workflows/release.yml) | Release identity, platform builds, nested service and app signing, packaging, draft release or artifact upload. |
| [`actions/install-nsis`](../.github/actions/install-nsis/action.yml) | Pinned NSIS install shared by CI and release. |
| [`signpath/artifact-configuration.xml`](../.github/signpath/artifact-configuration.xml) | SignPath artifact configuration: metadata restrictions the signed files must satisfy. |

The full pipeline and current platform/tool versions are in [development and release](development-and-release.md).

## Navigating a change bottom-up

For a native behavior, start with its `internal/sys` package, then the calling domain/`Clinic` code, then the `App` boundary and desktop consumer. For a button-triggered behavior, follow the same path in reverse.

When source files are split or moved, keep this inventory and the owning subsystem guide synchronized. A new helper is not fully integrated until its error path, ownership, caller, and test seam are understandable from those two places.
