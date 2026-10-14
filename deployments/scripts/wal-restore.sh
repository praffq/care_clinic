#!/bin/sh
# PostgreSQL restore_command used only by a staged point-in-time restore:
# wal-restore.sh %f %p. Needs the recovery key at $CARE_RECOVERY_KEY.
# Exits 1 for a file that was never archived so PostgreSQL ends recovery.
set -eu
umask 077
. "${CARE_SCRIPTS:-/scripts}/backup-lib.sh"

name=${1:?usage: wal-restore.sh NAME PATH}
dest=${2:?usage: wal-restore.sh NAME PATH}
key=${CARE_RECOVERY_KEY:-/restore-key.pem}

wal_name_ok "$name" || exit 1
file="$WAL_DIR/$name.gz.enc"
sums="$WAL_DIR/$name.sha256"
[ -f "$file" ] && [ -f "$sums" ] || exit 1

if [ "$(sed -n 's/^sha256=//p' "$sums")" != "$(sha256_of "$file")" ]; then
	echo "[wal-restore] ERROR: $name is damaged (checksum mismatch)" >&2
	exit 2
fi
tmp="$dest.care-restore"
openssl cms -decrypt -binary -inform DER -in "$file" -inkey "$key" | gunzip -c >"$tmp"
if [ "$(sed -n 's/^plain_sha256=//p' "$sums")" != "$(sha256_of "$tmp")" ]; then
	rm -f "$tmp"
	echo "[wal-restore] ERROR: $name decrypted to unexpected contents" >&2
	exit 2
fi
mv "$tmp" "$dest"
