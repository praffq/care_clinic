package backupcopy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeSet publishes a verified set and change files from start_wal to the
// given last segment number, as the sidecar would.
func fakeSet(t *testing.T, src, set string, first, last int) {
	t.Helper()
	files := []map[string]any{}
	for _, rel := range []string{"base-" + set + "/base.tar.gz.enc", "care-" + set + ".dump.enc", "files-" + set + ".tar.gz.enc"} {
		body := "cipher " + rel
		write(t, filepath.Join(src, rel), body)
		files = append(files, map[string]any{"path": rel, "sha256": sum([]byte(body)), "plain_sha256": strings.Repeat("0", 64), "bytes": len(body)})
	}
	for n := first; n <= last; n++ {
		addSegment(t, src, n)
	}
	checks := map[string]string{}
	for _, c := range storage.RequiredChecks {
		checks[c] = "ok"
	}
	m := map[string]any{
		"version": 1, "set": set, "created_at": 1767233400, "started_at": 1767232800,
		"base":  map[string]any{"start_wal": seg(first), "wal_segment_bytes": 16777216},
		"files": files, "wal": map[string]any{"dir": "wal", "from": seg(first), "through": seg(last)}, "checks": checks,
	}
	data, _ := json.Marshal(m)
	write(t, filepath.Join(src, "manifest.json"), string(data))
}

func seg(n int) string { return "00000001" + "00000000" + strings.ToUpper(hexPad(n)) }

func hexPad(n int) string {
	s := strings.ToUpper(hex.EncodeToString([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}))
	return s
}

func addSegment(t *testing.T, src string, n int) {
	t.Helper()
	body := "wal " + seg(n)
	write(t, filepath.Join(src, "wal", seg(n)+".gz.enc"), body)
	write(t, filepath.Join(src, "wal", seg(n)+".sha256"), "sha256="+sum([]byte(body))+"\nplain_sha256="+strings.Repeat("1", 64)+"\nwaldump=ok\n")
}

func run(t *testing.T, src, state, dest string) storage.CopyStatus {
	t.Helper()
	st, err := Run(src, state, dest, time.Unix(1767240000, 0))
	if err != nil {
		t.Fatal(err)
	}
	disk, err := storage.ReadCopyStatus(state)
	if err != nil || disk != st {
		t.Fatalf("status on disk %+v != returned %+v (%v)", disk, st, err)
	}
	return st
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestCopyOffAndWaiting(t *testing.T) {
	src, state, drive := t.TempDir(), t.TempDir(), t.TempDir()
	if st := run(t, src, state, ""); st.State != "off" {
		t.Fatalf("off: %+v", st)
	}
	if st := run(t, src, state, drive); st.State != "waiting" || exists(filepath.Join(drive, Folder, "manifest.json")) {
		t.Fatalf("no backup yet: %+v", st)
	}
}

func TestCopyMirrorsVerifiedSetAndCatchesUpAfterReconnect(t *testing.T) {
	src, state, root := t.TempDir(), t.TempDir(), t.TempDir()
	drive := filepath.Join(root, "usb")
	if err := os.Mkdir(drive, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeSet(t, src, "20260101-020000", 4, 6)
	st := run(t, src, state, drive)
	dest := filepath.Join(drive, Folder)
	if st.State != "ok" || st.Set != "20260101-020000" || st.WALThrough != seg(6) || st.LastCopiedAt != 1767240000 {
		t.Fatalf("first copy: %+v", st)
	}
	for _, rel := range []string{"manifest.json", "base-20260101-020000/base.tar.gz.enc", "care-20260101-020000.dump.enc",
		"files-20260101-020000.tar.gz.enc", "wal/" + seg(6) + ".gz.enc", "wal/" + seg(6) + ".sha256"} {
		if !exists(filepath.Join(dest, rel)) {
			t.Fatalf("missing %s", rel)
		}
	}

	// Drive unplugged: the copy reports disconnected, keeps its last time,
	// and nothing is created where the drive was.
	if err := os.Rename(drive, drive+"-away"); err != nil {
		t.Fatal(err)
	}
	addSegment(t, src, 7)
	fakeSet(t, src, "20260102-020000", 7, 8)
	st = run(t, src, state, drive)
	if st.State != "disconnected" || st.LastCopiedAt != 1767240000 || st.Set != "20260101-020000" {
		t.Fatalf("absent drive: %+v", st)
	}
	if exists(drive) {
		t.Fatal("the copier created the missing drive path on the local disk")
	}

	// Reconnected: catch up to the new set, then drop the old one.
	if err := os.Rename(drive+"-away", drive); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, "keep-me.txt"), "user file")
	st = run(t, src, state, drive)
	if st.State != "ok" || st.Set != "20260102-020000" || st.WALThrough != seg(8) {
		t.Fatalf("catch-up: %+v", st)
	}
	for _, gone := range []string{"base-20260101-020000", "care-20260101-020000.dump.enc", "wal/" + seg(4) + ".gz.enc", "wal/" + seg(6) + ".sha256"} {
		if exists(filepath.Join(dest, gone)) {
			t.Fatalf("old copy file %s was kept", gone)
		}
	}
	if !exists(filepath.Join(dest, "keep-me.txt")) || !exists(filepath.Join(dest, "base-20260102-020000/base.tar.gz.enc")) {
		t.Fatal("catch-up removed an unrelated file or missed the new set")
	}
	data, _ := os.ReadFile(filepath.Join(dest, "manifest.json"))
	srcData, _ := os.ReadFile(filepath.Join(src, "manifest.json"))
	if string(data) != string(srcData) {
		t.Fatal("copy manifest differs from the verified source")
	}
}

