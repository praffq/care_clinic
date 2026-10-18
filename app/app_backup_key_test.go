package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func smallBackupKey() ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "care-backup"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil
}

type setupDialogs struct {
	save, file string
}

func setupBackupApp(t *testing.T) (*App, *fakeAPI, *setupDialogs) {
	t.Helper()
	a, f := serviceApp(t)
	a.stateDir = filepath.Join(t.TempDir(), "clinic")
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := generateBackupKey
	generateBackupKey = smallBackupKey
	t.Cleanup(func() { generateBackupKey = old })
	d := &setupDialogs{}
	a.backupDialogs = &backupDialogs{
		save: func(wruntime.SaveDialogOptions) (string, error) { return d.save, nil },
		dir:  func(wruntime.OpenDialogOptions) (string, error) { return "", nil },
		file: func(wruntime.OpenDialogOptions) (string, error) { return d.file, nil },
	}
	return a, f, d
}

func saveAndVerify(t *testing.T, a *App, d *setupDialogs, backupDir string) string {
	t.Helper()
	d.save = filepath.Join(t.TempDir(), "recovery.pem")
	if ok, err := a.SaveSetupBackupRecovery(backupDir); err != nil || !ok {
		t.Fatalf("save = %v, %v", ok, err)
	}
	d.file = d.save
	if ok, err := a.VerifySetupBackupRecovery(backupDir); err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	return d.save
}

