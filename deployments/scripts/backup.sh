#!/bin/sh
# Backup sidecar. Modes:
#   backup.sh                 watch change-file archiving every minute and run
#                             the verified nightly cycle when one is due
#   backup.sh now             run the verified cycle immediately ("Backup now")
#   backup.sh once pre-update-<ts>
#                             verified database + files safety copy before an
#                             update; keeps only the newest safety copy
#
# The nightly cycle keeps exactly one verified set. A new set is written,
# checked (checksums, decrypt test, pg_restore --list, pg_verifybackup,
# pg_waldump, change-file chain) and recorded in manifest.json; only then is
# the previous set, and change files older than the new full copy, removed.
# Any failure leaves the previous set and every change file in place.
set -eu
umask 077
SCRIPTS=${CARE_SCRIPTS:-/scripts}
. "$SCRIPTS/backup-lib.sh"

MINIO_DIR=${CARE_MINIO_DIR:-/minio-data}
STATE_DIR=${CARE_STATE_DIR:-/state}
WORK_ROOT=${CARE_WORK_DIR:-/work}
PGDATA_MOUNT=${CARE_PGDATA_MOUNT:-/pgdata}
INTERVAL=${CARE_BACKUP_INTERVAL:-86400}
RETRY_AFTER=${CARE_BACKUP_RETRY:-3600}
TRIAL_EVERY=${CARE_TRIAL_RESTORE_INTERVAL:-604800}
ARCHIVE_TIMEOUT=${CARE_ARCHIVE_TIMEOUT:-300}
ARCHIVE_WAIT=${CARE_ARCHIVE_WAIT:-600}
TICK=${CARE_BACKUP_TICK:-60}
MANIFEST="$BACKUP_DIR/manifest.json"

RESERVE_KB="${BACKUP_MIN_FREE_KB:-0}"
case "$RESERVE_KB" in ''|*[!0-9]*)
	echo "[backup] ERROR: BACKUP_MIN_FREE_KB must be a non-negative number"
	exit 1 ;;
esac
if [ ! -f "$CERT" ]; then
	echo "[backup] ERROR: $CERT not found - all backups must be encrypted, refusing to run"
	exit 1
fi
if [ -z "${POSTGRES_PASSWORD:-}" ]; then
	echo "[backup] ERROR: POSTGRES_PASSWORD is not set - refusing to run"
	exit 1
fi
export PGPASSWORD="$POSTGRES_PASSWORD"
DB_HOST="${POSTGRES_HOST:-db}"
DB_PORT="${POSTGRES_PORT:-5432}"
DB_USER="${POSTGRES_USER:-postgres}"
DB_NAME="${POSTGRES_DB:-care}"

log() { echo "[backup] $*"; }
now() { date +%s; }
mb() { echo "$(( $1 / 1024 )) MB"; }

sql() {
	PGCONNECT_TIMEOUT=10 psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "${2:-$DB_NAME}" \
		-X -q -t -A -v ON_ERROR_STOP=1 -c "$1"
}

wait_for_database() {
	attempt=1
	until pg_isready -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t 5 >/dev/null 2>&1; do
		[ "$attempt" -ne 1 ] || log "database: waiting for PostgreSQL to accept connections"
		if [ "$attempt" -ge "${CARE_DB_WAIT_ATTEMPTS:-30}" ]; then
			log "ERROR: PostgreSQL did not become ready after $attempt checks; no backup files were changed"
			return 1
		fi
		attempt=$((attempt + 1))
		sleep 5
	done
}

free_kb() { df -Pk "$1" 2>/dev/null | awk 'NR == 2 { print $4 }'; }
kb_of() { if [ -e "$1" ]; then du -sk "$1" 2>/dev/null | cut -f1; else echo 0; fi; }
bytes_of() { wc -c <"$1" | tr -d ' '; }

# Status writes are how the app learns about backups; a failed write is
# logged and reported to the caller instead of being silently dropped.
state_dir_ok() {
	[ -d "$STATE_DIR" ] && return 0
	log "ERROR: the backup status folder $STATE_DIR is missing; status cannot be saved"
	return 1
}

