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
3. formats the data disk as ext4 (label `care-data`) if it has no filesystem;
4. mounts a tmpfs-backed overlay over the system image as `/` (so `/etc`,
   `/var/log`, `/run` … are writable but ephemeral);
5. mounts the data disk at `/var/lib`, creates `/var/lib/docker`,
   `/var/lib/care/{clinic,backups,builds}`, and writes `1` to
   `/var/lib/care/.data-schema` on first init;
6. `switch_root`s into OpenRC.

On any failure it logs `care-init: FATAL: …` and drops to a console shell.

OpenRC then brings up networking (DHCP on `eth0`), chrony, `dockerd`, and
`care-engine-agent`. The agent service refuses to start (and logs why on the
console) when `/var/lib/care/.data-schema` is newer than the schema this
engine supports (`/etc/care-engine/data-schema`).

### Kernel command line

```
console=<serial> [care.system=/dev/vda] [care.data=/dev/vdb] [care.debug]
```

| Hypervisor | `console=` |
| --- | --- |
| Virtualization.framework (virtio console) | `hvc0` |
| QEMU `-M virt` (arm64) | `ttyAMA0` |
| QEMU `-M q35` / WHPX (amd64) | `ttyS0` |

Kernel, initramfs (`care-init: …`), OpenRC, and agent logs go to the console.
`care.debug` enables `set -x` in the initramfs. Docker's log is
`/var/log/docker.log` (ephemeral).

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
