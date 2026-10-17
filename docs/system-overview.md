# System overview

This describes the implemented architecture agreed in [redesign.md](redesign.md).

## Release plane

`versions.toml` is the only human-edited release source. Four workflows validate
versions and constraints, resolve CARE backend/frontend branch heads once, build
only changed components, publish GHCR images and immutable GitHub Release
binaries, and sign a complete manifest in a reviewer-protected environment.
`updates.json` and its detached Ed25519 signature deploy together to Pages.
Requirements are configuration, not CI policy. Desktop trusts current and next
public keys and persists the highest accepted revision. Update coordination does
not change the existing macOS Apple ID signing/notarization or Windows SignPath
release paths.

## Clinic control plane

The desktop UI and background checker request plans from one Go update
coordinator. Only that coordinator changes installed desktop, engine and CARE
versions. It verifies downloads, checks requirements and machine health, waits
for staff approval, and journals each reversible step atomically. `Start()`
does not apply updates. Journal recovery precedes ordinary startup; finished
plans become history. Previous artifacts remain until health is established,
and database migration rollback restores a backup.

## Execution plane

On macOS and Windows, a Linux VM contains Docker, compose and the engine agent.
Windows QEMU is downloaded directly and unpacked. On Linux the same agent runs
natively behind a protected systemd-owned Unix socket. The desktop invokes
operations through a typed, streamed agent API:
compose lifecycle, builds, image management, exec, file transfer, health and
streaming logs. There is no host Docker CLI, compose, Docker relay or Windows
folder sync. Only the agent executes guest/native Docker and compose; the
desktop runner rejects host Docker commands. Job output keeps stdout and stderr
separate, preserves exit status and propagates cancellation to the agent's
process group.

Windows uses the VM's existing mTLS listener through QEMU forwarding. On macOS
the helper, which owns the Virtualization.framework VM and vsock handle,
transports the agent requests over mTLS through a private local control socket.
The helper manages VM boot and 80/443 forwarding, not container execution.
Linux installs the same agent binary as a root system service with access
restricted to the desktop user's group. The coordinator drives transactional
engine/agent updates; startup never activates a staged version. New engine
protocol versions require real immutable build artifacts and signed release
metadata before installation; source changes are not evidence that an old
published engine supports the new API.

CARE 3.1 has backend/frontend runtime and builder images. Plugin builds extend
the backend builder and copy its virtual environment onto the runtime.
Frontend settings compile into the image; local overrides trigger a builder
rebuild. Only the API URL is substituted by a tiny startup entrypoint.

## Configuration and recovery

The env editor manages persistent explicit overrides, including explicit empty
values. Effective env files combine overrides, deliberate string defaults from
`env-defaults.yml`, hostname-derived values and generated private secrets.
Clinic settings never trigger releases.

`deployments/env-defaults.yml` has strict string-only `be`/`fe` maps. Per-clinic
`backend.overrides.env` and `frontend.overrides.env` survive deployment refresh; reset
removes a key, while saving an equal-to-default or empty string keeps it
explicit. Setup generates independent credentials once in private `secrets.env`
(0600 and a restricted Windows DACL). The editor never reads effective env or
secrets. Hostname changes regenerate the API origin, CSRF origins and external
storage endpoint; PostgreSQL's URL is derived from its generated password.
Effective `backend.env`/`frontend.env` are deterministic private outputs.
Published frontend `build_env` and local builds preserve empty strings; JSON
build inputs avoid dotenv expansion of literal settings.

The desktop explicitly uploads the generated deployment, effective env, private
secrets, public backup certificate, scripts and build contexts into
`/var/lib/care`; no desktop directory is bind-mounted into the VM. Container
mounts refer to engine-local paths. Recovery files and selected backup inputs
are uploaded to unique private staging directories and removed after the job.

Backups originate on the engine's local disk. A replacement backup must pass checksums,
decryption, PostgreSQL archive/physical/WAL checks and chain verification before
the previous set can be removed. Continuous encrypted WAL, nightly physical
copies and daily portable dumps support staging restores and point-in-time
recovery. WAL pruning is bounded by a newly verified full copy's start LSN.
Optional removable-storage copying cannot block clinic operation and verifies
new copies before deleting old ones. Weekly trial restores require enough
space. UI surfaces verification, recoverable time and failures.
While the desktop is open, storage polling exports backup state and refreshes
the verified host backup set and completed encrypted WAL files through the
agent file API, even when optional second copying is disabled. Host exports
verify file sizes/checksums and publish the stable verified manifest last;
failed exports preserve the preceding record and cannot report a successful
second copy. The backup list, restore and recovery-bound views use these
explicit exports, not a sync daemon.
Point-in-time targets are bounded by the physical copy's completion and the
archived saved-through time. Staged replay must confirm it reached the requested
target before the journaled database swap. Timeline history is retained, but
cross-timeline archive chains are explicitly rejected. See
[backups and restore](backups-and-restore.md) for operation and alerts.

The support ZIP includes logs, journal/history, version and health evidence,
and env files including secrets. Download requires the explicit clinic-password
warning from the agreed spec. No backward compatibility is required.