write_status() {
	state_dir_ok || return 1
	tmp="$STATE_DIR/.backup-status.tmp"
	if (printf 'state=%s\nreason=%s\nat=%s\nneed_kb=%s\nfree_kb=%s\nmessage=%s\n' \
		"$1" "$2" "$(now)" "${3:-0}" "${4:-0}" "${5:-}" >"$tmp") &&
		mv "$tmp" "$STATE_DIR/backup-status"; then
		return 0
	fi
	rm -f "$tmp" 2>/dev/null || :
	log "ERROR: could not save the backup status ($1) to $STATE_DIR/backup-status"
	return 1
}

stamp_file() {
	state_dir_ok || return 1
	tmp="$STATE_DIR/.$1.tmp"
	if (now >"$tmp") && mv "$tmp" "$STATE_DIR/$1"; then
		return 0
	fi
	rm -f "$tmp" 2>/dev/null || :
	log "ERROR: could not save $STATE_DIR/$1"
	return 1
}
read_stamp() {
	v=$(cat "$STATE_DIR/$1" 2>/dev/null || true)
	case "$v" in ''|*[!0-9]*) echo 0 ;; *) echo "$v" ;; esac
}

# ---------------------------------------------------------------- estimates

cluster_kb() {
	b=$(sql "select coalesce(sum(pg_database_size(oid)), 0) from pg_database" 2>/dev/null || echo 0)
	case "$b" in ''|*[!0-9]*) b=0 ;; esac
	echo $(( b / 1024 ))
}

# space_ok: the plaintext work area must hold the full copy, dump and files
# archive at once; the backup folder must hold a second encrypted set beside
# the current one until the new set is verified.
space_ok() {
	cl=$(cluster_kb)
	files=$(kb_of "$MINIO_DIR")
	work_need=$(( (2 * cl + 2 * files) * 5 / 4 + 262144 ))
	set_need=$(( (2 * cl + files) * 5 / 4 + 262144 ))
	[ "$set_need" -ge 1048576 ] || set_need=1048576
	set_need=$(( set_need + RESERVE_KB ))
	SPACE_NEED=$set_need
	SPACE_FREE=$(free_kb "$BACKUP_DIR")
	case "$SPACE_FREE" in ''|*[!0-9]*)
		log "ERROR: could not read free space in the backup folder; no backup was started"
		SPACE_FREE=0
		return 1 ;;
	esac
	if [ "$SPACE_FREE" -lt "$set_need" ]; then
		log "ERROR: not enough space in the backup folder: this backup needs about $(mb "$set_need"), $(mb "$SPACE_FREE") is free"
		return 1
	fi
	wfree=$(free_kb "$WORK_ROOT")
	case "$wfree" in ''|*[!0-9]*) wfree=0 ;; esac
	if [ "$wfree" -lt "$work_need" ]; then
		SPACE_NEED=$work_need
		SPACE_FREE=$wfree
		log "ERROR: not enough room on the clinic's data disk to prepare a backup: needs about $(mb "$work_need"), $(mb "$wfree") is free"
		return 1
	fi
	log "space: $(mb "$SPACE_FREE") free in the backup folder, about $(mb "$set_need") needed"
}

# ---------------------------------------------------------------- row counts

COUNT_SQL="select coalesce(string_agg(format('select %L as t, count(*) as n from %I.%I', schemaname || '.' || tablename, schemaname, tablename), ' union all ' order by schemaname, tablename), 'select null, null where false') from pg_tables where schemaname not in ('pg_catalog', 'information_schema')"

table_counts() {
	q=$(sql "$COUNT_SQL" "$1") || return 1
	sql "select t || '|' || n from ($q) c order by t" "$1"
}

# counts_sane BEFORE AFTER TRIAL: every table that existed before and after the
# dump has a restored row count within the range seen during the dump, with a
# small allowance for rows added and removed in between.
counts_sane() {
	awk -F'|' '
		FILENAME == ARGV[1] { b[$1] = $2; next }
		FILENAME == ARGV[2] { a[$1] = $2; next }
		{ r[$1] = $2 }
		END {
			bad = 0
			for (t in b) {
				if (!(t in a)) continue
				lo = b[t] < a[t] ? b[t] : a[t]
				hi = b[t] < a[t] ? a[t] : b[t]
				slack = int(hi / 100)
				if (!(t in r)) { print "[backup] trial restore: table " t " is missing"; bad = 1; continue }
				if (r[t] < lo - slack || r[t] > hi + slack) {
					print "[backup] trial restore: " t " has " r[t] " rows, expected about " lo "-" hi; bad = 1
				}
			}
			exit bad
		}' "$1" "$2" "$3"
}

