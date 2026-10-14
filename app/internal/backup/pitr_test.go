package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

const pitrCreated = 1767229200 // 2026-01-01 01:00:00 UTC

func writePITRFixture(t *testing.T, s *Store, verified bool) string {
	t.Helper()
	checks := map[string]string{}
	for _, c := range storage.RequiredChecks {
		checks[c] = "ok"
	}
	if !verified {
		checks["wal_chain"] = "failed"
	}
	m := map[string]any{
		"version": 1, "set": "20260101-010101", "created_at": pitrCreated, "started_at": pitrCreated - 600,
		"base":   map[string]any{"start_wal": "000000010000000000000004", "wal_segment_bytes": 16777216},
		"files":  []map[string]any{{"path": "base-20260101-010101/base.tar.gz.enc", "sha256": strings.Repeat("a", 64)}},
		"wal":    map[string]any{"dir": "wal", "from": "000000010000000000000004", "through": "000000010000000000000009"},
		"checks": checks,
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(s.BackupDir, storage.ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	writePITRWAL(t, s, "ok", pitrCreated+3000, "")
	if err := os.MkdirAll(filepath.Join(s.Dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "scripts", "pitr-stage.sh"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, key, err := GenerateRecoveryFile()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "recovery.pem")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

func writePITRWAL(t *testing.T, s *Store, state string, savedThrough int64, gap string) {
	t.Helper()
	dir := filepath.Join(s.Dir, storage.StateDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("state=%s\nsaved_through=%d\ngap=%s\n", state, savedThrough, gap)
	if err := os.WriteFile(filepath.Join(dir, storage.WALStatusFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPITRTarget(t *testing.T) {
	m := storage.Manifest{CreatedAt: pitrCreated}
	now := time.Unix(pitrCreated+3600, 0)
	saved := int64(pitrCreated + 1800) // 01:30:00 UTC
	if got, err := PITRTarget("latest", m, saved, now); err != nil || got != "latest" {
		t.Fatalf("latest: %q %v", got, err)
	}
	if got, err := PITRTarget("2026-01-01T06:30:00+05:30", m, saved, now); err != nil || got != "2026-01-01 01:00:00+00" {
		t.Fatalf("zone not normalised to UTC: %q %v", got, err)
	}
	if got, err := PITRTarget("2026-01-01T01:30:00Z", m, saved, now); err != nil || got != "2026-01-01 01:30:00+00" {
		t.Fatalf("saved-through bound is inclusive: %q %v", got, err)
	}
	// 01:30:01 is before now but after the last archived change: replay
	// could not reach it, so it must be refused rather than silently stop early.
	for _, bad := range []string{"", "yesterday", "2026-01-01 01:30:00", "2026-01-01T00:59:59Z", "2026-01-01T01:30:01Z",
		"2026-01-01T03:00:00Z", "latest'; drop"} {
		if _, err := PITRTarget(bad, m, saved, now); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSavedThroughNeverPrecedesFullCopyOrTrustsUnknownStatus(t *testing.T) {
	m := storage.Manifest{CreatedAt: pitrCreated}
	for _, tc := range []struct {
		wal  storage.WALStatus
		want int64
	}{
		{storage.WALStatus{State: "ok", SavedThrough: pitrCreated + 900}, pitrCreated + 900},
		{storage.WALStatus{State: "failing", SavedThrough: pitrCreated + 60}, pitrCreated + 60},
		{storage.WALStatus{State: "ok", SavedThrough: pitrCreated - 900}, pitrCreated},
		{storage.WALStatus{State: "unknown", SavedThrough: pitrCreated + 900}, pitrCreated},
		{storage.WALStatus{}, pitrCreated},
	} {
		if got := SavedThrough(m, tc.wal); got != tc.want {
			t.Errorf("%+v: got %d want %d", tc.wal, got, tc.want)
		}
	}
}

func TestPointInTimeRestoreRefusesUnreachableTargetsBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name, target, gap, want string
	}{
		{"after saved changes", "2026-01-01T01:55:00Z", "", "only saved through"},
		{"change-file gap", "latest", "000000010000000000000006", "is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, load, _ := newRestoreFixture(t)
			key := writePITRFixture(t, s, true)
			writePITRWAL(t, s, "ok", pitrCreated+1800, tc.gap)
			err := s.RestorePointInTime(tc.target, key, time.Unix(pitrCreated+7200, 0))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if calls := load().Calls; len(calls) != 0 {
				t.Fatalf("an unreachable restore started helpers: %v", calls)
			}
		})
	}
}

func TestPointInTimeRestoreRunsThroughStagedSwap(t *testing.T) {
	s, load, save := newRestoreFixture(t)
	key := writePITRFixture(t, s, true)
	state := load()
	state.Fail = "activation"
	save(state)
	now := time.Unix(pitrCreated+3600, 0)
	if err := s.RestorePointInTime("2026-01-01T01:30:00Z", key, now); err == nil {
		t.Fatal("expected the synthetic activation failure")
	}
	state = load()
	var steps []string
	var pitr []string
	for _, call := range state.Calls {
		if call.Args[0] != "run" {
			continue
		}
		name := call.Args[slices.Index(call.Args, "--name")+1]
		_, suffix, _ := strings.Cut(name, "-restore-")
		steps = append(steps, suffix[25:])
		if suffix[25:] == "pitr" {
			pitr = call.Args
		}
	}
	if !slices.Equal(steps[:1], []string{"pitr"}) || slices.Contains(steps, "preflight") ||
		slices.Contains(steps, "replace-files") || !slices.Contains(steps, "stage-db") || !slices.Contains(steps, "swap-db") {
		t.Fatalf("point-in-time restore skipped the staged swap: %v", steps)
	}
	if slices.Index(steps, "pitr") > slices.Index(steps, "stage-db") {
		t.Fatalf("staged database was built before replay finished: %v", steps)
	}
	tail := pitr[len(pitr)-4:]
	if !slices.Equal(tail, []string{"fixture-backup", "sh", "/scripts/pitr-stage.sh", "2026-01-01 01:30:00+00"}) {
		t.Fatalf("pitr helper invocation = %v", tail)
	}
	joined := strings.Join(pitr, " ")
	for _, want := range []string{"target=/backups,readonly", "target=/scripts,readonly", "target=/keys,readonly",
		"source=" + key + ",target=/restore-key.pem,readonly", "--network none"} {
		if !strings.Contains(joined, want) {
			t.Errorf("pitr helper is missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "PRIVATE KEY") || strings.Contains(joined, "SyntheticDatabaseSecret") {
		t.Fatal("a secret appeared in Docker arguments")
	}
	if state.Databases.Live.OID != "200" || state.Files != "original-files" {
		t.Fatalf("swap did not happen or uploaded files changed: %+v", state)
	}
	if j, err := s.readRestoreJournal(); err != nil || j.Phase != "committed" || j.WithFiles {
		t.Fatalf("journal = %+v, %v", j, err)
	}
}

func TestPointInTimeReplayFailureKeepsLiveDatabase(t *testing.T) {
	s, load, save := newRestoreFixture(t)
	key := writePITRFixture(t, s, true)
	state := load()
	state.Fail = "pitr"
	save(state)
	if err := s.RestorePointInTime("latest", key, time.Unix(pitrCreated+60, 0)); err == nil {
		t.Fatal("replay failure was ignored")
	}
	state = load()
	if state.Databases.Live.OID != "100" || state.Databases.Staged != nil || state.Databases.Old != nil || state.Stopped {
		t.Fatalf("replay failure touched the live clinic: %+v", state)
	}
	for _, call := range state.Calls {
		if call.Args[0] == "compose" && slices.Contains(call.Args, "stop") {
			t.Fatal("writers were stopped before the replay finished")
		}
	}
	if j, err := s.readRestoreJournal(); err != nil || j == nil || j.Phase != "staging" {
		t.Fatalf("failure did not keep recovery metadata: %+v %v", j, err)
	}
}

func TestPointInTimeRestoreRefusesUnverifiedOrMissingInputs(t *testing.T) {
	s, load, _ := newRestoreFixture(t)
	now := time.Unix(pitrCreated+60, 0)
	if err := s.RestorePointInTime("latest", "", now); err == nil || !strings.Contains(err.Error(), "no verified backup") {
		t.Fatalf("missing manifest: %v", err)
	}
	key := writePITRFixture(t, s, false)
	if err := s.RestorePointInTime("latest", key, now); err == nil || !strings.Contains(err.Error(), "no verified backup") {
		t.Fatalf("unverified manifest: %v", err)
	}
	key = writePITRFixture(t, s, true)
	if err := s.RestorePointInTime("latest", "", now); err == nil {
		t.Fatal("restored without a recovery file")
	}
	if err := os.Remove(filepath.Join(s.Dir, "scripts", "pitr-stage.sh")); err != nil {
		t.Fatal(err)
	}
	if err := s.RestorePointInTime("latest", key, now); err == nil {
		t.Fatal("restored without the helper script")
	}
	if len(load().Calls) != 0 {
		t.Fatal("a refused restore still ran Docker")
	}
}
