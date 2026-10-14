#!/bin/sh
# PostgreSQL archive_command: wal-archive.sh %p %f
# Compresses one change file (WAL), encrypts it for the clinic backup
# certificate, proves the encrypted copy decrypts to the same bytes, and only
# then publishes it with an atomic rename. Any non-zero exit makes PostgreSQL
# keep the file in pg_wal and retry, so nothing is lost on failure.
set -eu
umask 077
. "${CARE_SCRIPTS:-/scripts}/backup-lib.sh"

src=${1:?usage: wal-archive.sh PATH NAME}
name=${2:?usage: wal-archive.sh PATH NAME}

if ! wal_name_ok "$name"; then
	echo "[wal-archive] ERROR: refusing unexpected file name $name" >&2
	exit 1
fi
[ -f "$src" ] || { echo "[wal-archive] ERROR: $src is missing" >&2; exit 1; }
mkdir -p "$WAL_DIR"

out="$WAL_DIR/$name.gz.enc"
sums="$WAL_DIR/$name.sha256"
plain_sum=$(sha256_of "$src")

# The checksum file is published last and is what marks a segment archived;
# readers ignore ciphertext without one.
if [ -f "$sums" ]; then
	# PostgreSQL may retry after a crash between publish and acknowledgement.
	if [ "$(sed -n 's/^plain_sha256=//p' "$sums")" = "$plain_sum" ] && wal_file_intact "$name"; then
		exit 0
	fi
	echo "[wal-archive] ERROR: $name is already archived with different contents; refusing to overwrite" >&2
	exit 1
fi
if [ -e "$out" ]; then
	# A crash after publishing the ciphertext but before its checksum leaves an
	# unacknowledged orphan. PostgreSQL still holds the source, so the orphan is
	# replaced by a freshly verified copy below.
	echo "[wal-archive] replacing unfinished archive of $name" >&2
fi

waldump=skipped
if wal_segment_name "$name"; then
	# Prove PostgreSQL can parse every record in the segment before archiving.
	dir=$(dirname "$src")
	case "$dir" in /*) ;; *) dir="$PWD/$dir" ;; esac
	if ! pg_waldump -q -p "$dir" "$name" >/dev/null 2>&1; then
		echo "[wal-archive] ERROR: pg_waldump could not read $name" >&2
		exit 1
	fi
	waldump=ok
fi

work=$(mktemp -d "$WAL_DIR/.tmp-$name.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM
gzip -c "$src" >"$work/plain.gz"
seal_verified "$work/plain.gz" "$work/file.gz.enc"
cipher_sum=$(sha256_of "$work/file.gz.enc")
printf 'sha256=%s\nplain_sha256=%s\nwaldump=%s\n' "$cipher_sum" "$plain_sum" "$waldump" >"$work/sums"
sync "$work/file.gz.enc" "$work/sums" 2>/dev/null || sync
mv "$work/file.gz.enc" "$out"
mv "$work/sums" "$sums"
sync "$WAL_DIR" 2>/dev/null || sync