func TestSetupBackupRecoverySavesThenVerifies(t *testing.T) {
	a, _, d := setupBackupApp(t)
	d.save = filepath.Join(t.TempDir(), "recovery.pem")
	if ok, err := a.SaveSetupBackupRecovery(""); err != nil || !ok {
		t.Fatalf("save = %v, %v", ok, err)
	}
	st, err := a.GetSetupBackupStatus()
	if err != nil || !st.Saved || st.Verified || st.Problem != "" || st.Path != d.save {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := stack.ReadRecoveryKey(d.save); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.SaveSetupBackupRecovery(""); err == nil || ok {
		t.Fatal("a second save replaced the key without asking")
	}
	d.file = d.save
	if ok, err := a.VerifySetupBackupRecovery(""); err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if st, _ = a.GetSetupBackupStatus(); !st.Verified || st.Problem != "" {
		t.Fatalf("status = %+v", st)
	}
	d.file = ""
	if ok, err := a.VerifySetupBackupRecovery(""); err != nil || ok {
		t.Fatalf("cancelled verify = %v, %v", ok, err)
	}
	if st, _ = a.GetSetupBackupStatus(); !st.Verified {
		t.Fatal("cancelling cleared the check")
	}
	onboardingJobUnlocked(t, a)
}

func TestSetupBackupRecoveryRejectsAnotherKey(t *testing.T) {
	a, _, d := setupBackupApp(t)
	saveAndVerify(t, a, d, "")
	other := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(other, testRecoveryKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	d.file = other
	if ok, err := a.VerifySetupBackupRecovery(""); err == nil || ok || !strings.Contains(err.Error(), "match") {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if st, _ := a.GetSetupBackupStatus(); st.Verified {
		t.Fatal("a failed check kept the earlier one")
	}
}

func TestSetupBackupStatusNoticesAMissingFile(t *testing.T) {
	a, _, d := setupBackupApp(t)
	path := saveAndVerify(t, a, d, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.GetSetupBackupStatus(); st.Verified || st.Problem != "missing" {
		t.Fatalf("status = %+v", st)
	}
	if err := os.WriteFile(path, testRecoveryKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.GetSetupBackupStatus(); st.Verified || st.Problem != "mismatch" {
		t.Fatalf("status = %+v", st)
	}
}

func TestReplaceSetupBackupRecoveryNeedsANewCheck(t *testing.T) {
	a, _, d := setupBackupApp(t)
	first := saveAndVerify(t, a, d, "")
	d.save = filepath.Join(t.TempDir(), "new.pem")
	if ok, err := a.ReplaceSetupBackupRecovery(""); err != nil || !ok {
		t.Fatalf("replace = %v, %v", ok, err)
	}
	if st, _ := a.GetSetupBackupStatus(); st.Verified || st.Path != d.save {
		t.Fatalf("status = %+v", st)
	}
	d.file = first
	if ok, err := a.VerifySetupBackupRecovery(""); err == nil || ok {
		t.Fatal("the replaced file still passed")
	}
}

func TestSetupBackupRecoveryStaysOutOfCAREAndBackupFolders(t *testing.T) {
	a, _, d := setupBackupApp(t)
	backupDir := t.TempDir()
	for _, path := range []string{filepath.Join(a.stateDir, "key.pem"), filepath.Join(backupDir, "key.pem")} {
		d.save = path
		if ok, err := a.SaveSetupBackupRecovery(backupDir); err == nil || ok {
			t.Fatalf("saved to %s", path)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s was written", path)
		}
	}
}

func TestSetupBackupRecoveryLocksOnceInstallStarts(t *testing.T) {
	a, _, d := setupBackupApp(t)
	if err := a.saveConfig(Config{Role: roleServer, AdminPwHash: "x"}); err != nil {
		t.Fatal(err)
	}
	d.save = filepath.Join(t.TempDir(), "recovery.pem")
	if _, err := a.SaveSetupBackupRecovery(""); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateBackupDir(t *testing.T) {
	a, _, _ := setupBackupApp(t)
	good := t.TempDir()
	file := filepath.Join(good, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	used := t.TempDir()
	writeHostSet(t, used, "daily-20260101T000000Z", time.Now())
	for dir, want := range map[string]string{
		good:                                 "",
		filepath.Join(good, "missing"):       "isn't there",
		file:                                 "file, not a folder",
		used:                                 "another CARE installation",
		"relative/backups":                   "absolute",
		a.stateDir:                           "CARE's own folders",
		filepath.Join(a.stateDir, "backups"): "CARE's own folders",
		"":                                   "service's backup folder",
	} {
		got := a.ValidateBackupDir(dir)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("ValidateBackupDir(%q) = %q, want %q", dir, got, want)
		}
	}
	if s := a.BackupDirSpace(good); s.Dir != good || s.Total == 0 || s.Need == 0 {
		t.Fatalf("space = %+v", s)
	}
}

func TestValidateBackupDirKeepsTheRecoveryFileOut(t *testing.T) {
	a, _, d := setupBackupApp(t)
	dir := t.TempDir()
	d.save = filepath.Join(dir, "recovery.pem")
	if ok, err := a.SaveSetupBackupRecovery(""); err != nil || !ok {
		t.Fatalf("save = %v, %v", ok, err)
	}
	if got := a.ValidateBackupDir(dir); !strings.Contains(got, "separate") {
		t.Fatalf("ValidateBackupDir = %q", got)
	}
}

func TestPrepareSetupBackupKeyNeedsACheckedFile(t *testing.T) {
	a, _, d := setupBackupApp(t)
	dir := t.TempDir()
	if _, err := a.prepareSetupBackupKey(dir); err == nil || !strings.Contains(err.Error(), "save") {
		t.Fatalf("err = %v", err)
	}
	d.save = filepath.Join(t.TempDir(), "recovery.pem")
	if _, err := a.SaveSetupBackupRecovery(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareSetupBackupKey(dir); err == nil || !strings.Contains(err.Error(), "check") {
		t.Fatalf("err = %v", err)
	}
	d.file = d.save
	if _, err := a.VerifySetupBackupRecovery(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareSetupBackupKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("accepted a missing backup folder")
	}
	if _, err := a.prepareSetupBackupKey(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRunSetupRefusesWithoutACheckedRecoveryFile(t *testing.T) {
	a, _, _ := setupBackupApp(t)
	if err := a.RunSetup("care", settingsPassword, t.TempDir()); err == nil || !strings.Contains(err.Error(), "recovery file") {
		t.Fatalf("err = %v", err)
	}
	if cfg := a.loadConfig(); cfg.AdminPwHash != "" {
		t.Fatal("setup started")
	}
	onboardingJobUnlocked(t, a)
}

func TestApplySetupBackupsHandsTheFolderAndKeyToTheClinic(t *testing.T) {
	a, f, d := setupBackupApp(t)
	dir := t.TempDir()
	saved := saveAndVerify(t, a, d, dir)
	oldKey := testRecoveryKey(t)
	if err := os.WriteFile(filepath.Join(a.stateDir, stack.BackupRecoveryKey), oldKey, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := a.prepareSetupBackupKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.applySetupBackups(context.Background(), dir, b); err != nil {
		t.Fatal(err)
	}
	u := f.Updates()
	if len(u) != 1 || u[0].BackupDir == nil || *u[0].BackupDir != dir {
		t.Fatalf("config updates = %+v", u)
	}
	key, err := stack.ReadRecoveryKey(filepath.Join(a.stateDir, stack.BackupRecoveryKey))
	if err != nil {
		t.Fatal(err)
	}
	userKey, _ := os.ReadFile(saved)
	if string(key) != string(userKey) {
		t.Fatal("the clinic's recovery key isn't the file the operator saved")
	}
	cert, err := os.ReadFile(filepath.Join(a.stateDir, stack.BackupCert))
	if err != nil || matchRecoveryKey(cert, userKey) != nil {
		t.Fatalf("certificate doesn't match the saved key: %v", err)
	}
	kept, _ := filepath.Glob(filepath.Join(a.stateDir, stack.BackupRecoveryKey+".replaced-*"))
	if len(kept) != 1 {
		t.Fatalf("earlier key kept as %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != string(oldKey) {
		t.Fatal("the earlier key changed")
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, SetupBackupFile)); !os.IsNotExist(err) {
		t.Fatalf("pending key left behind: %v", err)
	}
	if err := a.applySetupBackups(context.Background(), "", b); err != nil {
		t.Fatal(err)
	}
	if kept, _ = filepath.Glob(filepath.Join(a.stateDir, "*.replaced-*")); len(kept) != 1 {
		t.Fatalf("a retry moved the installed key: %v", kept)
	}
}
