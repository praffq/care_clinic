#!/bin/sh
# Shared helpers for the backup sidecar, the PostgreSQL WAL archiver and the
# restore helpers. Sourced, never executed directly.

BACKUP_DIR=${CARE_BACKUP_DIR:-/backups}
WAL_DIR=${CARE_WAL_DIR:-$BACKUP_DIR/wal}
CERT=${CARE_BACKUP_CERT:-/keys/backup-cert.pem}
EPHEMERAL_DIR=${CARE_EPHEMERAL_DIR:-/dev/shm}

wal_name_ok() {
	case "$1" in
		*[!0-9A-F.a-z]*) return 1 ;;
	esac
	printf '%s\n' "$1" | grep -Eq '^([0-9A-F]{24}(\.partial|\.[0-9A-F]{8}\.backup)?|[0-9A-F]{8}\.history)$'
}

wal_segment_name() {
	printf '%s\n' "$1" | grep -Eq '^[0-9A-F]{24}$'
}

sha256_of() {
	sha256sum "$1" | cut -c1-64
}

cert_serial() {
	openssl x509 -in "$CERT" -noout -serial | sed 's/^serial=//'
}

# check_envelope FILE: the CMS header is enveloped data with a key-transport
# recipient issued to this clinic's certificate. The clinic's private key is
# never on this computer, so this binds the file to the recovery key without
# being able to use it.
check_envelope() {
	serial=$(cert_serial) || return 1
	[ -n "$serial" ] || return 1
	header=$(head -c 65536 "$1" | openssl asn1parse -inform DER 2>/dev/null || true)
	printf '%s\n' "$header" | grep -q ':pkcs7-envelopedData' || return 1
	printf '%s\n' "$header" | grep -q ':rsaEncryption' || return 1
	serial=$(printf '%s' "$serial" | sed 's/^0*//')
	printf '%s\n' "$header" | sed -n 's/.*prim: INTEGER *:0*\([0-9A-F]*\)$/\1/p' | grep -qx "$serial"
}

# seal_verified PLAIN OUT: encrypt PLAIN for the clinic certificate and for a
# single-use verification key, then decrypt OUT with that key and require the
# plaintext checksum to match. The verification key lives only in memory-backed
# storage and is deleted before returning, so it can never decrypt anything
# later; the clinic recovery key remains the only lasting way in.
seal_verified() {
	seal_plain=$1
	seal_out=$2
	[ -f "$CERT" ] || { echo "[seal] ERROR: $CERT not found - refusing to write unencrypted data" >&2; return 1; }
	seal_tmp=$(mktemp -d "$EPHEMERAL_DIR/care-verify.XXXXXX") || return 1
	chmod 700 "$seal_tmp"
	seal_status=0
	if ! (umask 077
		openssl ecparam -name prime256v1 -genkey -noout -out "$seal_tmp/key.pem" &&
			openssl req -x509 -new -key "$seal_tmp/key.pem" -out "$seal_tmp/cert.pem" \
				-subj /CN=care-backup-verify -days 1) >/dev/null 2>&1; then
		echo "[seal] ERROR: could not create the single-use verification key" >&2
		seal_status=1
	elif ! openssl cms -encrypt -binary -aes-256-cbc -stream -outform DER \
		-in "$seal_plain" -out "$seal_out" "$CERT" "$seal_tmp/cert.pem"; then
		echo "[seal] ERROR: encryption failed" >&2
		seal_status=1
	elif ! check_envelope "$seal_out"; then
		echo "[seal] ERROR: the encrypted file is not addressed to this clinic's backup certificate" >&2
		seal_status=1
	else
		want=$(sha256_of "$seal_plain")
		got=$(openssl cms -decrypt -binary -inform DER -in "$seal_out" \
			-inkey "$seal_tmp/key.pem" -recip "$seal_tmp/cert.pem" | sha256sum | cut -c1-64) || got=""
		if [ -z "$want" ] || [ "$want" != "$got" ]; then
			echo "[seal] ERROR: decrypt test failed - the encrypted copy does not match the original" >&2
			seal_status=1
		fi
	fi
	rm -rf "$seal_tmp"
	[ "$seal_status" -eq 0 ] || rm -f "$seal_out"
	return "$seal_status"
}

# Segment arithmetic. Names are TTTTTTTTLLLLLLLLSSSSSSSS; with S segments per
# log id, the absolute number is L*S+S.
segment_number() {
	per=$2
	echo $(( 0x$(printf '%s' "$1" | cut -c9-16) * per + 0x$(printf '%s' "$1" | cut -c17-24) ))
}

segment_name() {
	timeline=$1
	number=$2
	per=$3
	printf '%s%08X%08X\n' "$timeline" $(( number / per )) $(( number % per ))
}

segments_per_log() {
	echo $(( 4294967296 / $1 ))
}

# wal_chain_check START LAST SEGMENT_BYTES: every segment from START to LAST on
# START's timeline is archived with an intact checksum and envelope.
wal_chain_check() {
	chain_start=$1
	chain_last=$2
	per=$(segments_per_log "$3")
	timeline=$(printf '%s' "$chain_start" | cut -c1-8)
	first=$(segment_number "$chain_start" "$per")
	last=$(segment_number "$chain_last" "$per")
	if [ "$last" -lt "$first" ]; then
		echo "[wal] ERROR: the newest archived change file is older than the full copy" >&2
		return 1
	fi
	# Archives on a later timeline mean the clinic's database branched (it
	# was recovered in place). That is not supported: refuse rather than
	# claim one unbroken chain.
	for f in "$WAL_DIR"/*.sha256; do
		[ -f "$f" ] || continue
		case "${f##*/}" in *.history.sha256) continue ;; esac
		other=$(printf '%s' "${f##*/}" | cut -c1-8)
		if [ "$((0x$other))" -gt "$((0x$timeline))" ]; then
			echo "[wal] ERROR: change files from a later database timeline ($other) are present; a new full backup cannot be chained to them" >&2
			return 1
		fi
	done
	n=$first
	while [ "$n" -le "$last" ]; do
		name=$(segment_name "$timeline" "$n" "$per")
		if ! wal_segment_ok "$name"; then
			echo "[wal] ERROR: gap or damage in archived changes at $name" >&2
			return 1
		fi
		n=$((n + 1))
	done
}

wal_file_intact() {
	file="$WAL_DIR/$1.gz.enc"
	sums="$WAL_DIR/$1.sha256"
	[ -f "$file" ] && [ ! -L "$file" ] && [ -f "$sums" ] || return 1
	want=$(sed -n 's/^sha256=//p' "$sums")
	[ -n "$want" ] && [ "$want" = "$(sha256_of "$file")" ] || return 1
	check_envelope "$file"
}

wal_segment_ok() {
	[ "$(sed -n 's/^waldump=//p' "$WAL_DIR/$1.sha256" 2>/dev/null)" = ok ] || return 1
	wal_file_intact "$1"
}

# newest_segment: highest archived segment on TIMELINE.
newest_segment() {
	newest=""
	for path in "$WAL_DIR"/"$1"????????????????.sha256; do
		[ -f "$path" ] || continue
		name=${path##*/}
		newest=${name%.sha256}
	done
	echo "$newest"
}
