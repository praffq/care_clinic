# Target system overview

This describes the final architecture agreed in [redesign.md](redesign.md).
The five stacked implementation phases deliver it incrementally; this is not a
claim that all phases already exist.

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
natively. The desktop invokes operations over the existing mTLS connection:
compose lifecycle, builds, image management, exec, file transfer, health and
streaming logs. There is no host Docker CLI, compose, Docker relay or Windows
folder sync in the target. The privileged helper only boots the VM and forwards
80/443. The coordinator drives the existing transactional engine updater.

CARE 3.1 has backend/frontend runtime and builder images. Plugin builds extend
the backend builder and copy its virtual environment onto the runtime.
Frontend settings compile into the image; local overrides trigger a builder
rebuild. Only the API URL is substituted by a tiny startup entrypoint.

## Configuration and recovery

The env editor manages persistent explicit overrides, including explicit empty
values. Effective env files combine overrides, deliberate string defaults from
`env-defaults.yml`, hostname-derived values and generated private secrets.
Clinic settings never trigger releases.

Backups live on local disk. A replacement backup must pass checksums,
decryption, PostgreSQL archive/physical/WAL checks and chain verification before
the previous set can be removed. Continuous encrypted WAL, nightly physical
copies and daily portable dumps support staging restores and point-in-time
recovery. WAL pruning is bounded by a newly verified full copy's start LSN.
Optional removable-storage copying cannot block clinic operation and verifies
new copies before deleting old ones. Weekly trial restores require enough
space. UI surfaces verification, recoverable time and failures.

The support ZIP includes logs, journal/history, version and health evidence,
and env files including secrets. Download requires the explicit clinic-password
warning from the agreed spec. No backward compatibility is required.
