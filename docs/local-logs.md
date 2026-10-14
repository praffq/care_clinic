# Local diagnostics

CARE Clinic keeps local logs in one private folder:

- macOS: `~/Library/Logs/care-clinic/`
- Windows: `%LOCALAPPDATA%\care-clinic\logs\`
- Linux: `$XDG_STATE_HOME/care-clinic/` (default `~/.local/state/care-clinic/`)

`care-log.log` records app operations, installation, updates and backups.
`app-update-helper.log` preserves installer/relaunch diagnostics after the update
staging directory is removed. Windows helper transcripts append across attempts
instead of replacing earlier diagnostics, and are converted to UTF-8 before
joining the log.
`engine-host.log` records the VM lifecycle and host connections.
`engine-console.log` captures serial boot/kernel output.
`guest-system.log` captures guest system services, including Docker.
`containers/<container-name>.log` captures container stdout/stderr.
`collector.log` records collection interruptions, reconnects and retention failures.

The engine helper copies the guest journal to the host continuously, independently
of the app window. It reconnects after interruptions and replays from its last
journal cursor where available. Guest logging requires the rebuilt engine with
the `/v1/logs` agent endpoint and Docker's journald logging driver. Existing
containers retain their original logging driver until recreated.

The Logs page shows the live collection state reported by the helper
(collecting, connecting, reconnecting, engine stopped). Engine updates are
downloaded and verified in advance, but opening CARE Clinic or starting the
clinic never applies one. When an engine update is required, the page shows
"CARE engine update required" and waits for staff to choose **Update CARE
engine**. A failed update restores the previous engine; retry is an explicit
staff action
([details](vm.md#engine-update-rollback)).

Linux runs Docker directly: container logs stay in the Docker daemon and can be
read with `docker compose logs` from the installation directory. Guest/VM
collection applies only to the app-managed engine on macOS and Windows.

Logs rotate at 10 MiB with four retained stream files. A minute-by-minute sweep
expires files inactive for seven days (by modification time) and enforces a
shared 500 MiB budget, reclaiming oldest files first. Active logs are truncated
rather than unlinked. This does not remove individual old records inside a
recently modified file. The limit can
be briefly exceeded between sweeps. The guest journal also has a 200 MiB persistent
budget and seven-day retention. Capture is best-effort: a hard crash, disk failure,
journal expiry or a prolonged disconnection can lose messages; gaps are reported
where detectable. Container names, rather than fixed service names, distinguish
replicas and recreated services.
Serial capture uses a bounded queue and Docker logging uses non-blocking buffers:
logging must not stall the VM or containers if the host disk falls behind.

The **Logs** sidebar page shows the folder path, log categories, retention limits
and sharing guidance. **Open logs folder** opens the shared folder. **Download
support logs** first displays: “This file contains clinic passwords. Share it
only with the CARE support team.” The ZIP includes retained app and engine/guest
logs, the update journal and history, version and health evidence, and the
installation's `.env`, `backend.env`, `frontend.env`, `secrets.env` and
`env-defaults.yml` files. Those environment files include clinic passwords and
other secrets. The operator explicitly confirms before the ZIP is saved locally;
it is never uploaded automatically. Review it and share it only with CARE support.

Logged messages can also contain patient information, URLs or secrets printed by
services. Export snapshots may omit messages written during export or rotation.
They include only the last 10 MiB per file, with a 500 MiB combined log limit,
and reject symlinks and destinations inside the log directory. `versions.json`
records the app's engine pin, not a measurement of the running engine version;
check the live engine status when diagnosing a version mismatch.

GitHub Actions build logs remain on GitHub and are not included in local exports.