# ---------------------------------------------------------------- trial restore

TRIAL_STATE=not_due
TRIAL_MESSAGE=""
TRIAL_AT=0

trial_restore() {
	dump=$1 before=$2 after=$3 work=$4
	last=$(read_stamp trial-at)
	if [ "${FORCE_TRIAL:-0}" != 1 ] && [ $(( $(now) - last )) -lt "$TRIAL_EVERY" ]; then
		TRIAL_STATE=not_due
		return 0
	fi
	db_kb=$(( $(sql "select pg_database_size(current_database())" || echo 0) / 1024 ))
	margin=$(( db_kb / 5 ))
	[ "$margin" -ge 524288 ] || margin=524288
	pgfree=$(free_kb "$PGDATA_MOUNT")
	TRIAL_AT=$(now)
	case "$pgfree" in ''|*[!0-9]*)
		TRIAL_STATE=skipped
		TRIAL_MESSAGE="could not check free space on the database disk"
		log "trial restore: skipped - $TRIAL_MESSAGE"
		return 0 ;;
	esac
	if [ "$pgfree" -le $(( db_kb + margin )) ]; then
		TRIAL_STATE=skipped
		TRIAL_MESSAGE="not enough free space for a test copy (needs about $(mb $(( db_kb + margin ))), $(mb "$pgfree") free)"
		log "trial restore: skipped - $TRIAL_MESSAGE"
		return 0
	fi
	trial_db="care_trial_$(date +%Y%m%d%H%M%S)"
	log "trial restore: restoring into throwaway database $trial_db"
	status=0
	if ! sql "create database \"$trial_db\"" postgres >/dev/null; then
		status=1
	elif ! pg_restore -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$trial_db" --no-owner --exit-on-error "$dump" >"$work/trial.log" 2>&1; then
		tail -n 5 "$work/trial.log" | sed 's/^/[backup]   /'
		status=1
	elif ! table_counts "$trial_db" >"$work/trial.counts"; then
		status=1
	elif ! counts_sane "$before" "$after" "$work/trial.counts"; then
		status=1
	fi
	if ! sql "drop database if exists \"$trial_db\" with (force)" postgres >/dev/null; then
		log "WARNING: could not remove throwaway database $trial_db"
	fi
	if [ "$status" -ne 0 ]; then
		TRIAL_STATE=failed
		TRIAL_MESSAGE="the test restore did not match the clinic's data"
		log "ERROR: trial restore failed"
		return 1
	fi
	TRIAL_STATE=passed
	TRIAL_MESSAGE="restored $(wc -l <"$work/trial.counts" | tr -d ' ') tables with matching row counts"
	log "trial restore: passed"
	stamp_file trial-at || true
}

# ---------------------------------------------------------------- WAL helpers

archiver_row() {
	sql "select coalesce(last_archived_wal, ''), coalesce(extract(epoch from last_archived_time)::bigint, 0), failed_count, coalesce(last_failed_wal, ''), coalesce(extract(epoch from last_failed_time)::bigint, 0), pg_walfile_name(pg_current_wal_lsn()), (pg_walfile_name_offset(pg_current_wal_lsn())).file_offset, (select setting::bigint from pg_settings where name = 'wal_segment_size'), current_setting('archive_mode') from pg_stat_archiver"
}

# wait_archived SEGMENT: block until PostgreSQL has archived SEGMENT.
wait_archived() {
	deadline=$(( $(now) + ARCHIVE_WAIT ))
	while :; do
		if [ -f "$WAL_DIR/$1.sha256" ]; then
			return 0
		fi
		if [ "$(now)" -ge "$deadline" ]; then
			log "ERROR: change file $1 was not archived within ${ARCHIVE_WAIT}s"
			return 1
		fi
		sleep 2
	done
}

# ---------------------------------------------------------------- the cycle

NEW_PATHS=""
discard_new() {
	for p in $NEW_PATHS; do
		rm -rf "$p" || log "WARNING: could not remove unverified $p"
	done
	NEW_PATHS=""
}

