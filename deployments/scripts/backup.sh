#!/bin/sh
# CARE backups, inside the engine VM (compose service "backup").
#
#   backup.sh                 daemon: a set every midnight (TZ), retry hourly
#   backup.sh once <id>       one set now (op backup.run)
#   backup.sh restore <id>    restore set <id> from /restore-input (op db.restore)
#
# A set is /backups/<id>/ with database.dump.enc (pg_dump -Fc) and
# files.tar.gz.enc (the Silo/minio data), both CMS-encrypted to the clinic
# certificate, and manifest.json listing each file's size and SHA-256. Sets are
# built in /backups/.partial-<id>/ and renamed into place once complete, so the
# agent never lists a half-written set. The host service copies complete sets
# out and deletes them here; there is no retention in the VM.
set -eu

BACKUP_DIR=/backups
MINIO_DIR=/minio-data
CERT=/keys/backup-cert.pem
INPUT_DIR=/restore-input
NEXT_FILE="$BACKUP_DIR/.next-backup"

DB_HOST="${POSTGRES_HOST:-db}"
DB_PORT="${POSTGRES_PORT:-5432}"
DB_USER="${POSTGRES_USER:-postgres}"
DB_NAME="${POSTGRES_DB:-care}"

log() { echo "[backup] $*"; }
fail() { echo "[backup] ERROR: $*"; return 1; }

if [ -z "${POSTGRES_PASSWORD:-}" ]; then
	echo "[backup] ERROR: POSTGRES_PASSWORD is not set - refusing to run"
	exit 1
fi
export PGPASSWORD="$POSTGRES_PASSWORD"

valid_id() {
	case "$1" in
	''|.*|*[!A-Za-z0-9T:_-]*) return 1 ;;
	esac
	[ "${#1}" -le 64 ]
}

wait_for_database() {
	attempt=1
	until pg_isready -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -t 5 >/dev/null 2>&1; do
		[ "$attempt" -gt 1 ] || log "database: waiting for PostgreSQL"
		[ "$attempt" -lt 30 ] || fail "PostgreSQL did not become ready after 30 checks" || return 1
		attempt=$((attempt + 1))
		sleep 5
	done
}

mb() { echo "$(( $1 / 1024 )) MB"; }
free_kb() { df -Pk "$BACKUP_DIR" | awk 'NR == 2 { print $4 }'; }

# The set needs room for the plain and sealed copy of the dump and the archive.
check_space() {
	db_bytes=$(PGCONNECT_TIMEOUT=5 psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
		"select pg_database_size(current_database())" 2>/dev/null || echo 0)
	case "$db_bytes" in ''|*[!0-9]*) db_bytes=0 ;; esac
	files_kb=$(du -sk "$MINIO_DIR" 2>/dev/null | cut -f1)
	case "$files_kb" in ''|*[!0-9]*) files_kb=0 ;; esac
	need=$(( (2 * db_bytes / 1024 + 2 * files_kb) * 5 / 4 + 262144 ))
	free=$(free_kb)
	case "$free" in ''|*[!0-9]*) log "WARNING: could not read free space - trying anyway"; return 0 ;; esac
	if [ "$free" -lt "$need" ]; then
		fail "not enough space for a backup: about $(mb "$need") needed, $(mb "$free") free"
		return 1
	fi
	log "space: $(mb "$free") free, about $(mb "$need") needed"
}

seal() {
	openssl cms -encrypt -binary -aes-256-cbc -stream -outform DER -in "$1" -out "$2" "$CERT"
}

# manifest <dir> <id>: write <dir>/manifest.json for the files in <dir>.
manifest() {
	{
		printf '{"id":"%s","created_at":"%s","files":[' "$2" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		sep=""
		for f in database.dump.enc files.tar.gz.enc; do
			sum=$(sha256sum "$1/$f" | cut -d' ' -f1)
			size=$(wc -c < "$1/$f" | tr -d ' ')
			printf '%s{"name":"%s","size":%s,"sha256":"%s"}' "$sep" "$f" "$size" "$sum"
			sep=","
		done
		printf ']}\n'
	} > "$1/manifest.json.tmp" && mv "$1/manifest.json.tmp" "$1/manifest.json"
}

