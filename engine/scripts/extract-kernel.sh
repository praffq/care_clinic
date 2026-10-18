#!/bin/sh
# Usage: extract-kernel <vmlinuz> <out>
# Alpine's arm64 kernel is an EFI zboot PE wrapping a compressed Image.
# Virtualization.framework only boots a raw arm64 Image, so unwrap it.
# x86_64 bzImage is copied unchanged.
set -eu
in=$1 out=$2
mkdir -p "$(dirname "$out")"
u32() { od -An -t u4 -j "$2" -N 4 "$1" | tr -d ' '; }
magic=$(dd if="$in" bs=1 skip=4 count=4 2>/dev/null)
if [ "$(dd if="$in" bs=1 count=2 2>/dev/null)" = "MZ" ] && [ "$magic" = "zimg" ]; then
	off=$(u32 "$in" 8)
	size=$(u32 "$in" 12)
	comp=$(dd if="$in" bs=1 skip=24 count=8 2>/dev/null | tr -d '\0')
	echo "zboot payload: offset=$off size=$size compression=$comp"
	payload() { tail -c +"$((off + 1))" "$in" | head -c "$size"; }
	case "$comp" in
	gzip) payload | gzip -dc >"$out" ;;
	zstd | zstd22) payload | zstd -dc >"$out" ;;
	xzkern | xz) payload | xz -dc >"$out" ;;
	*) echo "unsupported zboot compression: $comp" >&2; exit 1 ;;
	esac
	if [ "$(dd if="$out" bs=1 skip=56 count=4 2>/dev/null)" != "$(printf 'ARM\144')" ]; then
		echo "decompressed kernel lacks arm64 Image magic" >&2
		exit 1
	fi
	echo "raw arm64 Image: $(wc -c <"$out") bytes"
else
	cp "$in" "$out"
	echo "kernel copied unchanged: $(wc -c <"$out") bytes"
fi