# seal_publish PLAIN FINAL: encrypt with decrypt test, then atomic rename.
seal_publish() {
	tmp="$(dirname "$2")/.$(basename "$2").tmp"
	rm -f "$tmp"
	if [ -e "$2" ] || [ -L "$2" ]; then
		log "ERROR: refusing to overwrite existing backup ${2##*/}"
		return 1
	fi
	seal_verified "$1" "$tmp" || { rm -f "$tmp"; return 1; }
	sync "$tmp" 2>/dev/null || sync
	NEW_PATHS="$NEW_PATHS $2"
	mv "$tmp" "$2"
}

dump_and_files() {
	ts=$1 prefix=$2 work=$3
	log "database: dumping $DB_NAME"
	if ! pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -Fc -f "$work/care.dump"; then
		log "ERROR: pg_dump failed"
		return 1
	fi
	if ! pg_restore --list "$work/care.dump" >/dev/null 2>&1; then
		log "ERROR: dump failed verification (pg_restore --list)"
		return 1
	fi
	seal_publish "$work/care.dump" "$BACKUP_DIR/care-$prefix$ts.dump.enc" || { log "ERROR: encrypting the dump failed"; return 1; }
	log "database: wrote care-$prefix$ts.dump.enc (decrypt test passed)"

	if [ ! -d "$MINIO_DIR" ]; then
		log "ERROR: $MINIO_DIR is not mounted - refusing to call this a complete backup"
		return 1
	fi
	if ! tar -czf "$work/files.tar.gz" -C "$MINIO_DIR" . 2>"$work/tar.err"; then
		log "ERROR: archiving the files failed"
		tail -n 3 "$work/tar.err" | sed 's/^/[backup]   /'
		return 1
	fi
	if ! tar -tzf "$work/files.tar.gz" >/dev/null 2>&1; then
		log "ERROR: files archive is unreadable"
		return 1
	fi
	seal_publish "$work/files.tar.gz" "$BACKUP_DIR/files-$prefix$ts.tar.gz.enc" || { log "ERROR: encrypting the files archive failed"; return 1; }
	log "files: wrote files-$prefix$ts.tar.gz.enc (decrypt test passed)"
}

file_json() {
	jq -n --arg path "$1" --arg sha "$(sha256_of "$BACKUP_DIR/$1")" --arg plain "$2" \
		--argjson bytes "$(bytes_of "$BACKUP_DIR/$1")" \
		'{path: $path, sha256: $sha, plain_sha256: $plain, bytes: $bytes}'
}

previous_trial() {
	if [ -f "$MANIFEST" ]; then
		jq -c '.trial_restore // {state: "never"}' "$MANIFEST" 2>/dev/null || echo '{"state":"never"}'
	else
		echo '{"state":"never"}'
	fi
}

cycle() {
	ts=$(date +%Y%m%d-%H%M%S)
	started=$(now)
	log "===== verified backup $ts ====="
	write_status running "" 0 0 ""
	SPACE_NEED=0 SPACE_FREE=0 NEW_PATHS=""
	work="$WORK_ROOT/$ts"

	if ! wait_for_database; then
		write_status failed database_unavailable 0 0 "the database was not ready; the previous backup was kept"
		return 1
	fi
	if ! space_ok; then
		write_status failed disk_full "$SPACE_NEED" "$SPACE_FREE" "not enough space for a new backup; the previous backup was kept"
		return 1
	fi
	for p in "base-$ts" "care-$ts.dump.enc" "files-$ts.tar.gz.enc"; do
		if [ -e "$BACKUP_DIR/$p" ] || [ -L "$BACKUP_DIR/$p" ]; then
			log "ERROR: refusing to overwrite existing backup $p"
			write_status failed error 0 0 "a backup with this name already exists; the previous backup was kept"
			return 1
		fi
	done
	rm -rf "$work"
	mkdir -p "$work" "$WAL_DIR"
	if run_cycle "$ts" "$work" "$started"; then
		rm -rf "$work"
		if ! stamp_file backup-ok-at || ! write_status ok "" 0 "$(free_kb "$BACKUP_DIR")" ""; then
			log "verified backup $ts: published and verified, but its status could not be saved"
			return 1
		fi
		log "verified backup $ts: SUCCESS"
		return 0
	fi
	rm -rf "$work"
	if [ "$(jq -r '.set // empty' "$MANIFEST" 2>/dev/null)" = "$ts" ]; then
		# The new set is verified and recorded; only removing older copies failed.
		write_status failed error 0 "$(free_kb "$BACKUP_DIR")" "$FAIL_MESSAGE; the new verified backup was kept"
		stamp_file backup-failed-at
		log "verified backup $ts: published, but cleanup FAILED - older copies remain until the next backup"
		return 1
	fi
	discard_new
	reason=error
	free=$(free_kb "$BACKUP_DIR")
	case "$free" in ''|*[!0-9]*) ;; *) [ "$free" -ge $(( 262144 + RESERVE_KB )) ] || reason=disk_full ;; esac
	write_status failed "$reason" "$SPACE_NEED" "${free:-0}" "${FAIL_MESSAGE:-the backup could not be verified}; the previous backup was kept"
	stamp_file backup-failed-at
	log "verified backup $ts: FAILED - the previous backup and every change file were kept"
	return 1
}

