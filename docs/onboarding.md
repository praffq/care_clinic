# Server onboarding

[Documentation index](README.md)

Setting up a Mac as the clinic server takes four steps in the desktop app:
**computer checks**, **cleanup of an earlier setup**, **clinic name and admin
password**, and **install**. The
app only drives the steps; the engine VM and the clinic containers belong to
the boot-time [`care-clinic-service`](../app/cmd/care-clinic-service/README.md).
Nothing is installed on the host besides that service: no Docker, Rancher
Desktop, Git or WSL.

Server setup is macOS-only for now. The Windows app compiles, but the Windows
service comes in a later slice, so server setup is not available there.

```mermaid
sequenceDiagram
    participant UI as Desktop UI
    participant App as Go App
    participant OS as macOS (admin prompt)
    participant Svc as care-clinic-service
    participant VM as Engine VM (agent)
    UI->>App: BeginServerSetup, HostChecks
    App-->>UI: virtualization, memory, disk, service
    UI->>App: InstallService (only if the service is missing)
    App->>OS: osascript ... with administrator privileges
    OS->>Svc: copy binary, plist, care-clinic group, launchctl bootstrap
    App->>Svc: wait until the control socket answers
    UI->>App: ScanResidue (PurgeResidue if anything is left)
    App->>Svc: engine status, stack status, uploaded config
    UI->>App: RunSetup(name, adminPassword)
    App->>Svc: engine install + activate (bundled engine, if newer)
    Svc->>VM: boot engine, forward 80/443/8480, advertise name.local
    App->>VM: load images, upload config, migrate, stack up, create admin
    App-->>UI: setup-done, care-done(0, "setup")
```

## 1. Computer checks

`BeginServerSetup` saves the Server role, then `HostChecks()` returns a
[`hostcheck.Report`](../app/internal/hostcheck/hostcheck.go): a list of checks,
each `ok`, `warn` or `fail`, and `ready`, which is false while any check fails.
Warnings do not block setup.

| Check | Fails when | Warns when |
| --- | --- | --- |
| Virtualization | Not a Mac with Apple silicon, or the hypervisor is unavailable. | — |
| Memory | Less than 8 GiB (the engine VM uses 4 GiB). | — |
| Disk | Less than 20 GiB free where the service keeps the engine (`/Library/Application Support/care-clinic`). | Less than 40 GiB free. |
| Service | The control socket does not answer. On macOS the check carries the `install-service` action. | The check isn't available on this system yet (Windows). |

### Installing the service

When the service check fails with `install-service`, the UI offers to install
it. `InstallService()` ([`internal/service/install`](../app/internal/service/install))
shows **one** macOS administrator prompt (`osascript` `do shell script ... with
administrator privileges`) whose script:

1. creates the `care-clinic` group if missing and adds the logged-in user;
2. copies `Contents/MacOS/care-clinic-service` from the app bundle (or
   `CARE_CLINIC_SERVICE_BINARY`) to
   `/Library/Application Support/care-clinic/bin/care-clinic-service`;
3. writes `/Library/LaunchDaemons/org.ohc.care-clinic.plist`, booting out a
   running copy first;
4. runs `launchctl bootstrap system`.

The app then waits up to a minute for the control socket
`/var/run/care-clinic/control.sock` to answer. Group membership normally
applies only to new logins, so the socket is not group-restricted on disk
(`0666`): the service checks each caller's uid itself and admits root and
members of `care-clinic`, reading membership from the user database as well as
the caller's login groups. The installing user can use the app straight away,
with no log out. Declining the prompt installs nothing.

