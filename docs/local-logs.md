# Local logs

Everything CARE Clinic logs about the app, the host service, the engine VM and
the clinic's containers is kept on the clinic computer, in the folder the app
already uses. If the VM breaks and cannot be opened, the logs are still there.
The folder is small and cleans itself up.

| Platform | Log folder |
| --- | --- |
| macOS | `~/Library/Logs/care-clinic/` |
| Windows | `%LOCALAPPDATA%\care-clinic\logs\` |
| Linux | `$XDG_STATE_HOME/care-clinic/` (default `~/.local/state/care-clinic/`) |

Every log button in the app is labelled **Open log folder** and opens this
folder. The app has no export or upload feature; to share logs, zip the folder
yourself after reading the privacy note below.

## Files

| File | Written by | Contents |
| --- | --- | --- |
| `care-log.log` | the app | App operations: setup, installs, updates, backups. |
| `service.log` | care-clinic-service | The service's own log, including the VM lifecycle (boots, crashes, restarts, watchdog), host connections (port forwards, mDNS), backup export, and Go crash output. |
| `engine-console.log` | care-clinic-service | The VM's serial console: kernel, `care-init` (initramfs) and OpenRC output, with a `=== engine VM boot ... ===` line per boot. |
| `guest-system.log` | care-clinic-service | The guest's persistent system logs, one line per guest line, prefixed with its source: `messages:` (busybox syslogd, kernel messages through klogd), `docker:` (`/var/log/docker.log`) and `agent:` (`/var/log/care-engine-agent.log`). |
| `containers/<name>.log` | care-clinic-service | stdout and stderr of each container (`care-backend-1`, `care-db-1`, ...), with Docker's timestamp on every line and `stderr:` before stderr lines. Container names, not service names, so replicas and one-off containers stay apart. |
| `collector.log` | care-clinic-service | What the collector did: where it writes, engine stopped and started, streams interrupted and reconnected (with how long), gaps the guest reported, retention actions and write failures. |
| `app-update-helper.log` | the app's update helper | Output of the in-place update helper (macOS shell script, Windows transcript), kept after the update's work folder is deleted. |

Rotated copies sit next to each file as `<name>.1.log` and `<name>.2.log`.

There is no separate `engine-host.log`: VM lifecycle and host connection
events already go through the service's structured log, so they stay in
`service.log` where operators have always found them.

## How each stream is collected

Collection runs inside care-clinic-service (a LaunchDaemon on macOS, the
`CareClinic` Windows service), which is always running and needs no open app
window. `internal/service/hostlogs` holds the collector.

**Which folder.** The service runs as root (LocalSystem on Windows), so it
writes into the folder of the user who installed it. The install records that
user in the service root (`/Library/Application Support/care-clinic/log-user`
on macOS, written by the one-prompt install and `scripts/service-macos.sh`;
`%ProgramData%\CARE Clinic\log-user` on Windows, written by
`care-clinic-service service install --log-user <SID>`). At start the service
resolves the user's home (macOS: the account database; Windows: the
`ProfileList` registry entry of the SID), computes the same folder as the app
(`logdir.Default`), creates any missing folders owned by the user, and opens
it. Every component from the home folder down must be a plain folder owned by
the user or root: symlinks and junctions are refused, and all file access goes
through an `os.Root` so no file name can escape the folder. On macOS and Linux
new files and folders are chowned to the user; on Windows they inherit the
profile's ACL (the user, SYSTEM and Administrators). If `log-user` is missing
or invalid, or the user can't be resolved, the service writes the same files to its fallback folder,
`--log-dir` (`/Library/Logs/care-clinic`, `%ProgramData%\CARE Clinic\logs`,
or `<root>/logs` with `--dev`), and the first lines of `collector.log` and
`service.log` say why.

**`service.log`.** The service's `slog` output, via `internal/service/logfile`,
which also makes the file the process's stderr.

**`engine-console.log`.** The VM driver writes the console into a bounded
in-memory queue (4 MiB, `logdir.Async`) drained to the file by its own
goroutine. When the disk can't keep up, output is dropped instead of stalling
the VM; a marker line in the console log and a `collector.log` line record how
many bytes were lost.

**`guest-system.log`.** The agent op `logs.system` follows the three guest
files (polling once a second) and streams each new line with a cursor, the
file's inode and the byte offset after the line. The service saves the latest
cursor per source and sends them back on reconnect, so it resumes exactly
where it stopped. When the file was rotated since (busybox renames `messages`
to `messages.0`), the agent first reads the rest of the rotated file with the
same inode, then the new file. When it can't find the old inode, or the file
was emptied in place (logrotate `copytruncate` for `docker.log` and the agent
log), it sends a gap line, which goes to `collector.log`. Without a saved
cursor it starts with the last 64 KiB of each file.

**`containers/<name>.log`.** The agent op `logs.containers` lists containers
every 3 seconds, follows `docker logs --timestamps --follow` of every running
container (new and recreated containers are picked up on the next poll), and
reads the remaining output of stopped containers every 30 seconds. The service
saves the newest timestamp per container name and sends it back as `--since`
on reconnect; lines at or before it are skipped. Containers without a saved
position start with their last 200 lines.

**Reconnects.** Each stream reconnects with exponential backoff (1 s to 1 min)
after an interruption: VM crash, engine restart, engine update, or the
service restarting. While the engine is stopped the collector waits and notes
the pause and the resumption in `collector.log`. Positions are saved every 5
seconds and at shutdown to `logs-state.json` in the service root (not in the
log folder); containers not seen for 7 days are dropped from it.

## Retention

| Limit | Value |
| --- | --- |
| Rotation | each file at 5 MiB, keeping 2 rotated copies (`name.1.log`, `name.2.log`); `care-log.log` too |
| Inactive files | changed more than 7 days ago: rotated copies and `containers/` files are deleted, current files are emptied |
| Gone containers | `containers/<name>.log` of a container gone (or silent) for 7 days is deleted |
| Folder budget | 100 MiB for all `*.log` files; rotated copies and closed container logs are deleted oldest first, then the oldest current files are emptied |

The service sweeps the folder at start and every minute and logs each action
in `collector.log`. Current files (`service.log`, `care-log.log`, ...) are
emptied rather than deleted, since another process may have them open.
Files that don't end in `.log` are never touched. On a computer without the
service (Linux, or a client) `care-log.log` is bounded by its own rotation
(about 15 MiB).

Logging never blocks the VM or the containers: the console goes through the
bounded queue above, and container output is written by Docker to its own
files inside the VM (`mode: non-blocking`), which the collector reads
independently. If the host disk is slow, the guest streams just fall behind
and catch up from their saved positions.

## Inside the VM

The engine has no systemd/journald. Its persistent journal is the files in
`/var/log` on the data disk: `messages` (2 MiB × 3), `docker.log` and
`care-engine-agent.log` (5 MiB × 2, compressed, rotated hourly). Container
logs use Docker's `json-file` driver, 5 MiB × 2 per container, in
`/var/lib/docker` on the data disk, so they survive VM restarts for as long
as the container exists. Containers created before an engine update keep their
old log settings until they are recreated.

## Engine requirements

Guest logs need an engine built from this source: the `logs.*` agent ops
(`internal/engine/protocol/logs.go`), klogd, and the smaller Docker and
logrotate limits ship in the engine image. There is no fallback for older
engines: a failed log stream is reported in `collector.log` and retried like
any other interruption.

## Privacy

The agent masks credentials in every streamed line (passwords, tokens and keys
in `key=value` pairs, bearer tokens, passwords in URLs), but logs can still
contain patient information, URLs or other data printed by the clinic's
services. Review the files before sharing them. Nothing is uploaded
automatically.

## Removal

Uninstalling the clinic, the app or the service never deletes the log folder;
see [Cleanup and uninstall](cleanup-and-uninstall.md#logs-are-always-kept).
