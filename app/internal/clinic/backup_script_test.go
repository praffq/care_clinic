package clinic

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The shell scripts run for real against real openssl/jq; only PostgreSQL
// programs, df, flock and the clock are faked.
const fakeBackupTools = `#!/bin/sh
name=${0##*/}
printf '%s %s\n' "$name" "$*" >> "$FAKE_ROOT/trace"
fails() { case " $FAKE_FAIL " in *" $1 "*) return 0 ;; esac; return 1; }
case "$name" in
flock)
	fails lock && exit 1
	if [ -n "${FAKE_REAL_FLOCK:-}" ]; then exec "$FAKE_REAL_FLOCK" "$@"; fi
	exit 0 ;;
date)
	if [ "${1:-}" = "+%Y%m%d-%H%M%S" ]; then echo "$FAKE_TS"; exit 0; fi
	exec /bin/date "$@" ;;
df)
	free=$FAKE_FREE_KB
	case "$*" in *pgdata*) free=$FAKE_PGDATA_FREE_KB ;; */work*) free=${FAKE_WORK_FREE_KB:-99999999} ;; esac
	printf 'Filesystem 1024-blocks Used Available Capacity Mounted\nfake 99999999 1 %s 1%% /\n' "$free" ;;
sleep)
	if [ "$1" = "${CARE_BACKUP_TICK:-}" ]; then
		if [ -n "${FAKE_SLEEP_WAIT:-}" ]; then
			: > "$FAKE_ROOT/sleeping"
			while [ ! -f "$FAKE_ROOT/wake" ]; do /bin/sleep 0.05; done
		fi
		kill -TERM "$PPID"
		exit 0
	fi
	case "$1" in *[!0-9]*) exec /bin/sleep "$1" ;; esac
	exec /bin/sleep 0.01 ;;
pg_isready)
	printf '%s\n' "$*" > "$FAKE_ROOT/readiness-args"
	fails database && exit 1
	echo ready >> "$FAKE_ROOT/ready"
	[ "$(wc -l < "$FAKE_ROOT/ready")" -gt "${FAKE_READY_AFTER:-0}" ] || exit 1
	exit 0 ;;
pg_basebackup)
	fails basebackup && exit 1
	while [ "$1" != -D ]; do shift; done
	mkdir -p "$2/pg_wal"
	printf 'START WAL LOCATION: 0/%s (file %s)\nSTART TIMELINE: 1\n' "$FAKE_START_LSN" "$FAKE_START_WAL" > "$2/backup_label"
	printf '{"WAL-Ranges":[{"Timeline":1,"Start-LSN":"0/%s","End-LSN":"0/%s"}]}\n' "$FAKE_START_LSN" "$FAKE_END_LSN" > "$2/backup_manifest"
	echo data > "$2/PG_VERSION" ;;
pg_verifybackup) fails verify && exit 1; exit 0 ;;
pg_waldump) fails waldump && exit 1; exit 0 ;;
pg_dump)
	fails dump && exit 1
	if [ -n "${FAKE_HOLD:-}" ]; then
		: > "$FAKE_ROOT/holding"
		while [ ! -f "$FAKE_ROOT/release" ]; do /bin/sleep 0.05; done
	fi
	while [ "$1" != -f ]; do shift; done
	echo "dump of $FAKE_TS" > "$2" ;;
pg_restore)
	case "$*" in *--list*) fails list && exit 1; exit 0 ;; esac
	fails trial && exit 1; exit 0 ;;
gosu) shift; exec "$@" ;;
chown) exit 0 ;;
pg_ctl)
	case "$*" in
	*" start"*)
		while [ "$1" != -l ]; do shift; done
		printf '%s\n' "${FAKE_RECOVERY_LOG:-}" > "$2" ;;
	esac
	exit 0 ;;
psql)
	case "$*" in
	*pg_is_in_recovery*) echo f; exit 0 ;;
	*pg_last_xact_replay_timestamp*) echo "2026-01-02 03:10:00+00"; exit 0 ;;
	*"-XtAc select 1") echo 1; exit 0 ;;
	esac
	while [ "$#" -gt 0 ] && [ "$1" != -c ]; do
		[ "$1" = -d ] && { shift; db=$1; }
		shift
	done
	q=$2
	case "$q" in
	*"sum(pg_database_size"*) echo "${FAKE_CLUSTER_BYTES:-1048576}" ;;
	*"pg_database_size(current_database())"*) echo 1048576 ;;
	*string_agg*) echo "select 1" ;;
	*"t || '|' || n"*)
		if [ "${db:-}" != care ]; then echo "public.patients|$FAKE_TRIAL_ROWS"; else echo "public.patients|10"; fi ;;
	*wal_segment_size*) echo 16777216 ;;
	*"pg_walfile_name('"*) echo "$FAKE_END_WAL" ;;
	*pg_switch_wal*) echo 0/0 ;;
	*) : ;;
	esac ;;
esac
`

type scriptFixture struct {
	root, scripts, backups, wal, state, bin, cert, key string
	env                                                []string
}