func TestCopyRefusesCorruptSourceAndKeepsPreviousCopy(t *testing.T) {
	src, state, drive := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSet(t, src, "20260101-020000", 4, 5)
	run(t, src, state, drive)
	dest := filepath.Join(drive, Folder)
	fakeSet(t, src, "20260102-020000", 6, 6)
	write(t, filepath.Join(src, "files-20260102-020000.tar.gz.enc"), "tampered")
	st := run(t, src, state, drive)
	if st.State != "failed" || !strings.Contains(st.Message, "does not match its recorded checksum") ||
		st.Set != "20260101-020000" || st.LastCopiedAt != 1767240000 {
		t.Fatalf("corrupt source: %+v", st)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if !strings.Contains(string(data), "20260101-020000") {
		t.Fatal("copy manifest was advanced past an unverified file")
	}
	for _, kept := range []string{"care-20260101-020000.dump.enc", "base-20260101-020000/base.tar.gz.enc", "wal/" + seg(4) + ".gz.enc"} {
		if !exists(filepath.Join(dest, kept)) {
			t.Fatalf("failed copy deleted %s from the previous copy", kept)
		}
	}
	if exists(filepath.Join(dest, ".tmp-files-20260102-020000.tar.gz.enc")) || exists(filepath.Join(dest, "files-20260102-020000.tar.gz.enc")) {
		t.Fatal("a corrupt file was left on the copy")
	}
}

func TestCopyRequiresTheVerifiedChangeFileChain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(src string)
		want   string
	}{
		{"gap", func(src string) { _ = os.Remove(filepath.Join(src, "wal", seg(5)+".sha256")) }, "is missing"},
		{"missing folder", func(src string) { _ = os.RemoveAll(filepath.Join(src, "wal")) }, "change files"},
		{"short of manifest", func(src string) { _ = os.Remove(filepath.Join(src, "wal", seg(6)+".sha256")) }, "missing"},
		{"bad record", func(src string) { write(t, filepath.Join(src, "wal", seg(5)+".sha256"), "nope\n") }, "no valid checksum"},
		{"corrupt segment", func(src string) { write(t, filepath.Join(src, "wal", seg(5)+".gz.enc"), "flipped") }, "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, state, drive := t.TempDir(), t.TempDir(), t.TempDir()
			fakeSet(t, src, "20260101-020000", 4, 6)
			tc.break_(src)
			st := run(t, src, state, drive)
			if st.State != "failed" || !strings.Contains(st.Message, tc.want) || st.LastCopiedAt != 0 {
				t.Fatalf("status = %+v", st)
			}
			if exists(filepath.Join(drive, Folder, "manifest.json")) {
				t.Fatal("an unrestorable copy was marked current")
			}
		})
	}
}

func TestCopyIncludesTimelineHistory(t *testing.T) {
	src, state, drive := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSet(t, src, "20260101-020000", 4, 4)
	write(t, filepath.Join(src, "wal", "00000002.history.gz.enc"), "hist")
	write(t, filepath.Join(src, "wal", "00000002.history.sha256"), "sha256="+sum([]byte("hist"))+"\n")
	if st := run(t, src, state, drive); st.State != "ok" {
		t.Fatalf("%+v", st)
	}
	if !exists(filepath.Join(drive, Folder, "wal", "00000002.history.sha256")) {
		t.Fatal("timeline history was not copied")
	}
}

func TestCopyReplacesCorruptDestinationFile(t *testing.T) {
	src, state, drive := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSet(t, src, "20260101-020000", 4, 4)
	run(t, src, state, drive)
	target := filepath.Join(drive, Folder, "care-20260101-020000.dump.enc")
	write(t, target, "rotted on usb")
	write(t, filepath.Join(drive, Folder, ".tmp-care-20260101-020000.dump.enc"), "half written")
	if st := run(t, src, state, drive); st.State != "ok" {
		t.Fatalf("%+v", st)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "cipher care-20260101-020000.dump.enc" || exists(filepath.Join(drive, Folder, ".tmp-care-20260101-020000.dump.enc")) {
		t.Fatalf("corrupt copy not repaired: %q", data)
	}
}

func TestCopyStatusWriteFailureIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission fixture")
	}
	src, root, drive := t.TempDir(), t.TempDir(), t.TempDir()
	state := filepath.Join(root, "state")
	write(t, state, "a file where the state folder should be")
	if _, err := Run(src, state, drive, time.Now()); err == nil {
		t.Fatal("an unrecordable copy status was silent")
	}
}

func TestNextSegment(t *testing.T) {
	if got := nextSegment("0000000100000000000000FF", 16777216); got != "000000010000000100000000" {
		t.Fatalf("16MB wrap = %s", got)
	}
	if got := nextSegment("000000010000000000000004", 16777216); got != "000000010000000000000005" {
		t.Fatalf("next = %s", got)
	}
	if got := nextSegment("000000010000000000000003", 1<<30); got != "000000010000000100000000" {
		t.Fatalf("1GB wrap = %s", got)
	}
}
