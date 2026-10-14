#!/bin/sh
# Staged point-in-time restore, run in a throwaway helper container:
#   pitr-stage.sh latest|'YYYY-MM-DD HH:MM:SS+00'
# Mounts: /backups (read-only backup folder), /keys (clinic certificate),
# /restore-key.pem (recovery key), /restore (staging volume).
# Rebuilds the clinic's database from the verified full copy plus archived
# change files in a private PostgreSQL, waits until replay finishes and the
# copy is promoted, then writes /restore/database.dump for the normal staged
# restore and swap. The running clinic database is never touched here.
set -eu
umask 077
. "${CARE_SCRIPTS:-/scripts}/backup-lib.sh"

target=${1:?usage: pitr-stage.sh latest|TIME}
manifest="$BACKUP_DIR/manifest.json"
stage=${CARE_RESTORE_DIR:-/restore}
data="$stage/pgdata"
sock="$stage/sock"
key="$stage/recovery-key.pem"
db=${POSTGRES_DB:-care}
user=${POSTGRES_USER:-postgres}

log() { echo "[restore] $*"; }
die() { log "ERROR: $*"; exit 1; }

[ -f "$manifest" ] || die "no verified backup manifest was found"
jq -e '.version == 1 and .checks.wal_chain == "ok"' "$manifest" >/dev/null || die "the backup manifest is not a verified set"
base=$(jq -r '.files[] | select(.path | test("^base-[0-9]{8}-[0-9]{6}/base\\.tar\\.gz\\.enc$")) | .path' "$manifest")
[ -n "$base" ] || die "the manifest lists no full copy"
want=$(jq -r --arg p "$base" '.files[] | select(.path == $p) | .sha256' "$manifest")
plain=$(jq -r --arg p "$base" '.files[] | select(.path == $p) | .plain_sha256' "$manifest")
start_wal=$(jq -r '.base.start_wal' "$manifest")
seg_bytes=$(jq -r '.base.wal_segment_bytes' "$manifest")
[ "$(sha256_of "$BACKUP_DIR/$base")" = "$want" ] || die "the full copy does not match its recorded checksum"
newest=$(newest_segment "$(printf '%s' "$start_wal" | cut -c1-8)")
[ -n "$newest" ] || die "no archived change files were found"
wal_chain_check "$start_wal" "$newest" "$seg_bytes" || die "archived change files have a gap; restoring would silently lose changes"
log "change files $start_wal to $newest are complete"

rm -rf "$data" "$sock"
mkdir -p "$data" "$sock"
cp "${CARE_RESTORE_KEY:-/restore-key.pem}" "$key"
chown postgres:postgres "$key" "$data" "$sock"
chmod 600 "$key"
chmod 700 "$data" "$sock"
cleanup() {
	gosu postgres pg_ctl -D "$data" -m fast stop >/dev/null 2>&1 || true
	rm -rf "$data" "$sock" "$key" "$stage/base.tar.gz"
}
trap cleanup EXIT INT TERM

log "decrypting the full copy"
openssl cms -decrypt -binary -inform DER -in "$BACKUP_DIR/$base" -inkey "$key" -out "$stage/base.tar.gz"
[ "$(sha256_of "$stage/base.tar.gz")" = "$plain" ] || die "the full copy decrypted to unexpected contents"
tar -xzf "$stage/base.tar.gz" -C "$data"
rm -f "$stage/base.tar.gz"
chown -R postgres:postgres "$data"

{
	echo "restore_command = 'CARE_RECOVERY_KEY=$key CARE_WAL_DIR=$WAL_DIR /bin/sh ${CARE_SCRIPTS:-/scripts}/wal-restore.sh %f %p'"
	echo "archive_mode = off"
	# Only the live clinic's own timeline is archived; never follow a branch.
	echo "recovery_target_timeline = 'current'"
	echo "listen_addresses = ''"
	echo "unix_socket_directories = '$sock'"
	if [ "$target" != latest ]; then
		echo "recovery_target_time = '$target'"
		echo "recovery_target_action = 'promote'"
		echo "recovery_target_inclusive = on"
	fi
} >>"$data/postgresql.auto.conf"
gosu postgres touch "$data/recovery.signal"

log "replaying archived changes ${target:+up to $target}"
gosu postgres pg_ctl -D "$data" -l "$sock/recovery.log" -t 60 -w \
	-o "-c hba_file=${CARE_SCRIPTS:-/scripts}/pg_hba.conf" start >/dev/null || {
	tail -n 20 "$sock/recovery.log" | sed 's/^/[restore]   /'
	die "the staged database could not start replaying changes"
}
deadline=$(( $(date +%s) + ${CARE_PITR_TIMEOUT:-7200} ))
while :; do
	state=$(psql -h "$sock" -U "$user" -d postgres -XtAc "select pg_is_in_recovery()" 2>/dev/null || echo down)
	[ "$state" != f ] || break
	if [ "$state" = down ] && ! gosu postgres pg_ctl -D "$data" status >/dev/null 2>&1; then
		tail -n 20 "$sock/recovery.log" | sed 's/^/[restore]   /'
		die "replaying changes failed before reaching the requested point"
	fi
	[ "$(date +%s)" -lt "$deadline" ] || die "replaying changes did not finish in time"
	sleep 2
done
psql -h "$sock" -U "$user" -d "$db" -XtAc "select 1" >/dev/null || die "the staged database is not healthy"
if [ "$target" != latest ]; then
	# PostgreSQL logs this only when replay stopped at the requested moment;
	# running out of change files first must not pass as the requested time.
	if ! grep -Eq 'recovery stopping (before|after) ' "$sock/recovery.log"; then
		tail -n 20 "$sock/recovery.log" | sed 's/^/[restore]   /'
		die "replay ended before reaching $target; nothing was changed"
	fi
	grep -E 'recovery stopping (before|after) ' "$sock/recovery.log" | tail -n1 | sed 's/^/[restore]   /'
fi
log "staged database promoted at $(psql -h "$sock" -U "$user" -d postgres -XtAc "select coalesce(pg_last_xact_replay_timestamp()::text, 'the full copy')")"
pg_dump -h "$sock" -U "$user" -d "$db" -Fc -f "$stage/database.dump"
pg_restore --list "$stage/database.dump" >/dev/null || die "the staged database dump is unreadable"
sync
log "staged database ready"
