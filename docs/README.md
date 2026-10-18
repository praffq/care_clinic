# CARE Clinic documentation

CARE Clinic is a local control application for a clinic's CARE installation. A boot-time root service, `care-clinic-service`, runs the clinic's Docker Compose stack inside an engine VM, forwards its ports and advertises its name. The Wails desktop app is only the user interface: it installs the service, sets up the clinic through it, and maintains the computer's local networking and trust.

At first run, choose to host the clinic or connect to its `.local` address.
The Start screen saves no role: Server is recorded when setup initializes,
Client when connecting begins. Client discovery changes nothing on the computer.
The server operations documented here do not run on clients.

These guides cover the operator's desktop workflows and the current
implementation, from React/Wails lifecycle handling to operating-system
primitives and the deployment kit. They do not replace the CARE medical
application's own user or API documentation.

## Start here

| Guide | What you will understand |
| --- | --- |
| [Using CARE Clinic](desktop-workflows.md) | Start, client connection, the setup wizard, installation, permission prompts and every panel tab. |
| [Architecture and design choices](architecture.md) | The system boundary, layers, state, dependency direction, and the reasons behind the design. |
| [Repository and file map](repository-map.md) | Where every Go component lives, what each file does, and which guide explains it. |
| [Wails application and API](wails-application.md) | Process startup, background jobs, concurrency, authorization, all bound methods, and events. |
| [Server onboarding](onboarding.md) | Computer checks, the one-prompt service install, engine install and clinic install. |
| [Configuration and settings](configuration-and-settings.md) | The installed directory, persisted configuration, environment files, and settings changes. |
| [Clinic lifecycle and deployment](clinic-lifecycle.md) | Setup through the stack runtime, images, starting, migrations, service relationships, and stopping. |
| [Cleanup and uninstall](cleanup-and-uninstall.md) | What each removal operation deletes or preserves, ownership checks, and partial-cleanup recovery. |
| [Native integrations](native-integrations.md) | Process execution, file persistence, logs, elevation, TLS trust, mDNS, and OS differences. |
| [The care-clinic-service](../app/cmd/care-clinic-service/README.md) | The root host service: ports, control socket, `ctl`, developer install and reboot test. |
| [Engine](../engine/README.md) | The engine VM image. |
| [Development and release](development-and-release.md) | Local builds, the service, embedded assets, release pins, CI, packaging, and changing the backend safely. |
| [Release pins](versions.md) | The `versions.yml` file that pins desktop, engine, CARE and image versions, and how it is validated. |
| [Dev image tarballs](dev-images.md) | Building the engine's images locally into `dist/images/*.tar` with digests for the dev update path. |
| [CI publishing](ci-publishing.md) | `images.yml`, `engine.yml` and `publish.yml`: multi-arch images on GHCR, engine releases, the signed feed on GitHub Pages, and the one-time key/Pages/package setup. |
| [Update feed](updates-feed.md) | The signed `updates.json` feed clients read, its signature and trust model, and how it is verified. |
| [Update coordinator](updates.md) | How updates are checked, applied, journaled, resumed and rolled back: CARE, engine slot swaps and desktop downloads. |
| [Plugins](plugins.md) | CARE backend plugins: catalog presets, custom plugins built in the VM, the journaled change and rollback, frontend plugins via CARE admin. |
| [Releasing CARE Clinic](releases.md) | Preparing a version, starting the release, the macOS app bundle layout, reviewing and publishing the draft, signing limitations, and retrying safely. |
| [Home access](home-access.md) | Letting doctors open CARE from home through Tailscale, without exposing the clinic to the internet. |

For a first reading, follow the table from top to bottom. If you are fixing one behavior, use the task map below instead.

## Find a behavior