fail() { FAIL_MESSAGE=$1; log "ERROR: $1"; return 1; }

run_cycle() {
	ts=$1 work=$2 started=$3
	FAIL_MESSAGE=""
	table_counts "$DB_NAME" >"$work/before.counts" || { fail "could not count table rows"; return 1; }

	log "full copy: pg_basebackup"
	if ! pg_basebackup -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -w -D "$work/base" -Fp -X stream \
		-c fast -l "care-$ts" --manifest-checksums=SHA256 >"$work/basebackup.log" 2>&1; then
		tail -n 5 "$work/basebackup.log" | sed 's/^/[backup]   /'
		fail "the full database copy failed"; return 1
	fi
	# Restores can target any moment after the copy finished (when it became consistent).
	base_ended=$(now)
	if ! pg_verifybackup -q "$work/base" >"$work/verify.log" 2>&1; then
		tail -n 5 "$work/verify.log" | sed 's/^/[backup]   /'
		fail "pg_verifybackup rejected the full copy"; return 1
	fi
	start_lsn=$(sed -n 's/^START WAL LOCATION: \([0-9A-F/]*\) .*/\1/p' "$work/base/backup_label")
	start_wal=$(sed -n 's/^START WAL LOCATION: .*(file \([0-9A-F]\{24\}\)).*/\1/p' "$work/base/backup_label")
	timeline=$(sed -n 's/^START TIMELINE: \([0-9]*\)$/\1/p' "$work/base/backup_label")
	end_lsn=$(jq -r '.["WAL-Ranges"][-1]["End-LSN"]' "$work/base/backup_manifest")
	seg_bytes=$(sql "select (select setting::bigint from pg_settings where name = 'wal_segment_size')") || seg_bytes=""
	case "$seg_bytes" in ''|*[!0-9]*) fail "could not read the change-file size"; return 1 ;; esac
	wal_segment_name "$start_wal" && [ -n "$start_lsn" ] && [ -n "$end_lsn" ] && [ -n "$timeline" ] ||
		{ fail "could not read the full copy's starting position"; return 1; }
	end_wal=$(sql "select pg_walfile_name('$end_lsn'::pg_lsn)")
	if ! pg_waldump -q -p "$work/base/pg_wal" -s "$start_lsn" -e "$end_lsn" -t "$timeline" >"$work/waldump.log" 2>&1; then
		tail -n 5 "$work/waldump.log" | sed 's/^/[backup]   /'
		fail "pg_waldump could not read the full copy's change records"; return 1
	fi
	log "full copy: verified from $start_lsn ($start_wal) to $end_lsn"

	tar -czf "$work/base.tar.gz" -C "$work/base" . || { fail "packing the full copy failed"; return 1; }
	base_plain=$(sha256_of "$work/base.tar.gz")
	rm -rf "$work/base"
	mkdir -p "$BACKUP_DIR/.base-$ts.tmp"
	NEW_PATHS="$NEW_PATHS $BACKUP_DIR/.base-$ts.tmp"
	seal_verified "$work/base.tar.gz" "$BACKUP_DIR/.base-$ts.tmp/base.tar.gz.enc" ||
		{ fail "encrypting the full copy failed"; return 1; }
	rm -f "$work/base.tar.gz"
	NEW_PATHS="$NEW_PATHS $BACKUP_DIR/base-$ts"
	mv "$BACKUP_DIR/.base-$ts.tmp" "$BACKUP_DIR/base-$ts"
	log "full copy: wrote base-$ts/base.tar.gz.enc (decrypt test passed)"

	dump_and_files "$ts" "" "$work" || { FAIL_MESSAGE=${FAIL_MESSAGE:-"the database dump or files archive failed"}; return 1; }
	dump_plain=$(sha256_of "$work/care.dump")
	files_plain=$(sha256_of "$work/files.tar.gz")
	rm -f "$work/files.tar.gz"
	table_counts "$DB_NAME" >"$work/after.counts" || { fail "could not count table rows"; return 1; }

	sql "select pg_switch_wal()" >/dev/null || true
	wait_archived "$end_wal" || { fail "the change files after the full copy were not archived"; return 1; }
	newest=$(newest_segment "$(printf '%s' "$start_wal" | cut -c1-8)")
	[ -n "$newest" ] || { fail "no archived change files were found"; return 1; }
	wal_chain_check "$start_wal" "$newest" "$seg_bytes" || { fail "archived change files have a gap"; return 1; }
	log "change files: $start_wal to $newest present and intact"

	trial_restore "$work/care.dump" "$work/before.counts" "$work/after.counts" "$work" ||
		{ fail "the weekly test restore failed"; return 1; }
	if [ "$TRIAL_STATE" = not_due ]; then
		trial=$(previous_trial)
	else
		trial=$(jq -nc --arg s "$TRIAL_STATE" --arg m "$TRIAL_MESSAGE" --argjson at "$TRIAL_AT" '{state: $s, at: $at, message: $m}')
	fi
	rm -f "$work/care.dump"

	base_file=$(file_json "base-$ts/base.tar.gz.enc" "$base_plain")
	dump_file=$(file_json "care-$ts.dump.enc" "$dump_plain")
	files_file=$(file_json "files-$ts.tar.gz.enc" "$files_plain")
	jq -n --arg set "$ts" --argjson created "$(now)" --argjson started "$started" --argjson base_ended "$base_ended" \
		--arg start_lsn "$start_lsn" --arg end_lsn "$end_lsn" --arg start_wal "$start_wal" \
		--argjson timeline "$timeline" --argjson seg "$seg_bytes" --arg newest "$newest" \
		--arg serial "$(cert_serial)" --argjson trial "$trial" \
		--argjson base "$base_file" --argjson dump "$dump_file" --argjson files "$files_file" \
		'{version: 1, set: $set, created_at: $created, started_at: $started,
		  certificate_serial: $serial,
		  base: {start_lsn: $start_lsn, end_lsn: $end_lsn, ended_at: $base_ended, start_wal: $start_wal, timeline: $timeline, wal_segment_bytes: $seg},
		  files: [$base, $dump, $files],
		  wal: {dir: "wal", from: $start_wal, through: $newest},
		  checks: {checksums: "ok", decrypt_test: "ok", pg_restore_list: "ok", pg_verifybackup: "ok", pg_waldump: "ok", wal_chain: "ok"},
		  trial_restore: $trial}' >"$BACKUP_DIR/.manifest.json.tmp" || { fail "writing the backup manifest failed"; return 1; }

	# Read every published file back against the manifest before trusting it.
	for f in $(jq -r '.files[].path' "$BACKUP_DIR/.manifest.json.tmp"); do
		want=$(jq -r --arg p "$f" '.files[] | select(.path == $p) | .sha256' "$BACKUP_DIR/.manifest.json.tmp")
		[ "$want" = "$(sha256_of "$BACKUP_DIR/$f")" ] || { fail "$f changed after it was written"; return 1; }
	done
	sync
	mv "$BACKUP_DIR/.manifest.json.tmp" "$MANIFEST"
	NEW_PATHS=""
	log "manifest: recorded verified set $ts"
	prune_previous "$ts" "$start_wal" || { FAIL_MESSAGE="the new backup is verified, but removing the previous one failed"; return 1; }
}

