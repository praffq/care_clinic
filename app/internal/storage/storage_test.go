package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

func writeSized(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLatestSetSizesTheNewestVerifiedSetWithItsFullCopy(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, dir, "care-20260101-020000.dump.enc", 100)
	writeSized(t, dir, "files-20260101-020000.tar.gz.enc", 300)
	writeSized(t, dir, "care-20260102-020000.dump.enc", 200)
	writeSized(t, dir, "files-20260102-020000.tar.gz.enc", 500)
	if err := os.Mkdir(filepath.Join(dir, "base-20260102-020000"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(dir, "base-20260102-020000"), "base.tar.gz.enc", 700)
	writeSized(t, dir, "care-pre-update-20260103-090000.dump.enc", 250)
	writeSized(t, dir, "care-manual-20260104-090000.dump.enc", 250)
	writeSized(t, dir, "unrelated.txt", 999)

	set, found, err := LatestSet(dir)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if set.Stamp != "20260102-020000" || set.DumpBytes != 200 || set.FilesBytes != 500 || set.BaseBytes != 700 || set.Bytes() != 1400 {
		t.Fatalf("set = %+v", set)
	}
}

func TestLatestSetIgnoresLinksAndMissingParts(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, dir, "care-20260101-020000.dump.enc", 100)
	if err := os.Symlink(filepath.Join(dir, "care-20260101-020000.dump.enc"), filepath.Join(dir, "care-20260109-020000.dump.enc")); err != nil {
		t.Skip(err)
	}
	set, found, err := LatestSet(dir)
	if err != nil || !found || set.Stamp != "20260101-020000" || set.Bytes() != 100 {
		t.Fatalf("set=%+v found=%v err=%v", set, found, err)
	}
}

func TestLatestSetMissingFolder(t *testing.T) {
	_, found, err := LatestSet(filepath.Join(t.TempDir(), "nope"))
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestBackupNeed(t *testing.T) {
	if got := BackupNeed(BackupSet{}, false); got != BackupFloor {
		t.Fatalf("no backups: need = %d, want floor", got)
	}
	small := BackupSet{DumpBytes: 10 * MB, FilesBytes: 10 * MB}
	if got := BackupNeed(small, true); got != BackupFloor {
		t.Fatalf("small set: need = %s, want floor", Human(got))
	}
	big := BackupSet{BaseBytes: 3 * GB, DumpBytes: 2 * GB, FilesBytes: 4 * GB}
	want := 9*GB*5/4 + backupMargin
	if got := BackupNeed(big, true); got != want {
		t.Fatalf("big set: need = %s, want %s", Human(got), Human(want))
	}
}

func TestRunningLevel(t *testing.T) {
	for _, tc := range []struct {
		free uint64
		want Level
	}{
		{4 * GB, LevelCritical},
		{5 * GB, LevelLow},
		{9 * GB, LevelLow},
		{10 * GB, LevelOK},
	} {
		if got := RunningLevel(tc.free); got != tc.want {
			t.Errorf("RunningLevel(%s) = %s, want %s", Human(tc.free), got, tc.want)
		}
	}
}

func TestCheckInstall(t *testing.T) {
	docker := diskspace.Usage{Path: "/d", Volume: "1", Free: 29 * GB}
	if r := CheckInstall(docker, docker); r.OK {
		t.Fatalf("29 GB passed: %+v", r)
	}
	docker.Free = 30 * GB
	if r := CheckInstall(docker, docker); !r.OK {
		t.Fatalf("30 GB failed: %+v", r)
	}
	install := diskspace.Usage{Path: "/i", Volume: "2", Free: 100 * MB}
	if r := CheckInstall(install, docker); r.OK {
		t.Fatalf("near-full settings drive passed: %+v", r)
	}
}

func TestAssessBackupKeepsOnlyTheNewestSet(t *testing.T) {
	set := BackupSet{BaseBytes: 1 * GB, DumpBytes: 1 * GB, FilesBytes: 1 * GB}
	need := BackupNeed(set, true)

	if b := AssessBackup("/b", diskspace.Usage{Free: need - 1}, set, true, false); b.Level != LevelCritical {
		t.Fatalf("below need: %+v", b)
	}
	if b := AssessBackup("/b", diskspace.Usage{Free: need + 1}, set, true, false); b.Level != LevelLow {
		t.Fatalf("barely fits: %+v", b)
	}
	b := AssessBackup("/b", diskspace.Usage{Free: 100 * need}, set, true, true)
	if b.Level != LevelOK || !b.SharesDockerDrive || !strings.Contains(b.Message, "Only the newest verified backup is kept") {
		t.Fatalf("plenty of room: %+v", b)
	}
	data, err := json.Marshal(b)
	if err != nil || strings.Contains(string(data), "days_left") || strings.Contains(string(data), "retention") {
		t.Fatalf("backup space still exposes a retention contract: %s %v", data, err)
	}
}

func TestSummarise(t *testing.T) {
	now := time.Now()
	r := Report{Backup: BackupSpace{Level: LevelOK}, Drives: []Drive{{Level: LevelOK}}, NewestBackupAt: now.Add(-30 * time.Hour).Unix()}
	Summarise(&r, true, now)
	if !r.Stale || r.Level != LevelCritical {
		t.Fatalf("stale not flagged: %+v", r)
	}
	Summarise(&r, false, now)
	if r.Stale || r.Level != LevelOK {
		t.Fatalf("stopped clinic flagged stale: %+v", r)
	}
	r.LastRun = BackupRun{State: "failed", Reason: "disk_full"}
	Summarise(&r, false, now)
	if r.Level != LevelCritical || r.Headline == "" {
		t.Fatalf("failed run not surfaced: %+v", r)
	}
}

func TestReadBackupRun(t *testing.T) {
	dir := t.TempDir()
	if run, err := ReadBackupRun(dir); err != nil || run.State != "" {
		t.Fatalf("missing file: %+v %v", run, err)
	}
	body := "state=failed\nreason=disk_full\nat=1767225600\nneed_kb=2048\nfree_kb=1024\nmessage=not enough space\n"
	if err := os.WriteFile(filepath.Join(dir, StatusFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := ReadBackupRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "failed" || run.Reason != "disk_full" || run.At != 1767225600 ||
		run.NeedBytes != 2*MB || run.FreeBytes != 1*MB || run.Message != "not enough space" {
		t.Fatalf("parsed %+v", run)
	}
}

func TestDatabaseReadinessFailureDoesNotMeanSavedBackupsAreMissing(t *testing.T) {
	now := time.Now()
	r := Report{
		Backup:         BackupSpace{Level: LevelOK},
		LastRun:        BackupRun{State: "failed", Reason: "database_unavailable", At: now.Unix()},
		NewestBackupAt: now.Add(-time.Hour).Unix(),
	}
	Summarise(&r, true, now)
	if r.Level != LevelCritical || r.Stale || r.NewestBackupAt == 0 ||
		r.Headline != "The last backup could not start: the database was not ready." {
		t.Fatalf("database readiness failure lost its cause or existing backup: %+v", r)
	}
}

func TestParseDF(t *testing.T) {
	out := "Filesystem     1024-blocks     Used Available Capacity Mounted on\noverlay          102626232 40000000  62626232      39% /\n"
	free, total, err := ParseDF(out)
	if err != nil {
		t.Fatal(err)
	}
	if free != 62626232<<10 || total != 102626232<<10 {
		t.Fatalf("free=%d total=%d", free, total)
	}
	if _, _, err := ParseDF("garbage"); err == nil {
		t.Fatal("expected an error for unparseable output")
	}
}
