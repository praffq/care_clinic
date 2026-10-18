#!/bin/sh
# Usage: assemble <files-dir> <version> <arch> <out-dir>
set -eu
files=$1 version=$2 arch=$3 out=$4
name="care-engine-$version-$arch"
stage=$(mktemp -d)/$name
mkdir -p "$stage" "$out"
cp "$files"/vmlinuz "$files"/initramfs "$files"/system.img "$stage/"
sum() { sha256sum "$stage/$1" | cut -d' ' -f1; }
cat >"$stage/manifest.json" <<JSON
{
  "version": "$version",
  "arch": "$arch",
  "data_schema": $(cat "$files/data-schema"),
  "files": {
    "vmlinuz": "$(sum vmlinuz)",
    "initramfs": "$(sum initramfs)",
    "system.img": "$(sum system.img)"
  }
}
JSON
cat "$stage/manifest.json"
tar -C "$(dirname "$stage")" --owner=0 --group=0 --numeric-owner --mtime=@0 \
	-cf - "$name" | gzip -9n >"$out/$name.tar.gz"
(cd "$out" && sha256sum "$name.tar.gz" >"$name.tar.gz.sha256")
ls -l "$stage" "$out"