# prune_previous SET START_WAL: runs only after SET is verified and recorded.
prune_previous() {
	keep=$1 start=$2
	status=0
	for p in "$BACKUP_DIR"/care-[0-9]*.dump.enc "$BACKUP_DIR"/files-[0-9]*.tar.gz.enc "$BACKUP_DIR"/base-[0-9]*; do
		[ ! -L "$p" ] || continue
		case "${p##*/}" in
			care-"$keep".dump.enc | files-"$keep".tar.gz.enc | base-"$keep") continue ;;
			base-*) [ -d "$p" ] || continue ;;
			*) [ -f "$p" ] || continue ;;
		esac
		if rm -rf "$p"; then
			log "removed previous backup ${p##*/}"
		else
			log "ERROR: removing previous backup ${p##*/} failed"
			status=1
		fi
	done
	pruned=0
	for p in "$WAL_DIR"/*.sha256 "$WAL_DIR"/*.gz.enc; do
		[ -f "$p" ] && [ ! -L "$p" ] || continue
		name=${p##*/}
		name=${name%.sha256}
		name=${name%.gz.enc}
		case "$name" in *.history) continue ;; esac
		seg=$(printf '%s' "$name" | cut -c1-24)
		# Same-width hex names sort in WAL order; only files wholly before the
		# new full copy's starting segment are no longer needed.
		if [ "$(printf '%s\n%s\n' "$seg" "$start" | sort | head -n1)" = "$seg" ] && [ "$seg" != "$start" ]; then
			rm -f "$p" || status=1
			pruned=$((pruned + 1))
		fi
	done
	find "$BACKUP_DIR" -maxdepth 1 -name '.*.tmp' -exec rm -rf {} + 2>/dev/null || true
	find "$WAL_DIR" -maxdepth 1 -name '.tmp-*' -mmin +60 -exec rm -rf {} + 2>/dev/null || true
	[ "$pruned" -eq 0 ] || log "change files: removed $pruned files older than $start"
	return "$status"
}

