#!/bin/sh
# Usage: build-initramfs <src-dir> <out>
# Runs inside the target-arch rootfs (with busybox-static, kmod, cpio).
# Modules are stored uncompressed: busybox modprobe cannot load .ko.gz.
set -eu
src=$1 out=$2
kver=$(ls /lib/modules)
root=$(mktemp -d)
mkdir -p "$root/bin" "$root/dev" "$root/proc" "$root/sys" "$root/etc" "$root/lib/modules/$kver"
cp /bin/busybox.static "$root/bin/busybox"
cp "$src/init" "$root/init"
chmod 0755 "$root/init" "$root/bin/busybox"
cp "$src/modules" "$root/etc/modules-initramfs"
for m in $(cat "$src/modules"); do
	modprobe -S "$kver" --show-depends "$m" | awk '$1 == "insmod" { print $2 }'
done | sort -u | while read -r ko; do
	mkdir -p "$root$(dirname "$ko")"
	case "$ko" in
	*.gz) gzip -dc "$ko" >"$root${ko%.gz}" ;;
	*.xz) xz -dc "$ko" >"$root${ko%.xz}" ;;
	*.zst) zstd -qdc "$ko" >"$root${ko%.zst}" ;;
	*) cp "$ko" "$root$ko" ;;
	esac
done
cp /lib/modules/"$kver"/modules.order /lib/modules/"$kver"/modules.builtin* "$root/lib/modules/$kver/"
depmod -b "$root" "$kver"
mkdir -p "$(dirname "$out")"
(cd "$root" && find . | sort | cpio -o -H newc -R 0:0 --quiet | gzip -9n) >"$out"
echo "initramfs: $(wc -c <"$out") bytes, modules:"
find "$root/lib/modules" -name '*.ko*' | sed "s|$root/lib/modules/$kver/kernel/||"
