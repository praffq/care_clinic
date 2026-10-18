package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func testRecoveryKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// backupApp is a set-up clinic with a recovery key in its state folder and
// scripted dialogs.
func backupApp(t *testing.T) (*App, *backupDialogs, []byte) {
	t.Helper()
	a := settingsApp(t)
	a.stateDir = filepath.Join(t.TempDir(), "clinic")
	key := testRecoveryKey(t)
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.stateDir, stack.BackupRecoveryKey), key, 0o600); err != nil {
		t.Fatal(err)
	}
	none := func(wruntime.OpenDialogOptions) (string, error) { return "", nil }
	d := &backupDialogs{
		save: func(wruntime.SaveDialogOptions) (string, error) { return "", nil },
		dir:  none, file: none,
	}
	a.backupDialogs = d
	return a, d, key
}

func writeHostSet(t *testing.T, root, id string, created time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("encrypted dump")
	sum := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(dir, "db.dump.cms"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(protocol.BackupManifest{ID: id, CreatedAt: created, Files: []protocol.BackupFile{
		{Name: "db.dump.cms", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])},
	}})
	if err := os.WriteFile(filepath.Join(dir, protocol.BackupManifestName), m, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// waitJob returns once the background job started by a.run has finished.
func TestExportBackupRecoveryKey(t *testing.T) {
	a, d, key := backupApp(t)
	backupDir := t.TempDir()
	a.service.(*fakeAPI).set(func(st *api.ServiceStatus) { st.Backups = &api.BackupStatus{Dir: backupDir} })
	var target string
	d.save = func(o wruntime.SaveDialogOptions) (string, error) {
		if !strings.Contains(o.DefaultFilename, "backup-recovery-key") || !strings.HasSuffix(o.DefaultFilename, ".pem") {
			t.Errorf("filename = %q", o.DefaultFilename)
		}
		return target, nil
	}
	if _, err := a.ExportBackupRecoveryKey("WrongPassword123"); err == nil {
		t.Fatal("wrong admin password accepted")
	}
	if path, err := a.ExportBackupRecoveryKey(settingsPassword); err != nil || path != "" {
		t.Fatalf("cancelled: %q, %v", path, err)
	}
	for _, unsafe := range []string{filepath.Join(a.stateDir, "k.pem"), filepath.Join(backupDir, "k.pem")} {
		target = unsafe
		if _, err := a.ExportBackupRecoveryKey(settingsPassword); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("unsafe location %s: %v", unsafe, err)
		}
	}
	target = filepath.Join(t.TempDir(), "k.pem")
	path, err := a.ExportBackupRecoveryKey(settingsPassword)
	if err != nil || path != target {
		t.Fatalf("export: %q, %v", path, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("saved key differs: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if _, err := a.ExportBackupRecoveryKey(settingsPassword); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if err := os.Remove(filepath.Join(a.stateDir, stack.BackupRecoveryKey)); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(t.TempDir(), "k.pem")
	if _, err := a.ExportBackupRecoveryKey(settingsPassword); err == nil || !strings.Contains(err.Error(), "no backup recovery key") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestChooseBackupToRestore(t *testing.T) {
	a, d, key := backupApp(t)
	folder := t.TempDir()
	created := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	writeHostSet(t, folder, "daily-20261017T000000Z", created.Add(-24*time.Hour))
	newest := writeHostSet(t, folder, "daily-20261018T000000Z", created)

	if c, err := a.ChooseBackupToRestore(false); err != nil || c != nil {
		t.Fatalf("cancelled: %+v, %v", c, err)
	}
	d.dir = func(wruntime.OpenDialogOptions) (string, error) { return t.TempDir(), nil }
	if _, err := a.ChooseBackupToRestore(false); err == nil || !strings.Contains(err.Error(), "no backup") {
		t.Fatalf("empty folder: %v", err)
	}
	d.dir = func(wruntime.OpenDialogOptions) (string, error) { return folder, nil }
	c, err := a.ChooseBackupToRestore(false)
	if err != nil || c == nil || c.SetDir != newest || c.ID != "daily-20261018T000000Z" || !c.CreatedAt.Equal(created) || c.KeyPath != "" {
		t.Fatalf("choice = %+v, %v", c, err)
	}

	// From another computer: its recovery key is chosen too.
	if c, err := a.ChooseBackupToRestore(true); err != nil || c != nil {
		t.Fatalf("cancelled key: %+v, %v", c, err)
	}
	notKey := filepath.Join(t.TempDir(), "notes.pem")
	if err := os.WriteFile(notKey, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.file = func(wruntime.OpenDialogOptions) (string, error) { return notKey, nil }
	if _, err := a.ChooseBackupToRestore(true); err == nil || !strings.Contains(err.Error(), "recovery key") {
		t.Fatalf("bad key accepted: %v", err)
	}
	other := filepath.Join(t.TempDir(), "old.pem")
	if err := os.WriteFile(other, key, 0o600); err != nil {
		t.Fatal(err)
	}
	d.file = func(wruntime.OpenDialogOptions) (string, error) { return other, nil }
	if c, err := a.ChooseBackupToRestore(true); err != nil || c.KeyPath != other {
		t.Fatalf("choice = %+v, %v", c, err)
	}
}

func TestRestoreBackup(t *testing.T) {
	a, _, key := backupApp(t)
	set := writeHostSet(t, t.TempDir(), "daily-20261018T000000Z", time.Now())
	type call struct {
		dir string
		key []byte
	}
	var calls []call
	var fail error
	a.restoreSet = func(_ context.Context, dir string, k []byte) error {
		calls = append(calls, call{dir, k})
		return fail
	}
	if err := a.RestoreBackup(set, "", "WrongPassword123"); err == nil {
		t.Fatal("wrong admin password accepted")
	}
	if err := a.RestoreBackup(filepath.Join(t.TempDir(), "daily-20261018T000000Z"), "", settingsPassword); err == nil {
		t.Fatal("missing set accepted")
	}
	if err := a.RestoreBackup(set, "", settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if len(calls) != 1 || calls[0].dir != set || !bytes.Equal(calls[0].key, key) {
		t.Fatalf("calls = %+v", calls)
	}

	other := testRecoveryKey(t)
	otherPath := filepath.Join(t.TempDir(), "old.pem")
	if err := os.WriteFile(otherPath, other, 0o600); err != nil {
		t.Fatal(err)
	}
	fail = errors.New("db.restore failed")
	if err := a.RestoreBackup(set, otherPath, settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if len(calls) != 2 || !bytes.Equal(calls[1].key, other) {
		t.Fatalf("restore used the wrong key: %+v", calls)
	}
}

func TestBackupCopyDir(t *testing.T) {
	a, d, _ := backupApp(t)
	usb := t.TempDir()
	if dir, err := a.SetBackupCopyDir(settingsPassword); err != nil || dir != "" {
		t.Fatalf("cancelled: %q, %v", dir, err)
	}
	d.dir = func(wruntime.OpenDialogOptions) (string, error) { return usb, nil }
	if _, err := a.SetBackupCopyDir("WrongPassword123"); err == nil {
		t.Fatal("wrong admin password accepted")
	}
	if dir, err := a.SetBackupCopyDir(settingsPassword); err != nil || dir != usb {
		t.Fatalf("set: %q, %v", dir, err)
	}
	f := a.service.(*fakeAPI)
	copyDir := func(i int) string {
		u := f.Updates()
		if len(u) <= i || u[i].BackupCopyDir == nil {
			t.Fatalf("updates = %+v", u)
		}
		return *u[i].BackupCopyDir
	}
	if got := copyDir(0); got != usb {
		t.Fatalf("set %q", got)
	}
	if err := a.ClearBackupCopyDir("WrongPassword123"); err == nil {
		t.Fatal("wrong admin password accepted")
	}
	if err := a.ClearBackupCopyDir(settingsPassword); err != nil {
		t.Fatal(err)
	}
	if got := copyDir(1); got != "" {
		t.Fatalf("cleared to %q", got)
	}
}

func TestCopyBackupsNow(t *testing.T) {
	a, _, _ := backupApp(t)
	f := a.service.(*fakeAPI)
	f.set(func(st *api.ServiceStatus) {
		st.Backups = &api.BackupStatus{Copy: &api.BackupCopyStatus{Dir: "/Volumes/USB",
			LastError: "stat /Volumes/USB: no such file", Message: "The extra backup copy isn't connected; last copied never"}}
	})
	if err := a.CopyBackupsNow(); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Fatalf("err = %v", err)
	}
	f.set(func(st *api.ServiceStatus) {
		st.Backups.Copy = &api.BackupCopyStatus{Dir: "/Volumes/USB", Connected: true}
	})
	if err := a.CopyBackupsNow(); err != nil {
		t.Fatal(err)
	}
	if f.copies != 2 {
		t.Fatalf("copies = %d", f.copies)
	}
}

func TestPruneEngineImages(t *testing.T) {
	a, _, _ := backupApp(t)
	calls := 0
	a.pruneImages = func(context.Context) (protocol.ImagePruneResult, error) {
		calls++
		return protocol.ImagePruneResult{Removed: 3, Skipped: 1}, nil
	}
	if err := a.PruneEngineImages(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	cfg := a.loadConfig()
	cfg.SetupDone = false
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.PruneEngineImages(); err == nil {
		t.Fatal("pruned before setup")
	}
}