pre_update() {
	name=$1
	ts=${name#pre-update-}
	work="$WORK_ROOT/$name"
	NEW_PATHS="" FAIL_MESSAGE=""
	wait_for_database || return 1
	space_ok || { log "safety backup $name: FAILED - not enough space"; return 1; }
	for p in "care-pre-update-$ts.dump.enc" "files-pre-update-$ts.tar.gz.enc"; do
		if [ -e "$BACKUP_DIR/$p" ] || [ -L "$BACKUP_DIR/$p" ]; then
			log "ERROR: refusing to overwrite existing backup $p"
			return 1
		fi
	done
	rm -rf "$work"
	mkdir -p "$work"
	if ! dump_and_files "$ts" "pre-update-" "$work"; then
		rm -rf "$work"
		discard_new
		log "safety backup $name: FAILED"
		return 1
	fi
	rm -rf "$work"
	for p in "$BACKUP_DIR"/care-pre-update-*.dump.enc "$BACKUP_DIR"/files-pre-update-*.tar.gz.enc; do
		[ -e "$p" ] || continue
		case "${p##*/}" in *"-$ts".dump.enc | *"-$ts".tar.gz.enc) continue ;; esac
		rm -f "$p" && log "removed previous safety backup ${p##*/}"
	done
	log "safety backup $name: SUCCESS"
}

# ---------------------------------------------------------------- status loop

# wal_status records how far the clinic's changes are safely archived:
# saved_through is the last moment nothing was waiting to be archived.
wal_status() {
	state_dir_ok || return 1
	row=$(archiver_row 2>/dev/null) || { write_wal_status unknown 0 0 0 "" "the database is not reachable"; return; }
	IFS='|' read -r last_wal last_at failed_count failed_wal failed_at cur_wal cur_off seg_bytes mode <<ROW
$row
ROW
	last_at=${last_at:-0} failed_count=${failed_count:-0} failed_at=${failed_at:-0}
	cur_off=${cur_off:-0} seg_bytes=${seg_bytes:-16777216} mode=${mode:-off}
	if [ "$mode" != on ]; then
		write_wal_status off 0 "$last_at" "$failed_at" "" "change-file archiving is switched off"
		return
	fi
	t=$(now)
	clean_at=$(read_stamp wal-clean-at)
	pending_since=$(read_stamp wal-pending-since)
	per=$(segments_per_log "$seg_bytes")
	caught_up=0
	if [ -n "$cur_wal" ]; then
		cur_n=$(segment_number "$cur_wal" "$per")
		if [ "$cur_off" -eq 0 ]; then
			prev=$(segment_name "$(printf '%s' "$cur_wal" | cut -c1-8)" $((cur_n - 1)) "$per")
		else
			prev=""
		fi
		{ [ "$cur_off" -eq 0 ] && { [ "$last_wal" = "$prev" ] || [ "$last_wal" = "$cur_wal" ]; }; } && caught_up=1
		[ "$cur_off" -eq 0 ] && [ -z "$last_wal" ] && [ "$cur_n" -le 1 ] && caught_up=1
	fi
	if [ "$caught_up" -eq 1 ]; then
		stamp_file wal-clean-at || return 1
		rm -f "$STATE_DIR/wal-pending-since"
		clean_at=$t
		pending_since=0
	elif [ "$pending_since" -eq 0 ]; then
		stamp_file wal-pending-since || return 1
		pending_since=$t
	fi
	saved=$clean_at
	[ "$last_at" -le "$saved" ] || saved=$last_at
	state=ok message=""
	if [ "$failed_at" -gt "$last_at" ]; then
		state=failing
		message="PostgreSQL could not archive $failed_wal ($failed_count failures so far)"
	elif [ "$pending_since" -gt 0 ] && [ $(( t - pending_since )) -gt $(( ARCHIVE_TIMEOUT + 300 )) ]; then
		state=stale
		message="changes have been waiting to be archived for over $(( (t - pending_since) / 60 )) minutes"
	fi
	gap=""
	if [ -f "$MANIFEST" ]; then
		from=$(jq -r '.base.start_wal // empty' "$MANIFEST" 2>/dev/null || true)
		if [ -n "$from" ]; then
			gap=$(quick_gap "$from" "$per")
		fi
	fi
	write_wal_status "$state" "$saved" "$last_at" "$failed_at" "$gap" "$message"
}