| Task or question | Read |
| --- | --- |
| "What actually runs on the clinic computer?" | [System architecture](architecture.md) and [deployment services](clinic-lifecycle.md). |
| "Where does a button click enter Go?" | [Wails application and API](wails-application.md). |
| "When do I enter my computer password, and what does Cancel do?" | [Permission prompts](desktop-workflows.md#permission-prompts) and [the confirmation protocol](wails-application.md#in-window-permission-confirmations). |
| "Does Advanced really lock after 15 minutes?" | [Advanced's unlock window](desktop-workflows.md#advanced). |
| "Why are actions still locked after the CARE Clinic installer opens?" | [App update handoff](desktop-workflows.md#app-updates). |
| "Why does setup ask for my Mac password?" | [Installing the service](onboarding.md#installing-the-service). |
| "Who keeps the clinic running when the app is closed?" | [The care-clinic-service](../app/cmd/care-clinic-service/README.md). |
| "Why does an operation say something else is running?" | [Concurrency and job protocol](wails-application.md#concurrency-and-job-protocol). |
| "Which `backend.env` does the editor read?" | [Configuration and settings](configuration-and-settings.md). |
| "Why didn't a setting take effect?" | [Applying settings](configuration-and-settings.md#applying-settings). |
| "How are patient data and uploaded files stored?" | [Clinic lifecycle](clinic-lifecycle.md). |
| "What happens if uninstall is interrupted?" | [Cleanup and uninstall](cleanup-and-uninstall.md). |
| "The app works here but not on another device." | [Native integrations](native-integrations.md). |
| "How do staff computers connect?" | [Native client setup and initial trust](native-integrations.md#native-client-setup-and-trust-on-first-use). |
| "This client previously hosted CARE and now cannot reach another server." | [Client recovery and earlier-install cleanup](client-recovery.md). |
| "How do I reproduce the installed version?" | [Release identity](development-and-release.md#release-identity-and-pins). |
| "How do I release a new version?" | [Release runbook](releases.md). |
| "How does a new clinic get its facility, staff and master data?" | [Facility setup](onboarding.md#facility-setup). |
| "What is inside the macOS `.app`?" | [Bundle layout](releases.md#macos-app-bundle-layout). |
| "Which file should I change?" | [Repository map](repository-map.md). |

## Vocabulary

| Term | Meaning in this repository |
| --- | --- |
| CARE Clinic | The Go/Wails executable and its embedded desktop control panel. |
| Desktop frontend | `app/frontend/`: the interface inside the Wails window. It is not the CARE web application. |
| CARE frontend | The separately built `care_fe` web application served to clinic staff by the Compose stack. |
| `App` | The Wails-facing object in `app/*.go`; owns application state, authorization, jobs, confirmation requests and native integrations. |
| `Clinic` | The orchestration object in `app/internal/clinic/`; operates the installed stack through the stack runtime without importing Wails. |
| Service | `care-clinic-service`, the root LaunchDaemon that owns the engine VM, port forwarding, mDNS and boot-time start. The app reaches it on `/var/run/care-clinic/control.sock`. |
| Engine | The Linux VM (Apple Virtualization.framework) in which the agent runs the Compose stack. |
| Stack runtime | `app/internal/stack`: renders the kit, uploads it to the engine and runs typed agent ops. |
| Deployment kit | The files in `deployments/`, embedded into the desktop executable, unpacked locally and uploaded to the engine. |
| Installed kit | The runtime copy of that kit on the clinic computer; distinct from source and build staging. |
| Compose project | The named set of containers, networks, and volumes belonging to `care-clinic`. |
| Named volume | Docker-managed persistent storage inside the engine VM's data disk. |
| Pin | A version, image reference, repository URL, or source commit describing what a desktop release expects. |
| mDNS | Multicast DNS, used to advertise the clinic's `.local` name to nearby devices. |
| Root CA | The local certificate authority used to establish trust in the clinic's HTTPS certificate. |
| CARE web password | The password staff type to sign in to CARE in the browser. Separate from the CARE Clinic password after setup. |

## How to read the diagrams

Diagrams are Mermaid code blocks inside Markdown, so there are no separate image files to keep synchronized. GitHub renders them directly. In an editor without Mermaid support, the node labels and arrows remain readable as text.

An arrow in an architecture diagram means "calls or depends on" unless stated otherwise. A flowchart describes ordering and decisions, not parallel execution. Sequence diagrams explicitly distinguish a method being accepted from its eventual completion.

## Scope and source of truth

These guides describe the code in this checkout: the UI-only app, the
`care-clinic-service`, the engine VM and the Silo-backed storage service.
Backups and restore, storage monitoring, CARE software updates,
recovery codes, web password reset, start at login and residue cleanup were
removed in v2; they return later via engine agent ops.

The implementation and these guides are the source of truth; removed design
boards and historical notes are not runtime specifications.

File links lead to the implementation rather than fixed line numbers. When changing a component, update its guide, affected flowcharts, and the API or file map if the contract changed. Do not treat a diagram as a substitute for error handling in the code.

## Operational caution

Running the desktop application is not a read-only demonstration: it can refresh an existing installed kit and drive the installed service, which runs the real clinic. Setup, networking repair, and removal have real effects on the current computer. Use a disposable development environment for end-to-end experiments.

For safe UI regression work, use the [Playwright test fixture](development-and-release.md#safe-desktop-ui-tests).
It requires Vite test mode and does not operate a native clinic. Ordinary
`wails dev` uses the real backend; removed design boards and preview pages are
not application entry points.

The presence of regression tests is not a claim that every operating system or power-loss scenario has been exercised. The subsystem guides distinguish implemented recovery behavior from what their tests actually cover.