func newScriptFixture(t *testing.T) *scriptFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("backup scripts run inside Linux containers")
	}
	for _, tool := range []string{"sh", "openssl", "jq", "sha256sum", "gzip", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	root := t.TempDir()
	scripts, err := filepath.Abs(filepath.Join("..", "..", "..", "deployments", "scripts"))
	if err != nil {
		t.Fatal(err)
	}
	f := &scriptFixture{root: root, scripts: scripts, backups: filepath.Join(root, "backups"),
		state: filepath.Join(root, "state"), bin: filepath.Join(root, "bin"),
		cert: filepath.Join(root, "keys", "backup-cert.pem"), key: filepath.Join(root, "recovery.pem")}
	f.wal = filepath.Join(f.backups, "wal")
	for _, d := range []string{f.wal, f.state, f.bin, filepath.Dir(f.cert), filepath.Join(root, "work"),
		filepath.Join(root, "shm"), filepath.Join(root, "pgdata"), filepath.Join(root, "minio", "patient-bucket")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "minio", "patient-bucket", "scan.pdf"), []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", f.key,
		"-out", f.cert, "-subj", "/CN=clinic", "-days", "30").CombinedOutput()
	if err != nil {
		t.Fatalf("certificate: %v\n%s", err, out)
	}
	for _, tool := range []string{"flock", "date", "df", "sleep", "pg_isready", "pg_basebackup", "pg_verifybackup",
		"pg_waldump", "pg_dump", "pg_restore", "psql"} {
		if err := os.WriteFile(filepath.Join(f.bin, tool), []byte(fakeBackupTools), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.env = append(os.Environ(),
		"PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_ROOT="+root, "FAKE_FAIL=", "FAKE_TS=20260102-030405",
		"FAKE_FREE_KB=99999999", "FAKE_PGDATA_FREE_KB=99999999", "FAKE_TRIAL_ROWS=10",
		"FAKE_START_LSN=4000028", "FAKE_END_LSN=4000120",
		"FAKE_START_WAL="+seg(4), "FAKE_END_WAL="+seg(4),
		"CARE_SCRIPTS="+scripts, "CARE_BACKUP_DIR="+f.backups, "CARE_WAL_DIR="+f.wal,
		"CARE_BACKUP_CERT="+f.cert, "CARE_EPHEMERAL_DIR="+filepath.Join(root, "shm"),
		"CARE_STATE_DIR="+f.state, "CARE_WORK_DIR="+filepath.Join(root, "work"),
		"CARE_PGDATA_MOUNT="+filepath.Join(root, "pgdata"), "CARE_MINIO_DIR="+filepath.Join(root, "minio"),
		"CARE_ARCHIVE_WAIT=1", "CARE_DB_WAIT_ATTEMPTS=2", "POSTGRES_PASSWORD=test", "POSTGRES_DB=care", "BACKUP_MIN_FREE_KB=0")
	return f
}

func seg(n int) string { return fmt.Sprintf("00000001%08X%08X", n/256, n%256) }

func (f *scriptFixture) run(t *testing.T, env []string, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(f.scripts, script)}, args...)...)
	cmd.Env = append(append([]string{}, f.env...), env...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// archive publishes a fake WAL segment through the real archive script.
func (f *scriptFixture) archive(t *testing.T, name string) []byte {
	t.Helper()
	data := make([]byte, 4096)
	_, _ = rand.Read(data)
	src := filepath.Join(f.root, "pg_wal", name)
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := f.run(t, nil, "wal-archive.sh", src, name); err != nil {
		t.Fatalf("archive %s: %v\n%s", name, err, out)
	}
	return data
}

func (f *scriptFixture) decrypt(t *testing.T, path string) []byte {
	t.Helper()
	out, err := exec.Command("openssl", "cms", "-decrypt", "-binary", "-inform", "DER", "-in", path, "-inkey", f.key).Output()
	if err != nil {
		t.Fatalf("decrypt %s with the clinic recovery key: %v", path, err)
	}
	return out
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() == ".backup.lock" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestWALArchiveEncryptsVerifiesAndPublishesAtomically(t *testing.T) {
	f := newScriptFixture(t)
	data := f.archive(t, seg(1))
	sums, err := os.ReadFile(filepath.Join(f.wal, seg(1)+".sha256"))
	if err != nil || !strings.Contains(string(sums), "waldump=ok") {
		t.Fatalf("checksum file: %q, %v", sums, err)
	}
	if got := gunzip(t, f.decrypt(t, filepath.Join(f.wal, seg(1)+".gz.enc"))); !bytes.Equal(got, data) {
		t.Fatal("archived change file does not decrypt to the original")
	}
	if names := listDir(t, f.wal); len(names) != 2 {
		t.Fatalf("temporary files left behind: %v", names)
	}
	if names := listDir(t, filepath.Join(f.root, "shm")); len(names) != 0 {
		t.Fatalf("single-use verification key was not destroyed: %v", names)
	}
	// PostgreSQL retries after a crash: identical contents succeed again.
	src := filepath.Join(f.root, "pg_wal", seg(1))
	if out, err := f.run(t, nil, "wal-archive.sh", src, seg(1)); err != nil {
		t.Fatalf("identical retry: %v\n%s", err, out)
	}
	// Different contents under a published name must never overwrite it.
	if err := os.WriteFile(src, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := f.run(t, nil, "wal-archive.sh", src, seg(1)); err == nil {
		t.Fatalf("conflicting archive accepted:\n%s", out)
	}
	if got := gunzip(t, f.decrypt(t, filepath.Join(f.wal, seg(1)+".gz.enc"))); !bytes.Equal(got, data) {
		t.Fatal("conflicting archive replaced the original")
	}
}

func TestWALArchiveRepairsCrashBetweenPublishSteps(t *testing.T) {
	f := newScriptFixture(t)
	src := filepath.Join(f.root, "pg_wal", seg(2))
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("segment two"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The ciphertext was renamed into place but the process died before the
	// checksum file followed it.
	if err := os.WriteFile(filepath.Join(f.wal, seg(2)+".gz.enc"), []byte("torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := f.run(t, nil, "wal-archive.sh", src, seg(2)); err != nil {
		t.Fatalf("orphan blocked archiving: %v\n%s", err, out)
	}
	if got := gunzip(t, f.decrypt(t, filepath.Join(f.wal, seg(2)+".gz.enc"))); string(got) != "segment two" {
		t.Fatalf("orphan was not replaced: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.wal, seg(2)+".sha256")); err != nil {
		t.Fatal(err)
	}
}

func TestWALArchiveRefusesUnsafeInput(t *testing.T) {
	f := newScriptFixture(t)
	src := filepath.Join(f.root, "segment")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, fail string }{
		{"../../etc/passwd", ""}, {"00000001000000000000000Z", ""}, {seg(3), "waldump"},
	} {
		if out, err := f.run(t, []string{"FAKE_FAIL=" + tc.fail}, "wal-archive.sh", src, tc.name); err == nil {
			t.Fatalf("%s accepted:\n%s", tc.name, out)
		}
	}
	if err := os.Remove(f.cert); err != nil {
		t.Fatal(err)
	}
	if out, err := f.run(t, nil, "wal-archive.sh", src, seg(3)); err == nil {
		t.Fatalf("archived without the clinic certificate:\n%s", out)
	}
	if names := listDir(t, f.wal); len(names) != 0 {
		t.Fatalf("refused archives left files: %v", names)
	}
}

func TestWALRestoreDecryptsOnlyIntactFiles(t *testing.T) {
	f := newScriptFixture(t)
	data := f.archive(t, seg(5))
	env := []string{"CARE_RECOVERY_KEY=" + f.key}
	dest := filepath.Join(f.root, "restored")
	if out, err := f.run(t, env, "wal-restore.sh", seg(5), dest); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("restored change file differs")
	}
	if _, err := f.run(t, env, "wal-restore.sh", seg(6), filepath.Join(f.root, "missing")); err == nil {
		t.Fatal("missing change file reported as restored")
	}
	enc := filepath.Join(f.wal, seg(5)+".gz.enc")
	b, _ := os.ReadFile(enc)
	b[len(b)-1] ^= 1
	if err := os.WriteFile(enc, b, 0o600); err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(f.root, "damaged")
	if _, err := f.run(t, env, "wal-restore.sh", seg(5), damaged); err == nil {
		t.Fatal("damaged change file restored")
	}
	if _, err := os.Stat(damaged); !os.IsNotExist(err) {
		t.Fatal("damaged change file was written for PostgreSQL")
	}
}

type manifestFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	PlainSHA256 string `json:"plain_sha256"`
}

type scriptManifest struct {
	Set   string         `json:"set"`
	Files []manifestFile `json:"files"`
	Base  struct {
		StartWAL string `json:"start_wal"`
	} `json:"base"`
	Checks map[string]string `json:"checks"`
	Trial  struct {
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"trial_restore"`
}

func (f *scriptFixture) manifest(t *testing.T) scriptManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.backups, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m scriptManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// previousSet runs one good cycle at segment 2 so later cycles have a set to
// keep or replace, and archives segments 1-6 plus a timeline history file.
func (f *scriptFixture) previousSet(t *testing.T) {
	t.Helper()
	for n := 1; n <= 6; n++ {
		f.archive(t, seg(n))
	}
	f.archive(t, "00000002.history")
	env := []string{"FAKE_TS=20260101-030405", "FAKE_START_WAL=" + seg(2), "FAKE_END_WAL=" + seg(2), "FAKE_START_LSN=2000028"}
	if out, err := f.run(t, env, "backup.sh", "now"); err != nil {
		t.Fatalf("first cycle: %v\n%s", err, out)
	}
}

func TestBackupCycleReplacesPreviousOnlyAfterVerification(t *testing.T) {
	f := newScriptFixture(t)
	f.previousSet(t)
	if m := f.manifest(t); m.Set != "20260101-030405" || m.Trial.State != "passed" {
		t.Fatalf("first manifest: %+v", m)
	}
	out, err := f.run(t, nil, "backup.sh", "now")
	if err != nil {
		t.Fatalf("cycle: %v\n%s", err, out)
	}
	m := f.manifest(t)
	if m.Set != "20260102-030405" || m.Base.StartWAL != seg(4) || len(m.Files) != 3 {
		t.Fatalf("manifest: %+v", m)
	}
	for _, check := range []string{"checksums", "decrypt_test", "pg_restore_list", "pg_verifybackup", "pg_waldump", "wal_chain"} {
		if m.Checks[check] != "ok" {
			t.Fatalf("check %s not recorded: %+v", check, m.Checks)
		}
	}
	for _, file := range m.Files {
		plain := f.decrypt(t, filepath.Join(f.backups, file.Path))
		if file.PlainSHA256 == "" || len(plain) == 0 {
			t.Fatalf("%s is not decryptable with the recovery key", file.Path)
		}
	}
	want := []string{"base-20260102-030405", "care-20260102-030405.dump.enc", "files-20260102-030405.tar.gz.enc", "manifest.json", "wal"}
	if got := listDir(t, f.backups); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("backup folder = %v, want only the newest set %v", got, want)
	}
	var wal []string
	for _, name := range listDir(t, f.wal) {
		if strings.HasSuffix(name, ".sha256") {
			wal = append(wal, strings.TrimSuffix(name, ".sha256"))
		}
	}
	if strings.Join(wal, ",") != strings.Join([]string{seg(4), seg(5), seg(6), "00000002.history"}, ",") {
		t.Fatalf("change files kept = %v; only those from the new copy's start must remain", wal)
	}
	if status, _ := os.ReadFile(filepath.Join(f.state, "backup-status")); !strings.Contains(string(status), "state=ok") {
		t.Fatalf("status: %s", status)
	}
	if strings.Index(out, "manifest: recorded") > strings.Index(out, "removed previous backup") {
		t.Fatalf("previous set removed before the new one was recorded:\n%s", out)
	}
}

func TestBackupCycleFailureKeepsPreviousSetAndAllChangeFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		pre  func(*scriptFixture)
	}{
		{"database unavailable", []string{"FAKE_FAIL=database"}, nil},
		{"base backup", []string{"FAKE_FAIL=basebackup"}, nil},
		{"pg_verifybackup", []string{"FAKE_FAIL=verify"}, nil},
		{"pg_waldump", []string{"FAKE_FAIL=waldump"}, nil},
		{"pg_dump", []string{"FAKE_FAIL=dump"}, nil},
		{"pg_restore list", []string{"FAKE_FAIL=list"}, nil},
		{"change file never archived", []string{"FAKE_END_WAL=" + seg(9)}, nil},
		{"trial restore rows", []string{"FAKE_TRIAL_ROWS=9"}, func(f *scriptFixture) { _ = os.Remove(filepath.Join(f.state, "trial-at")) }},
		{"backup folder full", []string{"FAKE_FREE_KB=10"}, nil},
		{"gap in change files", nil, func(f *scriptFixture) { _ = os.Remove(filepath.Join(f.wal, seg(5)+".sha256")) }},
		{"damaged change file", nil, func(f *scriptFixture) {
			_ = os.WriteFile(filepath.Join(f.wal, seg(5)+".gz.enc"), []byte("damaged"), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScriptFixture(t)
			f.previousSet(t)
			if tc.pre != nil {
				tc.pre(f)
			}
			before := listDir(t, f.backups)
			walBefore := listDir(t, f.wal)
			manifest, _ := os.ReadFile(filepath.Join(f.backups, "manifest.json"))
			out, err := f.run(t, tc.env, "backup.sh", "now")
			if err == nil {
				t.Fatalf("failure reported as success:\n%s", out)
			}
			if got := listDir(t, f.backups); strings.Join(got, ",") != strings.Join(before, ",") {
				t.Fatalf("backup folder changed after failure: %v -> %v\n%s", before, got, out)
			}
			if got := listDir(t, f.wal); strings.Join(got, ",") != strings.Join(walBefore, ",") {
				t.Fatalf("change files changed after failure: %v -> %v", walBefore, got)
			}
			if now, _ := os.ReadFile(filepath.Join(f.backups, "manifest.json")); !bytes.Equal(now, manifest) {
				t.Fatal("manifest changed after failure")
			}
			status, _ := os.ReadFile(filepath.Join(f.state, "backup-status"))
			if !strings.Contains(string(status), "state=failed") || !strings.Contains(string(status), "previous backup was kept") {
				t.Fatalf("failure not alerted: %s", status)
			}
		})
	}
}

func TestTrialRestoreIsSkippedWithNoticeWhenSpaceIsShort(t *testing.T) {
	f := newScriptFixture(t)
	for n := 4; n <= 4; n++ {
		f.archive(t, seg(n))
	}
	out, err := f.run(t, []string{"FAKE_PGDATA_FREE_KB=1000"}, "backup.sh", "now")
	if err != nil {
		t.Fatalf("cycle: %v\n%s", err, out)
	}
	m := f.manifest(t)
	if m.Trial.State != "skipped" || !strings.Contains(m.Trial.Message, "not enough free space") {
		t.Fatalf("trial = %+v", m.Trial)
	}
	if strings.Contains(readTrace(t, f), "create database") {
		t.Fatal("trial database created without enough space")
	}
}

func TestTrialRestoreRunsWeeklyAndDropsThrowawayDatabase(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	if out, err := f.run(t, nil, "backup.sh", "now"); err != nil {
		t.Fatalf("cycle: %v\n%s", err, out)
	}
	trace := readTrace(t, f)
	if !strings.Contains(trace, "create database \"care_trial_") || !strings.Contains(trace, "drop database if exists \"care_trial_") {
		t.Fatalf("trial restore did not create and drop a throwaway database:\n%s", trace)
	}
	if f.manifest(t).Trial.State != "passed" {
		t.Fatal("trial result not recorded")
	}
	_ = os.Remove(filepath.Join(f.root, "trace"))
	if out, err := f.run(t, []string{"FAKE_TS=20260103-030405"}, "backup.sh", "now"); err != nil {
		t.Fatalf("second cycle: %v\n%s", err, out)
	}
	if strings.Contains(readTrace(t, f), "create database") {
		t.Fatal("trial restore repeated within a week")
	}
	if f.manifest(t).Trial.State != "passed" {
		t.Fatal("previous trial result dropped")
	}
}

func readTrace(t *testing.T, f *scriptFixture) string {
	b, _ := os.ReadFile(filepath.Join(f.root, "trace"))
	return string(b)
}

func TestPreUpdateBackupKeepsOnlyNewestSafetyCopy(t *testing.T) {
	f := newScriptFixture(t)
	f.previousSet(t)
	for _, ts := range []string{"20260102-010101", "20260102-020202"} {
		if out, err := f.run(t, nil, "backup.sh", "once", "pre-update-"+ts); err != nil {
			t.Fatalf("pre-update: %v\n%s", err, out)
		}
	}
	got := strings.Join(listDir(t, f.backups), ",")
	for _, want := range []string{"care-pre-update-20260102-020202.dump.enc", "files-pre-update-20260102-020202.tar.gz.enc", "care-20260101-030405.dump.enc"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "010101") {
		t.Fatalf("older safety copy kept: %s", got)
	}
	if out, err := f.run(t, nil, "backup.sh", "once", "../evil"); err == nil {
		t.Fatalf("unsafe one-shot name accepted:\n%s", out)
	}
}

func TestBackupReserveRefusesBeforeWriting(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	reserve := backupReserveKB("windows")
	out, err := f.run(t, []string{"BACKUP_MIN_FREE_KB=" + reserve, "FAKE_FREE_KB=" + reserve}, "backup.sh", "now")
	if err == nil || strings.Contains(readTrace(t, f), "pg_basebackup") {
		t.Fatalf("backup started below the reserve:\n%s", out)
	}
	status, _ := os.ReadFile(filepath.Join(f.state, "backup-status"))
	if !strings.Contains(string(status), "reason=disk_full") {
		t.Fatalf("status: %s", status)
	}
}

func (f *scriptFixture) status(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state, "backup-status"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// snapshot records every path and its contents under the backup folder so a
// failed run can be proven to have changed nothing.
func (f *scriptFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(f.backups, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.backups, p)
		if rel == ".backup.lock" {
			return nil
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "link:" + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed or was removed", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Fatalf("%s was left behind", k)
		}
	}
}

// daemon runs the scheduler for exactly one tick; the fake sleep ends it.
func (f *scriptFixture) daemon(t *testing.T, env ...string) string {
	t.Helper()
	out, _ := f.run(t, append([]string{"CARE_BACKUP_TICK=7", "CARE_BACKUP_INTERVAL=0", "CARE_BACKUP_RETRY=0"}, env...), "backup.sh")
	return out
}

func TestBackupScriptScheduledFailures(t *testing.T) {
	for _, tc := range []struct {
		name, fail, message string
	}{
		{"complete set", "", "SUCCESS"},
		{"database dump", "dump", "pg_dump failed"},
		{"dump verification", "list", "pg_restore --list"},
		{"full copy verification", "verify", "pg_verifybackup rejected the full copy"},
		{"lock acquisition", "lock", "acquiring the backup lock failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScriptFixture(t)
			f.previousSet(t)
			_ = os.Remove(filepath.Join(f.root, "trace"))
			before := f.snapshot(t)
			out := f.daemon(t, "FAKE_FAIL="+tc.fail)
			if !strings.Contains(out, tc.message) {
				t.Fatalf("missing %q:\n%s", tc.message, out)
			}
			if _, err := os.Stat(filepath.Join(f.state, "wal-status")); err != nil {
				t.Fatalf("scheduler did not record change-file status: %v", err)
			}
			if tc.fail == "" {
				if m := f.manifest(t); m.Set != "20260102-030405" || !strings.Contains(f.status(t), "state=ok\n") {
					t.Fatalf("scheduled success not recorded: %+v\n%s", m, f.status(t))
				}
				return
			}
			if strings.Contains(out, ": SUCCESS") || !strings.Contains(out, "backup cycle failed") {
				t.Fatalf("incomplete backup reported success:\n%s", out)
			}
			sameSnapshot(t, before, f.snapshot(t))
			if tc.fail == "lock" {
				if strings.Contains(readTrace(t, f), "pg_basebackup ") {
					t.Fatal("backup ran without the lock")
				}
				return
			}
			if s := f.status(t); !strings.Contains(s, "state=failed\nreason=error\n") || !strings.Contains(s, "previous backup was kept") {
				t.Fatalf("status: %s", s)
			}
			if _, err := os.Stat(filepath.Join(f.state, "backup-failed-at")); err != nil {
				t.Fatal("failed attempt not stamped for retry back-off")
			}
		})
	}
}

func TestBackupScriptWaitsForDatabaseBeforeDumping(t *testing.T) {
	for _, mode := range [][]string{{"now"}, {"once", "pre-update-20260102-030405"}} {
		t.Run(mode[0], func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			out, err := f.run(t, []string{"FAKE_READY_AFTER=3", "CARE_DB_WAIT_ATTEMPTS=10",
				"POSTGRES_HOST=clinic-db", "POSTGRES_PORT=5544", "POSTGRES_USER=care-owner", "POSTGRES_DB=care-test"}, "backup.sh", mode...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			trace := readTrace(t, f)
			if strings.Count(trace, "pg_isready ") != 4 || strings.Index(trace, "pg_dump ") < strings.LastIndex(trace, "pg_isready ") {
				t.Fatalf("did not wait for database readiness:\n%s", trace)
			}
			if !strings.Contains(out, "waiting for PostgreSQL") || !strings.Contains(out, ": SUCCESS") || strings.Contains(out, "FAILED") {
				t.Fatalf("startup wait reported as a failure:\n%s", out)
			}
			probe, _ := os.ReadFile(filepath.Join(f.root, "readiness-args"))
			if string(probe) != "-h clinic-db -p 5544 -U care-owner -d care-test -t 5\n" {
				t.Fatalf("readiness probe did not use the backup database settings: %q", probe)
			}
		})
	}
}

func TestBackupScriptDatabaseWaitIsBoundedAndPreservesSavedBackups(t *testing.T) {
	for _, mode := range [][]string{{"now"}, {"once", "pre-update-20260102-030405"}} {
		t.Run(mode[0], func(t *testing.T) {
			f := newScriptFixture(t)
			f.previousSet(t)
			statusBefore := f.status(t)
			if err := os.Remove(filepath.Join(f.root, "trace")); err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			out, err := f.run(t, []string{"FAKE_FAIL=database", "CARE_DB_WAIT_ATTEMPTS=4"}, "backup.sh", mode...)
			if err == nil || !strings.Contains(out, "PostgreSQL did not become ready") || strings.Contains(out, ": SUCCESS") {
				t.Fatalf("missing database readiness failure: %v\n%s", err, out)
			}
			trace := readTrace(t, f)
			if n := strings.Count(trace, "pg_isready "); n != 4 {
				t.Fatalf("readiness probes = %d, want 4", n)
			}
			for _, cmd := range []string{"pg_basebackup", "pg_dump", "pg_restore", "psql"} {
				if strings.Contains(trace, cmd+" ") {
					t.Fatalf("unavailable database reached %s:\n%s", cmd, trace)
				}
			}
			sameSnapshot(t, before, f.snapshot(t))
			status := f.status(t)
			if mode[0] == "once" {
				if status != statusBefore {
					t.Fatalf("failed safety backup changed the scheduled status: %s", status)
				}
			} else if !strings.Contains(status, "state=failed\nreason=database_unavailable\n") || !strings.Contains(status, "previous backup was kept") {
				t.Fatalf("scheduled failure lost its database-specific reason: %s", status)
			}
		})
	}
}

func TestBackupScriptManualIncludesFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		ok   bool
	}{
		{"complete", nil, true},
		{"dump fails", []string{"FAKE_FAIL=dump"}, false},
		{"dump unreadable", []string{"FAKE_FAIL=list"}, false},
		{"files folder missing", []string{"CARE_MINIO_DIR=/nonexistent-care-files"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScriptFixture(t)
			f.previousSet(t)
			statusBefore := f.status(t)
			before := f.snapshot(t)
			out, err := f.run(t, tc.env, "backup.sh", "once", "pre-update-20260102-030405")
			if (err == nil) != tc.ok || tc.ok != strings.Contains(out, "safety backup pre-update-20260102-030405: SUCCESS") {
				t.Fatalf("ok=%v: %v\n%s", tc.ok, err, out)
			}
			if strings.Contains(out, "removed previous backup") {
				t.Fatalf("safety backup pruned the verified set:\n%s", out)
			}
			after := f.snapshot(t)
			for _, name := range []string{"care-pre-update-20260102-030405.dump.enc", "files-pre-update-20260102-030405.tar.gz.enc"} {
				if _, ok := after[name]; ok != tc.ok {
					t.Fatalf("%s present=%v, want %v", name, ok, tc.ok)
				}
				delete(after, name)
			}
			sameSnapshot(t, before, after)
			if f.status(t) != statusBefore {
				t.Fatal("a safety backup changed the verified backup status")
			}
		})
	}
}

func TestBackupScriptPruneFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newScriptFixture(t)
	f.previousSet(t)
	if err := os.Chmod(f.wal, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.wal, 0o700) })
	out, err := f.run(t, nil, "backup.sh", "now")
	if err == nil || !strings.Contains(out, "published, but cleanup FAILED") || strings.Contains(out, ": SUCCESS") {
		t.Fatalf("cleanup failure not reported: %v\n%s", err, out)
	}
	if m := f.manifest(t); m.Set != "20260102-030405" {
		t.Fatalf("verified new set not kept: %+v", m)
	}
	for _, name := range []string{"care-20260102-030405.dump.enc", "files-20260102-030405.tar.gz.enc", "base-20260102-030405"} {
		if _, err := os.Stat(filepath.Join(f.backups, name)); err != nil {
			t.Fatalf("verified new set lost %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(f.wal, seg(2)+".sha256")); err != nil {
		t.Fatal("change file vanished despite failed removal")
	}
	s := f.status(t)
	if !strings.Contains(s, "state=failed\n") || !strings.Contains(s, "removing the previous one failed; the new verified backup was kept") {
		t.Fatalf("status: %s", s)
	}
	for _, claim := range []string{"previous backup was kept", "every change file were kept"} {
		if strings.Contains(s+out, claim) {
			t.Fatalf("partial cleanup made a false claim %q", claim)
		}
	}
}

func TestBackupScriptPrunesOnlyTopLevelRegularFiles(t *testing.T) {
	f := newScriptFixture(t)
	f.previousSet(t)
	keep := map[string]string{
		"notes.txt": "file",
		"unrelated/care-20250101-000000.dump.enc": "file",
		"unrelated/base-20250101-000000/x":        "file",
		"care-20250101-010101.dump.enc/x":         "file",
		"base-20250101-010101":                    "file",
		"wal/README":                              "file",
	}
	for name := range keep {
		p := filepath.Join(f.backups, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := []string{"files-20250101-010101.tar.gz.enc", "base-20250101-020202", "wal/" + seg(1) + "x.sha256"}
	for _, l := range links {
		if err := os.Symlink(filepath.Join(f.backups, "notes.txt"), filepath.Join(f.backups, l)); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := f.run(t, nil, "backup.sh", "now"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for name := range keep {
		if b, err := os.ReadFile(filepath.Join(f.backups, name)); err != nil || string(b) != "fixture" {
			t.Fatalf("pruning touched unrelated %s", name)
		}
	}
	for _, l := range links {
		if _, err := os.Lstat(filepath.Join(f.backups, l)); err != nil {
			t.Fatalf("pruning followed or removed symlink %s", l)
		}
	}
	for _, gone := range []string{"care-20260101-030405.dump.enc", "files-20260101-030405.tar.gz.enc", "base-20260101-030405"} {
		if _, err := os.Lstat(filepath.Join(f.backups, gone)); !os.IsNotExist(err) {
			t.Fatalf("previous set member %s kept after the new set was verified", gone)
		}
	}
}

func TestBackupScriptRejectsExistingBackupPaths(t *testing.T) {
	for _, tc := range []struct {
		name, final, kind string
		args              []string
	}{
		{"scheduled dump", "care-20260102-030405.dump.enc", "file", []string{"now"}},
		{"scheduled files", "files-20260102-030405.tar.gz.enc", "file", []string{"now"}},
		{"scheduled full copy", "base-20260102-030405", "directory", []string{"now"}},
		{"dangling symlink", "care-20260102-030405.dump.enc", "symlink", []string{"now"}},
		{"safety replay", "care-pre-update-20260102-030405.dump.enc", "published", []string{"once", "pre-update-20260102-030405"}},
		{"safety files", "files-pre-update-20260102-030405.tar.gz.enc", "file", []string{"once", "pre-update-20260102-030405"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			path := filepath.Join(f.backups, tc.final)
			switch tc.kind {
			case "published":
				if out, err := f.run(t, nil, "backup.sh", tc.args...); err != nil {
					t.Fatalf("first: %v\n%s", err, out)
				}
			case "file":
				if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			}
			_ = os.Remove(filepath.Join(f.root, "trace"))
			before := f.snapshot(t)
			out, err := f.run(t, nil, "backup.sh", tc.args...)
			if err == nil || !strings.Contains(out, "refusing to overwrite existing backup") || strings.Contains(out, ": SUCCESS") {
				t.Fatalf("existing backup path was not rejected: %v\n%s", err, out)
			}
			for _, cmd := range []string{"pg_basebackup", "pg_dump"} {
				if strings.Contains(readTrace(t, f), cmd+" ") {
					t.Fatalf("collision reached %s", cmd)
				}
			}
			sameSnapshot(t, before, f.snapshot(t))
		})
	}
}

func TestBackupScriptRefusesWhenBackupFolderIsFull(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	out, err := f.run(t, []string{"FAKE_FREE_KB=1024"}, "backup.sh", "now")
	if err == nil || !strings.Contains(out, "not enough space in the backup folder") || strings.Contains(out, ": SUCCESS") {
		t.Fatalf("full folder was not refused:\n%s", out)
	}
	if strings.Contains(readTrace(t, f), "pg_basebackup ") {
		t.Fatal("backup started writing on a full folder")
	}
	status := f.status(t)
	for _, want := range []string{"state=failed\n", "reason=disk_full\n", "free_kb=1024\n", "need_kb=1048576\n"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
}

func TestBackupScriptRefusesWhenWorkAreaIsFull(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	out, err := f.run(t, []string{"FAKE_WORK_FREE_KB=10"}, "backup.sh", "now")
	if err == nil || !strings.Contains(out, "not enough room on the clinic's data disk") || strings.Contains(readTrace(t, f), "pg_basebackup ") {
		t.Fatalf("full work area was not refused before copying:\n%s", out)
	}
	if !strings.Contains(f.status(t), "reason=disk_full\n") {
		t.Fatal(f.status(t))
	}
}

func TestBackupScriptSizesNeedFromLatestSet(t *testing.T) {
	f := newScriptFixture(t)
	du, err := exec.Command("du", "-sk", filepath.Join(f.root, "minio")).Output()
	if err != nil {
		t.Fatal(err)
	}
	files, err := strconv.Atoi(strings.Fields(string(du))[0])
	if err != nil {
		t.Fatal(err)
	}
	const clusterKB = 4 << 20
	need := (2*clusterKB+files)*5/4 + 262144
	for _, tc := range []struct {
		free int
		ok   bool
	}{{need - 1, false}, {need, true}} {
		t.Run(strconv.Itoa(tc.free), func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			out, err := f.run(t, []string{"FAKE_CLUSTER_BYTES=" + strconv.Itoa(clusterKB*1024), "FAKE_FREE_KB=" + strconv.Itoa(tc.free)}, "backup.sh", "now")
			if (err == nil) != tc.ok || strings.Contains(out, ": SUCCESS") != tc.ok {
				t.Fatalf("free=%d need=%d: %v\n%s", tc.free, need, err, out)
			}
			if !tc.ok && !strings.Contains(f.status(t), "need_kb="+strconv.Itoa(need)+"\n") {
				t.Fatalf("status: %s", f.status(t))
			}
		})
	}
}

func TestBackupScriptRecordsOrdinaryFailure(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	if _, err := f.run(t, []string{"FAKE_FAIL=verify"}, "backup.sh", "now"); err == nil {
		t.Fatal("failure reported as success")
	}
	if s := f.status(t); !strings.Contains(s, "state=failed\n") || !strings.Contains(s, "reason=error\n") {
		t.Fatalf("ordinary failure recorded as:\n%s", s)
	}
}

func TestBackupScriptManualRefusesWhenFull(t *testing.T) {
	f := newScriptFixture(t)
	out, err := f.run(t, []string{"FAKE_FREE_KB=10"}, "backup.sh", "once", "pre-update-20260102-030405")
	if err == nil || !strings.Contains(out, "not enough space in the backup folder") {
		t.Fatalf("safety backup on a full folder: %v\n%s", err, out)
	}
	if strings.Contains(readTrace(t, f), "pg_dump ") {
		t.Fatal("safety backup started writing on a full folder")
	}
	if _, err := os.Stat(filepath.Join(f.state, "backup-status")); !os.IsNotExist(err) {
		t.Fatalf("safety backup touched the verified backup status: %v", err)
	}
}

func TestBackupScriptLeavesNoPlaintextBehind(t *testing.T) {
	for _, fail := range []string{"", "list", "verify"} {
		t.Run("fail="+fail, func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			_, _ = f.run(t, []string{"FAKE_FAIL=" + fail}, "backup.sh", "now")
			if names := listDir(t, filepath.Join(f.root, "work")); len(names) != 0 {
				t.Fatalf("plaintext work files left: %v", names)
			}
			if names := listDir(t, filepath.Join(f.root, "shm")); len(names) != 0 {
				t.Fatalf("verification keys left: %v", names)
			}
			for p := range f.snapshot(t) {
				if p != "." && p != "wal" && !strings.HasSuffix(p, ".enc") && !strings.HasSuffix(p, ".sha256") &&
					p != "manifest.json" && !strings.HasPrefix(p, "base-") {
					t.Fatalf("unencrypted file in the backup folder: %s", p)
				}
			}
		})
	}
}

func TestBackupScriptManualSuccessClearsFailedStatus(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	if _, err := f.run(t, []string{"FAKE_FAIL=dump"}, "backup.sh", "now"); err == nil {
		t.Fatal("failure reported as success")
	}
	if !strings.Contains(f.status(t), "state=failed") {
		t.Fatal(f.status(t))
	}
	if out, err := f.run(t, nil, "backup.sh", "now"); err != nil {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	if !strings.Contains(f.status(t), "state=ok\n") {
		t.Fatalf("a successful backup left the failed status:\n%s", f.status(t))
	}
}

func TestBackupScriptReportsStatusWriteFailures(t *testing.T) {
	for _, blocked := range []string{".backup-status.tmp", ".backup-ok-at.tmp"} {
		t.Run(blocked, func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			// A directory where the temp file belongs makes the write fail.
			if err := os.MkdirAll(filepath.Join(f.state, blocked, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := f.run(t, nil, "backup.sh", "now")
			if err == nil {
				t.Fatalf("an unsaved status was reported as success:\n%s", out)
			}
			if !strings.Contains(out, "ERROR: could not save") {
				t.Fatalf("status write failure was not logged:\n%s", out)
			}
		})
	}
}

func TestBackupScriptReportsMissingStateFolder(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	if err := os.RemoveAll(f.state); err != nil {
		t.Fatal(err)
	}
	out, err := f.run(t, nil, "backup.sh", "now")
	if err == nil || !strings.Contains(out, "backup status folder") {
		t.Fatalf("a missing status folder was not reported: %v\n%s", err, out)
	}
}

func TestBackupCycleRefusesLaterTimelineChangeFiles(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	f.archive(t, "000000020000000000000005")
	out, err := f.run(t, nil, "backup.sh", "now")
	if err == nil || !strings.Contains(out, "later database timeline") {
		t.Fatalf("a branched change-file chain was accepted: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(f.backups, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("a set was published over a branched chain: %v", err)
	}
}

// pitr-stage.sh must not hand a database to the staged swap unless
// PostgreSQL reports that replay stopped at the requested moment.
func TestPITRStageRequiresReachedTarget(t *testing.T) {
	f := newScriptFixture(t)
	f.archive(t, seg(4))
	if out, err := f.run(t, nil, "backup.sh", "now"); err != nil {
		t.Fatalf("cycle: %v\n%s", err, out)
	}
	for _, tool := range []string{"gosu", "chown", "pg_ctl"} {
		if err := os.WriteFile(filepath.Join(f.bin, tool), []byte(fakeBackupTools), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, target, log string
		ok                bool
	}{
		{"reached", "2026-01-02 03:10:00+00", "LOG:  recovery stopping before commit of transaction 812, time 2026-01-02 03:10:04+00", true},
		{"ran out of change files", "2026-01-02 03:10:00+00", "LOG:  redo done at 0/5000060", false},
		{"latest", "latest", "LOG:  redo done at 0/5000060", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := filepath.Join(f.root, "stage-"+strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.MkdirAll(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := f.run(t, []string{"CARE_RESTORE_DIR=" + stage, "CARE_RESTORE_KEY=" + f.key,
				"FAKE_RECOVERY_LOG=" + tc.log}, "pitr-stage.sh", tc.target)
			_, dumpErr := os.Stat(filepath.Join(stage, "database.dump"))
			if tc.ok && (err != nil || dumpErr != nil) {
				t.Fatalf("staging failed: %v %v\n%s", err, dumpErr, out)
			}
			if !tc.ok && (err == nil || dumpErr == nil || !strings.Contains(out, "replay ended before reaching")) {
				t.Fatalf("an unreached target was staged for the swap: %v %v\n%s", err, dumpErr, out)
			}
			if _, err := os.Stat(filepath.Join(stage, "recovery-key.pem")); !os.IsNotExist(err) {
				t.Fatalf("the recovery key was left in the stage: %v", err)
			}
		})
	}
}

func TestBackupScriptWritersSerialize(t *testing.T) {
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("native flock is unavailable")
	}
	for _, first := range [][]string{{"now"}, {"once", "pre-update-20260102-040404"}} {
		t.Run(first[0]+" first", func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			second := []string{"once", "pre-update-20260102-050505"}
			if first[0] == "once" {
				second = []string{"now"}
			}
			start := func(args []string, env ...string) (*exec.Cmd, *bytes.Buffer) {
				cmd := exec.Command("sh", append([]string{filepath.Join(f.scripts, "backup.sh")}, args...)...)
				cmd.Env = append(append(append([]string{}, f.env...), "FAKE_REAL_FLOCK="+flock), env...)
				var out bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &out
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				return cmd, &out
			}
			waitFile := func(name string) {
				for i := 0; ; i++ {
					if _, err := os.Stat(filepath.Join(f.root, name)); err == nil {
						return
					}
					if i > 400 {
						t.Fatalf("%s never appeared", name)
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			a, aOut := start(first, "FAKE_HOLD=1")
			waitFile("holding")
			b, bOut := start(second)
			time.Sleep(300 * time.Millisecond)
			if strings.Count(readTrace(t, f), "pg_dump ") != 1 {
				t.Fatal("a second writer ran while the first held the backup lock")
			}
			if err := os.WriteFile(filepath.Join(f.root, "release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := a.Wait(); err != nil {
				t.Fatalf("first: %v\n%s", err, aOut)
			}
			if err := b.Wait(); err != nil {
				t.Fatalf("second: %v\n%s", err, bOut)
			}
		})
	}
	t.Run("scheduler releases the lock while sleeping", func(t *testing.T) {
		f := newScriptFixture(t)
		f.archive(t, seg(4))
		d := exec.Command("sh", filepath.Join(f.scripts, "backup.sh"))
		d.Env = append(append([]string{}, f.env...), "FAKE_REAL_FLOCK="+flock, "FAKE_SLEEP_WAIT=1",
			"CARE_BACKUP_TICK=7", "CARE_BACKUP_INTERVAL=0", "CARE_BACKUP_RETRY=0")
		var dOut bytes.Buffer
		d.Stdout, d.Stderr = &dOut, &dOut
		if err := d.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Process.Kill(); _ = d.Wait() }()
		for i := 0; ; i++ {
			if _, err := os.Stat(filepath.Join(f.root, "sleeping")); err == nil {
				break
			}
			if i > 800 {
				t.Fatalf("scheduler never slept:\n%s", dOut.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
		if out, err := f.run(t, []string{"FAKE_REAL_FLOCK=" + flock}, "backup.sh", "once", "pre-update-20260102-060606"); err != nil {
			t.Fatalf("lock held across scheduler sleep: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(f.backups, "care-20260102-030405.dump.enc")); err != nil {
			t.Fatalf("scheduled set missing:\n%s", dOut.String())
		}
	})
}
