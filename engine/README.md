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

`app/cmd/care-engine-agent` serves the engine protocol
(`app/internal/engine/agent`, executing ops with the docker CLI). It is built
static (`CGO_ENABLED=0`) during the image build. Listeners:

| Listener | When | Used by |
| --- | --- | --- |
| AF_VSOCK port `1024` (any CID) | always | Virtualization.framework (virtio-socket device) |
| TCP `:47377`, mutual TLS 1.3 | only if `/sys/firmware/qemu_fw_cfg/by_name/opt/care/tls/raw` exists | QEMU (Windows) |

The TLS bundle is passed with `-fw_cfg name=opt/care/tls,file=bundle.pem` and
holds three PEM blocks, in order: the CA certificate, the server certificate
and the server private key. Clients must present a certificate issued by that
CA. The bundle is read once at startup and never logged. Without the bundle
there is no TCP listener at all. The `vmw_vsock_virtio_transport`
and `qemu_fw_cfg` modules are loaded from `/etc/modules`. Request lines are
logged to `/var/log/care-engine-agent.log`.

`dockerd` uses the containerd image store (`"features":
{"containerd-snapshotter": true}` in `/etc/docker/daemon.json`), which image
digest checks rely on. Once dockerd answers, the agent logs the active store,
e.g. `docker image store: containerd (overlayfs [["driver-type","io.containerd.snapshotter.v1"]])`,
or a warning if it is not containerd.

Before the clinic stack is installed (no `/var/lib/care/clinic/docker-compose.yml`),
`stack.status` returns no services, `stack.stop`/`stack.down` succeed without
doing anything, and `stack.logs` fails with error code `not_installed`.

## Smoke test

```sh
engine/smoke.sh arm64   # or amd64; optional 2nd arg = timeout seconds
```

Boots the built bundle under QEMU TCG inside an `alpine` container with a
blank 8 GiB data disk, generates a throwaway CA plus server/client
certificates, passes the server bundle through fw_cfg, forwards guest port
47377 via QEMU `hostfwd`, and waits until `/v1/status` (over mutual TLS)
reports `docker_ready: true`; it then checks that a request without a client
certificate is rejected. (vhost-vsock is not available inside the container,
so the smoke test covers the TLS listener; the vsock listener is exercised by
`vzboot` below.) The serial log is written to `dist/smoke-<arch>.log`. The
status request is made from inside the container because Rancher Desktop's
published-port forwarding proved unreliable.

## Booting with Virtualization.framework (macOS)

`app/cmd/vzboot` is a development harness that boots a bundle the way the
macOS host service will:

```sh
engine/build.sh arm64
tar -C dist -xzf dist/care-engine-0.1.0-arm64.tar.gz
app/cmd/vzboot/build.sh            # builds dist/vzboot, ad-hoc signed with
                                   # com.apple.security.virtualization
dist/vzboot --bundle dist/care-engine-0.1.0-arm64 --data /tmp/data.img \
  [--mem 4096 --cpus 2] [--call 'POST /v1/ops/stack.status [JSON body]'] [--exit]
```

It creates a sparse 64 GiB data disk if absent, boots with the Linux boot
loader (`console=hvc0 care.system=/dev/vda care.data=/dev/vdb`), virtio-blk
(system.img read-only, then the data disk), NAT networking and a virtio-socket
device, mirrors the console to stdout and `<data>.console.log`, then polls
`GET /v1/status` over vsock port 1024 until `docker_ready` is true. Ctrl-C (or
`--exit`) sends `POST /v1/shutdown` and waits for the VM to power off,
force-stopping it after 30 s. On an M-series Mac the agent is ready ~7 s after
start on a blank data disk and ~3 s on later boots.