Developers can install the same service with `scripts/service-macos.sh
install` or `install-built` (needs sudo); see
[development](development-and-release.md#the-care-clinic-service).

## 2. Cleanup of an earlier setup

The wizard page **Cleanup stale files** comes right after the computer
checks, so the service is already running. `ScanResidue()` returns a
[`residue.Report`](../app/internal/residue/residue.go): `clean` and the list of
traces an earlier CARE Clinic (v1 or v2) left on this computer. Continue stays
disabled until a scan is clean, and Review does not re-run it.

| Trace | Found when | Blocks setup |
| --- | --- | --- |
| `engine` | The engine is installed and its stack has containers or an uploaded `stack.env`, or the engine is stopped, crashed or backing off so its data couldn't be checked. | Yes |
| `containers`, `volumes`, `networks` | Docker answers and has v1 resources labelled `com.docker.compose.project=care-clinic`. | Yes |
| `images` | Docker answers and has `ghcr.io/ohcnetwork/care*` images. | No |
| `install-dir` | The install folder (`.../care-clinic/install`) exists. | Yes |
| `secrets` | The host copy of `secrets.env` exists in the clinic state folder. The backup certificate and recovery key next to it are kept. | Yes |
| `config` | `config.json` holds a password hash, `setup_done` or `removing`. A saved role or clinic name alone is not a trace. | Yes |
| `hosts`, `certificate` | A `# care-clinic` hosts line, or a trusted `CARE` root certificate. | Yes |
| `firewall` | CARE's own Windows firewall rules (`CARE Clinic <name>`). The service's `CARE Clinic` rule is not counted. | No |
| `autostart` | The v1 start-at-login entry: `~/Library/LaunchAgents/ohc.care-clinic.plist`, `~/.config/autostart/care-clinic.desktop`, or the `CARE Clinic` value under `HKCU\...\Run`. | Yes |

A service that does not answer, or an engine that is starting, stopping or not
ready, makes the scan fail rather than report clean. A stopped Docker is not
checked: its old containers can't hold ports or data open, and v1 volumes are
never attached to the engine.

**Remove it all** calls `PurgeResidue(true)`, which refuses a finished clinic
(use Uninstall instead), persists `removing`, and then:

1. If the scan found `engine`, waits for the engine (starting it if stopped)
   and runs `Clinic.Uninstall` with `RemoveData`: `StackDown` with volumes,
   the uploaded config files and `secrets.env`. Without it, `SkipStack` leaves
   the engine alone.
2. Reverts the hosts entry, certificate trust, CARE firewall rules and
   Tailscale home access, then deletes the install folder.
3. Removes the v1 Docker containers, volumes, networks and CARE images
   (an image still in use is logged and kept), the v1 start-at-login entry and
   any remaining `secrets.env`.
4. Forgets the settings except the role, rescans, and fails while anything
   blocking is left.

The service, the engine slots, backups already exported to the backup folder,
the backup key, admin recovery codes and the log stay. Approval prompts are the
same as for Uninstall: the in-window confirmation for the hosts file, and the
operating system's own password prompt.

## 3. Clinic name and admin password

The clinic name becomes `<name>.local` (default `care`). `MDNSStatus` and
`ValidateDomain` check it before setup; `RunSetup` checks again that no other
computer on the network advertises it, unless this Mac's service already does.
Keep other clinic servers awake during setup: an offline computer cannot be
detected.

The CARE Clinic admin password must be 8 to 20 characters with an uppercase
letter, a lowercase letter and a digit (`ValidatePassword`). It protects the
Advanced settings, uninstall and update-check switches, and becomes the
password of the CARE web login `admin`. Only its bcrypt hash is saved.

Once the password is valid and confirmed, the admin step asks for six one-time
recovery codes and keeps Continue disabled until they are saved:

- `SaveSetupRecoveryCodes()` opens a save dialog and writes the sheet to the
  chosen file. It never overwrites a file, and refuses CARE's install,
  settings, clinic state, log and backup folders. "" means the dialog was
  cancelled. Saving again issues a new set; the old codes stop working.
- `GetSetupRecoveryStatus()` reports `codes_saved`, `codes_path` and
  `codes_problem` (`missing`, `unreadable` or `mismatch` when the sheet was
  moved, damaged or no longer holds every active code).
- `OpenSetupRecoveryCodes()` opens the sheet for printing;
  `OpenSetupRecoveryFolder()` selects it in Explorer (Windows only).

The codes use the same storage as the control panel's: only their hashes are
kept, in `admin-recovery.json` in the clinic state folder. They are
independent of the password, so changing the password after saving them
keeps them valid, and `RunSetup` leaves the file alone. After installation
`GetAdminRecoveryCodeCount` reports 6 and `ResetAdminPassword` accepts them.
These setup calls are locked once `RunSetup` has stored the password hash, the
clinic is installed or it is being removed; after that, new codes come from
Advanced and need the admin password. Review checks the codes again before
installing. Cleaning up a failed install keeps the saved codes for the next
attempt; Uninstall deletes them.

## 4. Install

`RunSetup(name, adminPassword)` validates its input synchronously, then runs
the `setup` job:

1. Save the name and password hash in `config.json`.
2. Wait for the service, then **install the engine** if needed. The app uses
   the engine bundle shipped in `Contents/Resources/engine` (or
   `CARE_CLINIC_ENGINE_BUNDLE`). It installs and activates it through the
   service, the same as `ctl install` and `ctl activate`, when no engine is
   installed or the bundled version is newer than the active one. With no
   bundle, an already installed engine is used; otherwise setup fails.
3. Wait up to 5 minutes for the engine to be ready, starting it if stopped,
   and tell the service the clinic name to advertise.
4. Copy the deployment kit to the install folder, then `Clinic.Setup` loads the
   CARE images into the engine and uploads the rendered configuration.
5. `Clinic.Start` migrates the database, brings the stack up, creates the
   `admin` login and offers to set up this Mac's browser for
   `https://<name>.local/` (hosts entry and certificate trust; skipping it
   doesn't stop the clinic).

Images come from `CARE_CLINIC_DEV_IMAGES` (a
[`scripts/dev-images.sh`](dev-images.md) output folder) or, failing that, the
images the engine's clinic already runs. Release images come in a later
slice; without either, setup fails and suggests `ctl clinic install --images`.

Progress streams to the UI as `care-log` lines. Success emits `setup-done`
and `care-done(0, "setup")`, and saves `setup_done`. A failure emits
`setup-failed {can_retry}`:

- **Try again** (`RetrySetup`) resumes the same attempt while the app is still
  open, skipping preparation that already finished.
- Otherwise `CleanupFailedInstall` removes the clinic from the engine and this
  computer's install files, then setup starts over. The service and engine
  stay installed.

After setup the service keeps the clinic running: it boots the engine at
startup, brings the stack up after each engine boot, and advertises the name.
Quitting the app does not stop the clinic.

## Facility setup

Reaching the control panel means the clinic is installed; it does not mean a
facility, staff or clinical catalog exists in CARE. Facility setup happens in
the CARE web application. The CARE Onboarding frontend plugin is served by
the clinic and enabled in CARE's admin dashboard ([plugins.md](plugins.md)).
