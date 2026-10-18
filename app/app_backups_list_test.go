package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type fakeBackups struct {
	mu     sync.Mutex
	cfg    api.ServiceConfig
	list   api.BackupList
	status api.BackupStatus
	runErr error
}

func (f *fakeBackups) Config(context.Context) (api.ServiceConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

func (f *fakeBackups) UpdateConfig(_ context.Context, u api.ServiceConfigUpdate) (api.ServiceConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u.BackupDir != nil {
		if !filepath.IsAbs(*u.BackupDir) {
			return f.cfg, errors.New("backup_dir must be absolute")
		}
		f.cfg.BackupDir = *u.BackupDir
	}
	return f.cfg, nil
}

func (f *fakeBackups) Backups(context.Context) (api.BackupList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, nil
}

func (f *fakeBackups) BackupStatus(context.Context) (api.BackupStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeBackups) RunBackup(_ context.Context, onFrame func(protocol.Frame)) (api.BackupStatus, error) {
	onFrame(protocol.Frame{Progress: &protocol.Progress{State: "dumping", Bytes: 50, Total: 100}})
	onFrame(protocol.Frame{Log: &protocol.LogLine{Line: "pg_dump done"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.runErr
}

var (
	t1 = time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC)
	t2 = t1.Add(24 * time.Hour)
	t3 = t2.Add(24 * time.Hour)
)

func backupsApp(t *testing.T) (*App, *fakeBackups, *events) {
	t.Helper()
	a, ev := panelApp(t)
	b := &fakeBackups{
		cfg: api.ServiceConfig{BackupDir: "/Backups/care"},
		list: api.BackupList{Dir: "/Backups/care",
			Host: []api.HostBackup{{ID: "20260501T020000Z", CreatedAt: t1, Size: 300}, {ID: "20260502T020000Z", CreatedAt: t2, Size: 400}},
			Engine: []protocol.BackupSet{
				{ID: "20260502T020000Z", Complete: true, CreatedAt: t2},
				{ID: "20260503T020000Z", Complete: true, CreatedAt: t3, Files: []protocol.BackupFile{{Size: 10}, {Size: 5}}},
				{ID: "20260503T030000Z", CreatedAt: t3.Add(time.Hour)},
			}},
		status: api.BackupStatus{Dir: "/Backups/care", LastExported: "20260502T020000Z", VerifiedAt: t2.Add(time.Minute), Pending: 1},
	}
	a.hooks.backups = func() (backupService, error) { return b, nil }
	return a, b, ev
}

func TestListBackupsMergesHostAndEngineSets(t *testing.T) {
	a, _, _ := backupsApp(t)
	st, err := a.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if st.Dir != "/Backups/care" || st.Status.Pending != 1 || len(st.Sets) != 3 {
		t.Fatalf("state = %+v", st)
	}
	want := []BackupSet{
		{ID: "20260503T020000Z", CreatedAt: t3, Size: 15},
		{ID: "20260502T020000Z", CreatedAt: t2, Size: 400, Dir: filepath.Join("/Backups/care", "20260502T020000Z"), Copied: true, Latest: true, VerifiedAt: t2.Add(time.Minute)},
		{ID: "20260501T020000Z", CreatedAt: t1, Size: 300, Dir: filepath.Join("/Backups/care", "20260501T020000Z"), Copied: true},
	}
	for i, s := range st.Sets {
		if s != want[i] {
			t.Errorf("set %d = %+v, want %+v", i, s, want[i])
		}
	}
}

func TestBackUpNowReportsProgressAndCopyFailures(t *testing.T) {
	a, b, ev := backupsApp(t)
	if err := a.BackUpNow(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if code := ev.done(t, backupJob); code != 0 {
		t.Fatalf("care-done = %d: %v", code, ev.errors())
	}
	p := ev.named("backup-progress")
	if len(p) != 2 || p[1][0] != (BackupProgress{Job: backupJob, State: "dumping", Bytes: 50, Total: 100}) {
		t.Fatalf("progress = %v", p)
	}

	b.status.LastError = "disk full"
	if err := a.BackUpNow(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if errs := ev.errors(); len(errs) != 1 || !strings.HasPrefix(errs[0], "The backup didn't finish") || !strings.Contains(errs[0], "disk full") {
		t.Fatalf("care-error = %v", errs)
	}
}

func TestRestoreBackupReportsProgress(t *testing.T) {
	a, _, ev := backupsApp(t)
	a.stateDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(a.stateDir, stack.BackupRecoveryKey), testRecoveryKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	set := writeHostSet(t, root, "20260502T020000Z", t2)
	var restored string
	a.restoreSet = func(_ context.Context, dir string, _ []byte) error {
		restored = dir
		a.backupFrames(restoreJob)(protocol.Frame{Progress: &protocol.Progress{State: "restoring", Message: "database"}})
		return nil
	}
	if err := a.RestoreBackup(set, "", settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if restored != set {
		t.Fatalf("restored %q", restored)
	}
	p := ev.named("backup-progress")
	if len(p) != 2 || p[0][0].(BackupProgress).Job != restoreJob || p[1][0] != (BackupProgress{Job: restoreJob, State: "restoring", Message: "database"}) {
		t.Fatalf("progress = %v", p)
	}
}

func TestChooseBackupFolder(t *testing.T) {
	a, b, _ := backupsApp(t)
	var asked string
	pick := ""
	a.backupDialogs = &backupDialogs{dir: func(o wruntime.OpenDialogOptions) (string, error) { asked = o.DefaultDirectory; return pick, nil }}
	if _, err := a.ChooseBackupFolder("wrong"); err == nil {
		t.Fatal("changed the folder with a wrong admin password")
	}
	if dir, err := a.ChooseBackupFolder(settingsPassword); err != nil || dir != "" || asked != "/Backups/care" {
		t.Fatalf("cancelled = %q, %v (asked from %q)", dir, err, asked)
	}
	vol := filepath.Join(t.TempDir(), "Clinic", "backups") // absolute on every OS
	pick = vol
	if dir, err := a.ChooseBackupFolder(settingsPassword); err != nil || dir != pick || b.cfg.BackupDir != pick {
		t.Fatalf("chose = %q, %v", dir, err)
	}
	pick = "relative"
	if _, err := a.ChooseBackupFolder(settingsPassword); err == nil || b.cfg.BackupDir != vol {
		t.Fatalf("accepted a relative folder: %v", err)
	}
}

func TestSystemInfo(t *testing.T) {
	a, _ := panelApp(t)
	a.pins.AppVersion = "0.2.0"
	f := newFakeAPI(t)
	f.status.Version = "0.2.0"
	f.status.Engine.EngineVersion = "0.1.4"
	f.slotList = api.SlotList{Pointer: slots.Pointer{Current: "0.1.4", Previous: "0.1.3"},
		Slots: []slots.Slot{{Version: "0.1.3", DataSchema: 1}, {Version: "0.1.4", DataSchema: 2, Arch: "arm64"}}}
	a.service = f
	info := a.SystemInfo()
	if info.Error != "" || info.DesktopVersion != "0.2.0" || info.ServiceVersion != "0.2.0" || info.Engine == nil || info.Engine.EngineVersion != "0.1.4" || info.Engine.Stack != nil {
		t.Fatalf("info = %+v", info)
	}
	if info.Current != "0.1.4" || info.Fallback != "0.1.3" || len(info.Slots) != 2 || !info.Slots[0].Fallback || !info.Slots[1].Current || info.Slots[1].DataSchema != 2 {
		t.Fatalf("slots = %+v", info.Slots)
	}

	f.statusErr = errors.New("connection refused")
	if info := a.SystemInfo(); !strings.Contains(info.Error, "connection refused") || info.DesktopVersion != "0.2.0" {
		t.Fatalf("unreachable = %+v", info)
	}
}
