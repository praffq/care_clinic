#!/bin/sh
# Boot a built bundle under QEMU (TCG, inside a container) with a blank data
# disk and wait for the agent to answer GET /v1/status via hostfwd.
# Usage: engine/smoke.sh <arm64|amd64> [timeout-seconds]
set -eu
arch=${1:-}
timeout=${2:-600}
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
version=$(tr -d '[:space:]' <"$here/VERSION")
name="care-engine-$version-$arch"

case "$arch" in
arm64)
	qemu="qemu-system-aarch64 -M virt -cpu max"
	console=ttyAMA0
	pkg=qemu-system-aarch64
	;;
amd64)
	qemu="qemu-system-x86_64 -M q35 -cpu max"
	console=ttyS0
	pkg=qemu-system-x86_64
	;;
*) echo "usage: $0 <arm64|amd64> [timeout-seconds]" >&2; exit 2 ;;
esac

bundle="$repo/dist/$name.tar.gz"
[ -f "$bundle" ] || { echo "missing $bundle; run engine/build.sh $arch" >&2; exit 1; }

work=$(mktemp -d)
container="care-engine-smoke-$arch-$$"
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

tar -C "$work" -xzf "$bundle"
log="$repo/dist/smoke-$arch.log"

docker run -d --name "$container" \
	-v "$work/$name:/bundle:ro" alpine:3.24 sh -c "
	apk add --no-cache -q $pkg qemu-img >/dev/null &&
	qemu-img create -f raw /data.img 8G >/dev/null &&
	exec $qemu -accel tcg -smp 2 -m 2048 -nographic -no-reboot \
		-kernel /bundle/vmlinuz -initrd /bundle/initramfs \
		-append 'console=$console care.system=/dev/vda care.data=/dev/vdb panic=-1' \
		-drive file=/bundle/system.img,if=virtio,format=raw,readonly=on \
		-drive file=/data.img,if=virtio,format=raw \
		-netdev user,id=n0,hostfwd=tcp::47377-:47377 -device virtio-net-pci,netdev=n0" >/dev/null

echo "booting $name under QEMU TCG (log: $log)"
start=$(date +%s)
while :; do
	docker logs "$container" >"$log" 2>&1 || true
	# Query through QEMU's hostfwd from inside the container; this avoids
	# depending on the Docker host's port publishing (flaky on Rancher/Lima).
	if out=$(docker exec "$container" wget -qO- -T 3 http://127.0.0.1:47377/v1/status 2>/dev/null) &&
		echo "$out" | grep -q '"docker_ready":true'; then
		echo "agent ready after $(($(date +%s) - start))s: $out"
		exit 0
	fi
	if [ -z "$(docker ps -q -f "name=^$container$")" ]; then
		echo "VM exited; see $log" >&2
		tail -40 "$log" >&2
		exit 1
	fi
	if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
		echo "timed out after ${timeout}s (last response: ${out:-none}); see $log" >&2
		tail -40 "$log" >&2
		exit 1
	fi
	sleep 5
done