# quick_gap FROM PER: first missing segment name between FROM and the newest
# archived one, by name only (checksums are checked by the nightly cycle).
quick_gap() {
	tl=$(printf '%s' "$1" | cut -c1-8)
	newest=$(newest_segment "$tl")
	[ -n "$newest" ] || return 0
	n=$(segment_number "$1" "$2")
	last=$(segment_number "$newest" "$2")
	while [ "$n" -le "$last" ]; do
		name=$(segment_name "$tl" "$n" "$2")
		if [ ! -f "$WAL_DIR/$name.sha256" ] || [ ! -f "$WAL_DIR/$name.gz.enc" ]; then
			echo "$name"
			return 0
		fi
		n=$((n + 1))
	done
}

write_wal_status() {
	state_dir_ok || return 1
	tmp="$STATE_DIR/.wal-status.tmp"
	(printf 'state=%s\nchecked_at=%s\nsaved_through=%s\nlast_archived_at=%s\nlast_failed_at=%s\ngap=%s\nmessage=%s\n' \
		"$1" "$(now)" "$2" "$3" "$4" "$5" "$6" >"$tmp") && mv "$tmp" "$STATE_DIR/wal-status" && return 0
	rm -f "$tmp" 2>/dev/null || :
	log "ERROR: could not save the change-file status to $STATE_DIR/wal-status"
	return 1
}

due() {
	ok=$(read_stamp backup-ok-at)
	if [ "$ok" -eq 0 ] && [ -f "$MANIFEST" ]; then
		ok=$(jq -r '.created_at // 0' "$MANIFEST" 2>/dev/null || echo 0)
	fi
	failed=$(read_stamp backup-failed-at)
	t=$(now)
	[ $(( t - ok )) -ge "$INTERVAL" ] && [ $(( t - failed )) -ge "$RETRY_AFTER" ]
}

with_backup_lock() (
	if ! flock -x 9; then
		log "ERROR: acquiring the backup lock failed"
		return 1
	fi
	"$@"
) 9>"$BACKUP_DIR/.backup.lock"

case "${1:-}" in
	now)
		with_backup_lock cycle
		exit ;;
	once)
		name=${2:?no backup name given}
		case "$name" in pre-update-[0-9]*) ;; *) echo "[backup] ERROR: unknown one-shot backup $name" >&2; exit 2 ;; esac
		with_backup_lock pre_update "$name"
		exit ;;
	"") ;;
	*) echo "[backup] ERROR: unknown mode $1" >&2; exit 2 ;;
esac

log "sidecar started; verified encrypted backups -> $BACKUP_DIR (one set kept, change files archived continuously)"
while :; do
	wal_status || log "ERROR: change-file status was not updated - see errors above"
	if due; then
		with_backup_lock cycle || log "backup cycle failed - see errors above"
		wal_status || log "ERROR: change-file status was not updated - see errors above"
	fi
	sleep "$TICK"
done
