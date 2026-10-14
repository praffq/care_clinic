#!/bin/sh
# Entry point for the db service. Prepares the change-file (WAL) archive folder
# for the postgres user, then hands over to the official image entry point with
# continuous archiving switched on.
set -eu
WAL_DIR=${CARE_WAL_DIR:-/backups/wal}
mkdir -p "$WAL_DIR"
chown postgres:postgres "$WAL_DIR" 2>/dev/null || true
chmod 700 "$WAL_DIR" 2>/dev/null || true
if ! gosu postgres test -w "$WAL_DIR"; then
	echo "[db] ERROR: PostgreSQL cannot write to $WAL_DIR; change files would pile up unarchived" >&2
	exit 1
fi
exec docker-entrypoint.sh postgres \
	-c hba_file=/scripts/pg_hba.conf \
	-c wal_level=replica \
	-c archive_mode=on \
	-c archive_timeout=300 \
	-c "archive_command=/bin/sh /scripts/wal-archive.sh %p %f" \
	"$@"