# Leftovers of a set or restore that was interrupted.
clean_scratch() {
	rm -rf "$BACKUP_DIR"/.partial-* "$BACKUP_DIR"/.restore-*
}

make_set() {
	id=$1
	valid_id "$id" || fail "invalid backup id: $id" || return 1
	[ -f "$CERT" ] || fail "$CERT not found - backups must be encrypted" || return 1
	[ -d "$MINIO_DIR" ] || fail "$MINIO_DIR is not mounted" || return 1
	final="$BACKUP_DIR/$id"
	[ ! -e "$final" ] || fail "backup $id already exists" || return 1
	log "===== backup set $id ====="
	clean_scratch
	wait_for_database || return 1
	check_space || return 1

	work="$BACKUP_DIR/.partial-$id"
	mkdir -p "$work"
	log "database: dumping $DB_NAME"
	if ! pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -Fc -f "$work/database.dump"; then
		rm -rf "$work"; fail "pg_dump failed"; return 1
	fi
	if ! pg_restore --list "$work/database.dump" >/dev/null 2>&1; then
		rm -rf "$work"; fail "the dump failed verification (pg_restore --list)"; return 1
	fi
	if ! seal "$work/database.dump" "$work/database.dump.enc"; then
		rm -rf "$work"; fail "encrypting the dump failed"; return 1
	fi
	rm -f "$work/database.dump"

	log "files: archiving uploads"
	if ! tar -czf "$work/files.tar.gz" -C "$MINIO_DIR" . || ! tar -tzf "$work/files.tar.gz" >/dev/null; then
		rm -rf "$work"; fail "archiving the files failed"; return 1
	fi
	if ! seal "$work/files.tar.gz" "$work/files.tar.gz.enc"; then
		rm -rf "$work"; fail "encrypting the files archive failed"; return 1
	fi
	rm -f "$work/files.tar.gz"

	if ! manifest "$work" "$id" || ! sync || ! mv "$work" "$final"; then
		rm -rf "$work"; fail "publishing the set failed"; return 1
	fi
	sync
	log "backup set $id: SUCCESS ($(du -sh "$final" | cut -f1))"
}

with_lock() (
	flock -x 9 || fail "acquiring the backup lock failed" || exit 1
	"$@"
) 9>"$BACKUP_DIR/.backup.lock"

psql_admin() {
	psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d postgres -v ON_ERROR_STOP=1 -qAt "$@"
}

