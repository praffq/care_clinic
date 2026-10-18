# CARE engine VM image

The engine is a small Alpine Linux VM that runs `dockerd` (with compose and
buildx) and `care-engine-agent`. The host service boots it directly from a
kernel + initramfs (macOS: Virtualization.framework; Windows: QEMU+WHPX).
Everything here is built with Docker only — no Nix, no sudo.

## Build

```sh
engine/build.sh arm64   # or amd64
```

Requires Docker with buildx (Rancher Desktop / Docker Desktop). Cross-arch
builds use the daemon's QEMU binfmt emulation. Output:

```
dist/care-engine-<ver>-<arch>.tar.gz
dist/SHA256SUMS
```

`engine/VERSION` is the single source of the engine version. The build is
reproducible for identical Alpine package versions (fixed mtimes, gzip `-n`).

## Bundle layout

```
care-engine-<ver>-<arch>/
  manifest.json   {version, arch, data_schema, files: {name: sha256}}
  vmlinuz         kernel (arm64: raw uncompressed Image unwrapped from Alpine's
                  EFI zboot vmlinuz, as required by VZLinuxBootLoader;
                  amd64: bzImage)
  initramfs       gzip cpio: static busybox + virtio/erofs/ext4/overlay modules
  system.img      EROFS (lz4hc) read-only root filesystem
```

Contents: Alpine 3.24 `linux-virt` (Alpine 3.22's kernel lacks EROFS),
OpenRC, docker, docker-cli-compose, docker-cli-buildx, ca-certificates,
e2fsprogs, chrony, and the agent.

## Disks and boot

| Device | Contents |
| --- | --- |
| `/dev/vda` | `system.img`, attached read-only |
| `/dev/vdb` | persistent data disk (raw, any size), mounted at `/var/lib` |

The initramfs (`initramfs/init`):

1. loads the modules in `initramfs/modules`;
2. mounts `system.img` (EROFS) read-only;
3. checks the data disk:
   - ext4 labelled `care-data`: used as is;
   - any other filesystem or label: refused;
   - no filesystem: formatted as ext4 (`care-data`) **only** if its first and
     last 1 MiB are all zeros, otherwise refused (a damaged superblock must
     never cause clinic data to be wiped);
4. mounts a tmpfs-backed overlay over the system image as `/` (so `/etc`,
   `/run` … are writable but ephemeral);
5. mounts the data disk at `/var/lib`, creates `/var/lib/docker`,
   `/var/lib/care/{clinic,backups,builds,logs}`, and writes `1` to
   `/var/lib/care/.data-schema` on first init;
6. bind-mounts `/var/lib/care/logs` on `/var/log` so logs persist;
7. `switch_root`s into OpenRC.

On any failure it logs `care-init: FATAL: …` to the console, waits 10 s and
powers the VM off (the host service sees the VM exit). With `care.debug` it
drops to an emergency shell instead.

OpenRC then brings up networking (DHCP on `eth0`), chrony, crond, `dockerd`,
and `care-engine-agent`. The agent only *uses* docker (not *needs*), so
`/v1/status` keeps answering with `docker_ready: false` when dockerd fails. The
agent service refuses to start (and logs why) when
`/var/lib/care/.data-schema` is newer than the schema this engine supports
(`/etc/care-engine/data-schema`).

### Logs

Persistent under `/var/log` (= `/var/lib/care/logs` on the data disk):
`docker.log`, `care-engine-agent.log` (logrotate hourly: 10 MiB × 5,
compressed), `messages` (busybox syslogd: 2 MiB × 3). Container logs use
Docker's `json-file` driver (10 MiB × 3).

### Kernel command line

```
console=<serial> [care.system=/dev/vda] [care.data=/dev/vdb] [care.debug]
```

| Hypervisor | `console=` |
| --- | --- |
| Virtualization.framework (virtio console) | `hvc0` |
| QEMU `-M virt` (arm64) | `ttyAMA0` |
| QEMU `-M q35` / WHPX (amd64) | `ttyS0` |

Kernel, initramfs (`care-init: …`) and OpenRC output go to the console.
`care.debug` enables `set -x` in the initramfs and an emergency shell on
failure.

## Agent

`app/cmd/care-engine-agent` is a placeholder: `GET /v1/status` on TCP
`:47377` returns `{"engine_version": "...", "docker_ready": bool}`. It is
built static (`CGO_ENABLED=0`) during the image build.

## Smoke test

```sh
engine/smoke.sh arm64   # or amd64; optional 2nd arg = timeout seconds
```

Boots the built bundle under QEMU TCG inside an `alpine` container with a
blank 8 GiB data disk, forwards guest port 47377 via QEMU `hostfwd`, and
waits until `/v1/status` reports `docker_ready: true`. The serial log is
written to `dist/smoke-<arch>.log`. The status request is made from inside
the container because Rancher Desktop's published-port forwarding proved
unreliable. On an Apple Silicon Mac both arches reach `docker_ready` in
under a minute.
