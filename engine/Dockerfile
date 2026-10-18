# syntax=docker/dockerfile:1.7
# Builds the CARE engine VM bundle. Driven by engine/build.sh; requires the
# named build context "app" (the repo's app/ directory) for the agent source.

ARG ALPINE_VERSION=3.24
ARG GO_VERSION=1.26

# --- placeholder agent, cross-compiled on the build platform -----------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS agent
ARG TARGETARCH
ARG ENGINE_VERSION
WORKDIR /src
COPY --from=app go.mod go.sum ./
COPY --from=app cmd/care-engine-agent ./cmd/care-engine-agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${ENGINE_VERSION}" \
    -o /out/care-engine-agent ./cmd/care-engine-agent

# --- guest root filesystem (target platform) ---------------------------------
FROM alpine:${ALPINE_VERSION} AS rootfs
ARG ENGINE_VERSION
RUN apk add --no-cache \
      openrc busybox-openrc busybox-mdev-openrc linux-virt \
      docker docker-cli-compose docker-cli-buildx \
      ca-certificates e2fsprogs chrony \
 && rc-update add sysfs sysinit \
 && rc-update add procfs sysinit \
 && rc-update add devfs sysinit \
 && rc-update add dmesg sysinit \
 && rc-update add mdev sysinit \
 && rc-update add hwdrivers sysinit \
 && rc-update add cgroups sysinit \
 && rc-update add modules boot \
 && rc-update add sysctl boot \
 && rc-update add hostname boot \
 && rc-update add bootmisc boot \
 && rc-update add syslog boot \
 && rc-update add networking boot \
 && rc-update add chronyd default \
 && rc-update add docker default
COPY rootfs/ /
COPY --from=agent /out/care-engine-agent /usr/bin/care-engine-agent
RUN rc-update add care-engine-agent default \
 && sed -i 's/^#\?rc_cgroup_mode=.*/rc_cgroup_mode="unified"/' /etc/rc.conf \
 && echo "${ENGINE_VERSION}" > /etc/care-engine/version \
 && rm -f /etc/motd /boot/initramfs-* \
 && rm -rf /var/cache/apk/*

# --- kernel: Alpine ships arm64 as an EFI zboot image; VZ needs a raw Image --
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS kernel
RUN apk add --no-cache zstd xz
COPY scripts/extract-kernel.sh /usr/local/bin/extract-kernel
RUN --mount=type=bind,from=rootfs,source=/boot,target=/boot \
    extract-kernel /boot/vmlinuz-virt /out/vmlinuz

# --- initramfs (target platform, for busybox-static and kmod) ----------------
FROM rootfs AS initramfs
RUN apk add --no-cache busybox-static kmod cpio
COPY initramfs/ /initramfs-src/
COPY scripts/build-initramfs.sh /usr/local/bin/build-initramfs
RUN build-initramfs /initramfs-src /out/initramfs

# --- system.img (EROFS, lz4hc) -----------------------------------------------
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS system
RUN apk add --no-cache erofs-utils
RUN --mount=type=bind,from=rootfs,target=/rootfs \
    mkdir -p /out && mkfs.erofs -zlz4hc,12 -Eztailpacking -T0 \
      --exclude-regex='^boot/.+' /out/system.img /rootfs

# --- bundle: manifest + tarball ----------------------------------------------
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS assemble
ARG ENGINE_VERSION
ARG TARGETARCH
RUN apk add --no-cache tar
COPY rootfs/etc/care-engine/data-schema /b/data-schema
COPY --from=kernel /out/vmlinuz /b/vmlinuz
COPY --from=initramfs /out/initramfs /b/initramfs
COPY --from=system /out/system.img /b/system.img
COPY scripts/assemble.sh /usr/local/bin/assemble
RUN assemble /b "${ENGINE_VERSION}" "${TARGETARCH}" /out

FROM scratch AS bundle
COPY --from=assemble /out/ /
