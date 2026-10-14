package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeManifest(t *testing.T, dir string, edit func(map[string]any)) {
	t.Helper()
	checks := map[string]any{}
	for _, c := range RequiredChecks {
		checks[c] = "ok"
	}
	m := map[string]any{
		"version": 1, "set": "20260101-020000", "created_at": 1767233400, "started_at": 1767232800,
		"base":   map[string]any{"start_wal": "000000010000000000000004", "ended_at": 1767233000, "wal_segment_bytes": 16777216},
		"files":  []any{map[string]any{"path": "care-20260101-020000.dump.enc", "sha256": strings.Repeat("b", 64), "bytes": 3}},
		"wal":    map[string]any{"dir": "wal", "from": "000000010000000000000004", "through": "000000010000000000000006"},
		"checks": checks, "trial_restore": map[string]any{"state": "passed", "at": 1767233300},
	}
	if edit != nil {
		edit(m)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeState(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codes(h BackupHealth) []string {
	var out []string
	for _, a := range h.Alerts {
		out = append(out, a.Code)
	}
	return out
}

func TestHealthReportsVerifiedBackupAndSavedThrough(t *testing.T) {
	backups, state := t.TempDir(), t.TempDir()
	writeManifest(t, backups, nil)
	writeState(t, state, WALStatusFile, "state=ok\nchecked_at=1767240000\nsaved_through=1767239900\nlast_archived_at=1767239900\nlast_failed_at=0\ngap=\nmessage=\n")
	h := ReadHealth(backups, state, true, time.Unix(1767240000, 0))
	if !h.Verified || h.LastBackupAt != 1767233400 || h.SavedThrough != 1767239900 || h.EarliestRestoreAt != 1767233000 ||
		h.Set != "20260101-020000" || h.Trial.State != "passed" || h.Copy.State != "off" || len(h.Alerts) != 0 {
		t.Fatalf("health = %+v", h)
	}
}

func TestHealthAlerts(t *testing.T) {
	now := time.Unix(1767240000, 0)
	for _, tc := range []struct {
		name     string
		manifest func(map[string]any)
		noManif  bool
		wal      string
		run      string
		copy     string
		running  bool
		want     []string
	}{
		{name: "no backup", noManif: true, running: true, want: []string{"no_backup"}},
		{name: "unverified", manifest: func(m map[string]any) { m["checks"].(map[string]any)["pg_waldump"] = "failed" }, wal: "state=ok\n", want: []string{"unverified"}},
		{name: "stale backup", manifest: func(m map[string]any) { m["created_at"] = 1767240000 - 2*86400 }, wal: "state=ok\n", running: true, want: []string{"backup_stale"}},
		{name: "stale ignored when stopped", manifest: func(m map[string]any) { m["created_at"] = 1767240000 - 2*86400 }, wal: "state=ok\n"},
		{name: "failed keeps previous", run: "state=failed\nreason=error\nat=1767239000\nmessage=the full database copy failed\n", wal: "state=ok\n", want: []string{"backup_failed"}},
		{name: "wal failing", wal: "state=failing\nmessage=PostgreSQL could not archive X\n", running: true, want: []string{"wal_failing"}},
		{name: "wal stale", wal: "state=stale\nmessage=waiting\n", running: true, want: []string{"wal_stale"}},
		{name: "wal off", wal: "state=off\n", running: true, want: []string{"wal_off"}},
		{name: "wal gap", wal: "state=ok\ngap=000000010000000000000005\n", running: true, want: []string{"wal_gap"}},
		{name: "wal unknown", running: true, want: []string{"wal_unknown"}},
		{name: "status not refreshing", wal: "state=ok\nchecked_at=1767238000\n", running: true, want: []string{"status_stale"}},
		{name: "status age ignored when stopped", wal: "state=ok\nchecked_at=1767238000\n"},
		{name: "trial failed", manifest: func(m map[string]any) {
			m["trial_restore"] = map[string]any{"state": "failed", "message": "counts differ"}
		}, wal: "state=ok\n", want: []string{"trial_failed"}},
		{name: "trial skipped", manifest: func(m map[string]any) {
			m["trial_restore"] = map[string]any{"state": "skipped", "message": "not enough space"}
		}, wal: "state=ok\n", want: []string{"trial_skipped"}},
		{name: "copy missing", wal: "state=ok\n", copy: `{"state":"disconnected","dir":"/media/usb","last_copied_at":1767200000,"set":"20251231-020000"}`, want: []string{"copy_missing"}},
		{name: "copy failed", wal: "state=ok\n", copy: `{"state":"failed","message":"disk full"}`, want: []string{"copy_failed"}},
		{name: "copy stale", wal: "state=ok\n", copy: `{"state":"ok","last_copied_at":1767000000,"set":"20251225-020000"}`, want: []string{"copy_stale"}},
		{name: "copy unreadable", wal: "state=ok\n", copy: `{`, want: []string{"status_unreadable", "copy_failed"}},
		{name: "manifest unsafe path", manifest: func(m map[string]any) {
			m["files"] = []any{map[string]any{"path": "../escape", "sha256": strings.Repeat("b", 64)}}
		}, wal: "state=ok\n", want: []string{"manifest_unreadable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backups, state := t.TempDir(), t.TempDir()
			if !tc.noManif {
				writeManifest(t, backups, tc.manifest)
			}
			if tc.wal != "" {
				writeState(t, state, WALStatusFile, tc.wal)
			}
			if tc.run != "" {
				writeState(t, state, StatusFile, tc.run)
			}
			if tc.copy != "" {
				writeState(t, state, CopyStatusFile, tc.copy)
			}
			h := ReadHealth(backups, state, tc.running, now)
			if got := codes(h); !slices.Equal(got, tc.want) {
				t.Fatalf("alerts = %v, want %v (%+v)", got, tc.want, h.Alerts)
			}
			if tc.name == "failed keeps previous" && !strings.Contains(h.Alerts[0].Message, "previous verified backup was kept") {
				t.Fatalf("failure did not say the previous backup was kept: %q", h.Alerts[0].Message)
			}
			if tc.name == "copy missing" && (!strings.Contains(h.Alerts[0].Message, "Last copied") || h.Copy.LastCopiedAt != 1767200000) {
				t.Fatalf("missing drive lost its last-copied time: %+v", h)
			}
		})
	}
}

func TestHealthUnreadableStateIsAnAlert(t *testing.T) {
	backups, state := t.TempDir(), t.TempDir()
	writeManifest(t, backups, nil)
	if err := os.Mkdir(filepath.Join(state, WALStatusFile), 0o700); err != nil {
		t.Fatal(err)
	}
	h := ReadHealth(backups, state, false, time.Unix(1767240000, 0))
	if !slices.Contains(codes(h), "status_unreadable") {
		t.Fatalf("unreadable wal status was silent: %+v", h.Alerts)
	}
}

func TestSummariseUsesHealthAlerts(t *testing.T) {
	now := time.Now()
	r := Report{Backup: BackupSpace{Level: LevelOK}, NewestBackupAt: now.Unix(),
		Health: BackupHealth{Alerts: []Alert{{Code: "wal_gap", Level: LevelCritical, Message: "Change file X is missing."}}}}
	Summarise(&r, true, now)
	if r.Level != LevelCritical || r.Headline != "Change file X is missing." {
		t.Fatalf("health alert not surfaced: %+v", r)
	}
}

func TestEarliestRestoreFallsBackSafely(t *testing.T) {
	m := Manifest{CreatedAt: 200}
	if m.EarliestRestoreAt() != 200 {
		t.Fatal("missing ended_at must fall back to the completion time")
	}
	m.Base.EndedAt = 150
	if m.EarliestRestoreAt() != 150 {
		t.Fatal("ended_at not used")
	}
	m.Base.EndedAt = 300
	if m.EarliestRestoreAt() != 200 {
		t.Fatal("an ended_at after completion is not trusted")
	}
}

func TestSafeBackupPath(t *testing.T) {
	for _, ok := range []string{"care-20260101-020000.dump.enc", "base-20260101-020000/base.tar.gz.enc"} {
		if !SafeBackupPath(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", ".hidden", "a\\b", "a/./b"} {
		if SafeBackupPath(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