# restore_set <id>: the agent verified the inputs against the manifest and
# stopped everything that writes to the database or the files.
restore_set() {
	id=$1
	valid_id "$id" || fail "invalid backup id: $id" || return 1
	key="$INPUT_DIR/recovery-key.pem"
	[ -s "$key" ] || fail "no recovery key in the restore input" || return 1
	grep -Eq "\"id\"[[:space:]]*:[[:space:]]*\"$id\"" "$INPUT_DIR/manifest.json" || fail "the restore input is not set $id" || return 1
	wait_for_database || return 1
	clean_scratch
	work="$BACKUP_DIR/.restore-$id"
	mkdir -p "$work/files"
	trap 'rm -rf "$work"' EXIT

	log "restore $id: decrypting"
	openssl cms -decrypt -binary -inform DER -inkey "$key" -in "$INPUT_DIR/database.dump.enc" -out "$work/database.dump" ||
		fail "decrypting the database failed - is this the recovery key for this clinic?" || return 1
	openssl cms -decrypt -binary -inform DER -inkey "$key" -in "$INPUT_DIR/files.tar.gz.enc" -out "$work/files.tar.gz" ||
		fail "decrypting the files failed" || return 1

	log "restore $id: checking"
	pg_restore --list "$work/database.dump" >/dev/null || fail "the database dump is unreadable" || return 1
	tar -tzf "$work/files.tar.gz" > "$work/files.list" || fail "the files archive is unreadable" || return 1
	if grep -Eq '^/|(^|/)\.\.(/|$)' "$work/files.list"; then
		fail "the files archive has unsafe paths"; return 1
	fi
	tar -xzf "$work/files.tar.gz" -C "$work/files" || fail "extracting the files failed" || return 1
	rm -f "$work/files.tar.gz"

	log "restore $id: loading the database into ${DB_NAME}_restore"
	psql_admin -c "DROP DATABASE IF EXISTS ${DB_NAME}_restore" -c "DROP DATABASE IF EXISTS ${DB_NAME}_previous" || return 1
	psql_admin -c "CREATE DATABASE ${DB_NAME}_restore TEMPLATE template0" || return 1
	if ! pg_restore -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "${DB_NAME}_restore" \
		--exit-on-error --single-transaction --no-owner --no-privileges "$work/database.dump"; then
		psql_admin -c "DROP DATABASE IF EXISTS ${DB_NAME}_restore" || true
		fail "loading the dump failed; the current data was not changed"; return 1
	fi
	rm -f "$work/database.dump"

	log "restore $id: switching databases"
	psql_admin -1 \
		-c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN ('$DB_NAME', '${DB_NAME}_restore') AND pid <> pg_backend_pid()" \
		-c "ALTER DATABASE $DB_NAME RENAME TO ${DB_NAME}_previous" \
		-c "ALTER DATABASE ${DB_NAME}_restore RENAME TO $DB_NAME" >/dev/null ||
		fail "switching databases failed; the current data was not changed" || return 1

	log "restore $id: replacing the files"
	if ! find "$MINIO_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} + || ! cp -a "$work/files/." "$MINIO_DIR/"; then
		fail "replacing the files failed: the database is restored but the files are incomplete; run the restore again"
		return 1
	fi
	sync
	psql_admin -c "DROP DATABASE IF EXISTS ${DB_NAME}_previous" || log "WARNING: could not drop ${DB_NAME}_previous"
	log "restore $id: SUCCESS"
}

case "${1:-daemon}" in
once)
	with_lock make_set "${2:?no backup id given}"
	exit
	;;
restore)
	with_lock restore_set "${2:?no backup id given}"
	exit
	;;
daemon) ;;
*)
	echo "usage: backup.sh [daemon | once <id> | restore <id>]" >&2
	exit 2
	;;
esac

RETRY_SECONDS=3600
WAKE_SECONDS=300

# Midnight in TZ: seconds since the epoch of the next local 00:00.
next_midnight() {
	# shellcheck disable=SC2046
	set -- $(date '+%s %H %M %S')
	echo $(( $1 - ${2#0} * 3600 - ${3#0} * 60 - ${4#0} + 86400 ))
}

save_next() {
	printf '%s\n' "$1" > "$NEXT_FILE.tmp" && mv "$NEXT_FILE.tmp" "$NEXT_FILE" || true
	NEXT=$1
}

NEXT=$(cat "$NEXT_FILE" 2>/dev/null || true)
case "$NEXT" in ''|*[!0-9]*) NEXT=0 ;; esac
log "daemon started (TZ=${TZ:-UTC}); encrypted sets -> $BACKUP_DIR"

while true; do
	if [ "$(date +%s)" -ge "$NEXT" ]; then
		if with_lock make_set "daily-$(date -u +%Y%m%dT%H%M%SZ)"; then
			save_next "$(next_midnight)"
		else
			log "backup failed - see above; trying again in an hour"
			save_next $(( $(date +%s) + RETRY_SECONDS ))
		fi
	fi
	sleep "$WAKE_SECONDS"
done
