[Documentation index](README.md)

# Cleanup and uninstall

Removing CARE Clinic has three independent parts: the **clinic** (removed by
the app), the **desktop app** itself, and the **host service with its engine
VM** (removed by a script). Removing the clinic never removes the service or
the engine.

## 1. Choose the correct operation

| Intent | Entry point | Removed | Kept |
| --- | --- | --- | --- |
| Pause the clinic | `ClinicAction("stop")` | Nothing; the stack stops. | Containers, data, configuration. |
| Remove the clinic | `RunUninstall(removeData, adminPassword)` | The stack in the engine; with **also delete clinic data**, its database, files and uploaded configuration. This computer's hosts entry, certificate trust, firewall rules and Tailscale home access; the install folder; `config.json` is reset to `{}`. | The service, the engine and its slots, the app, logs. Without **also delete clinic data**, the clinic's volumes in the engine. |
| Recover from failed first setup | `CleanupFailedInstall()` | The unfinished clinic in the engine (with its data) and the install folder. If the service isn't answering, only the install folder. The saved name and password hash are forgotten. | The role, the service and the engine. |
| Disconnect a client | `DisconnectClient()` | This client's hosts entry and owned certificate, then the role. See [native integrations](native-integrations.md#removing-client-access). | Certificates this client did not install. |
| Remove the desktop app | `RemoveApp()`, or the Windows uninstaller | The app. | Everything else. |
| Remove the service and engine | `scripts/service-macos.sh uninstall [--purge]` | See [section 4](#4-removing-the-service-and-engine). | |

Backups, plugins, residue scans and "Remove everything" were removed in v2;
they return later via engine agent ops.

## 2. Uninstall, phase by phase

`RunUninstall` is the `uninstall` job:

1. `requireAdmin` checks the CARE Clinic admin password.
2. `beginRemoval` persists `removing=true`. While it is set, normal start and
   configuration changes are refused, so a partial removal is finished rather
   than overwritten.
3. [`Clinic.Uninstall`](../app/internal/clinic/uninstall.go) reads the clinic's
   root certificate (best effort), then runs `StackDown` in the engine. With
   `removeData` it removes the volumes, deletes the uploaded config files (so
   the service stops bringing the stack up after boot) and deletes the host
   copy of `secrets.env`.
4. It reverts this computer's changes: certificate trust, the hosts entry and
   CARE's firewall rules. On Windows these are batched into one elevation.
   Failures are collected and reported; a declined prompt is not success.
5. It turns off Tailscale home access, logging rather than failing if
   Tailscale can't be reached.
6. It removes the install folder, refusing any path that isn't
   `.../care-clinic/install`.
7. The app resets `config.json`, logs "Uninstall complete." and emits
   `uninstalled`.

The UI shows the result only after both `uninstalled` and the matching
successful `care-done`. A failure leaves `removing=true` and the removal
available to run again.

CARE-owned confirmations use the in-window dialog (Continue/Cancel). The
operating system's own password prompts stay native.

## 3. Removing the desktop app

"Set up" means a role is saved and either the install folder has files or the
settings hold more than the role and chosen name.

**In the app.** The uninstall panel and the client **Disconnect** dialog offer
**Also remove the CARE Clinic app from this computer** when `CanRemoveApp()`
is true. After the cleanup succeeds, `RemoveApp()` waits for the job, quits
and then:

- **macOS** moves the `.app` bundle to the Trash with `NSFileManager`. It is
  refused when the app runs from the disk image or an App Translocation copy.
- **Windows** starts the installed `uninstall.exe`, which shows the usual
  administrator prompt.

Dragging the app to the Trash on macOS skips the clinic cleanup; the clinic
keeps running in the service.

**Windows uninstaller.** `un.onInit` in
[`project.nsi`](../app/build/windows/installer/project.nsi) runs the app first:

| Mode | Started as | What the app does |
| --- | --- | --- |
| Interactive | `CARE Clinic.exe --uninstall` | Exits 0 if nothing is set up. Otherwise opens the uninstall screen and exits 0 once the setup is gone, or 1 when kept. |
| Silent (`/S`) | `CARE Clinic.exe --uninstall-check` | Never shows a window. Exits 0 if nothing is set up, otherwise 1. |

| Exit code | Uninstaller result |
| --- | --- |
| 0 | Removes the app. |
| 1 | Stops: the computer still has a clinic setup or connection. |
| 3 | Stops: another CARE Clinic process was still running after 10 seconds. |
| 4 | The uninstaller runs as a different Windows account; asks before removing anyway. |

## 4. Removing the service and engine

The app never removes `care-clinic-service`. On macOS:

```sh
scripts/service-macos.sh uninstall          # stop and remove the LaunchDaemon and binary
scripts/service-macos.sh uninstall --purge  # also delete engine slots, VM data and logs
```

Without `--purge`, `/Library/Application Support/care-clinic` (engine slots
and VM disks, including any clinic data still in them) and
`/Library/Logs/care-clinic` are kept. The `care-clinic` group is kept either
way. Run the clinic uninstall first if its hosts entry and certificate
trust should be reverted too.

## 5. Invariants

- Persist `removing` before destructive work, and keep it until cleanup succeeds.
- A canceled privilege prompt or failed inspection is not successful cleanup.
- Clinic data is deleted only with an explicit `removeData`.
- The service and engine outlive the clinic and the app.
