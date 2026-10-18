package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/bcrypt"
)

func setupRecoveryApp(t *testing.T) *App {
	t.Helper()
	a := settingsApp(t)
	a.cfg = Config{Role: roleServer, MDNSName: "care.local"}
	a.stateDir = filepath.Join(t.TempDir(), "clinic")
	return a
}

func setupStatus(t *testing.T, a *App) SetupRecoveryStatus {
	t.Helper()
	status, err := a.GetSetupRecoveryStatus()
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func saveSetupCodes(t *testing.T, a *App) (string, []string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "codes.txt")
	path, err := a.saveSetupRecoveryCodesWith(func(wruntime.SaveDialogOptions) (string, error) { return target, nil })
	if err != nil || path != target {
		t.Fatalf("save: %q, %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, sheetCodes(t, data)
}

func finishSetup(t *testing.T, a *App, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.AdminPwHash = string(hash)
	a.cfg.SetupDone = true
}

func TestSetupRecoveryStatusBeforeSaving(t *testing.T) {
	a := setupRecoveryApp(t)
	if status := setupStatus(t, a); status.CodesSaved || status.CodesPath != "" || status.CodesProblem != "" {
		t.Fatalf("status without codes: %+v", status)
	}
	a.cfg.Role = roleClient
	if _, err := a.GetSetupRecoveryStatus(); err == nil {
		t.Fatal("client exposed setup recovery status")
	}
}

func TestSetupRecoveryCodesWorkAfterInstall(t *testing.T) {
	a := setupRecoveryApp(t)
	var options wruntime.SaveDialogOptions
	target := ""
	choose := func(opts wruntime.SaveDialogOptions) (string, error) { options = opts; return target, nil }
	if path, err := a.saveSetupRecoveryCodesWith(choose); err != nil || path != "" {
		t.Fatalf("cancelled dialog: %q, %v", path, err)
	}
	if !strings.HasPrefix(options.DefaultFilename, "CARE-care.local-clinic-admin-codes-") || !strings.HasSuffix(options.DefaultFilename, ".txt") {
		t.Fatalf("filename = %q", options.DefaultFilename)
	}
	if setupStatus(t, a).CodesSaved {
		t.Fatal("cancelled dialog saved codes")
	}
	path, codes := saveSetupCodes(t, a)
	if status := setupStatus(t, a); !status.CodesSaved || status.CodesPath != path || status.CodesProblem != "" {
		t.Fatalf("status after saving: %+v", status)
	}
	finishSetup(t, a, "ChosenLater123")
	if n := recoveryCount(t, a); n != recoveryCodeCount {
		t.Fatalf("remaining after install = %d", n)
	}
	if err := a.ResetAdminPassword(codes[0], "ResetDesktop123"); err != nil {
		t.Fatal("setup code didn't reset the installed clinic's password:", err)
	}
	if !a.VerifyAdminPassword("ResetDesktop123") {
		t.Fatal("password not reset")
	}
	if n := recoveryCount(t, a); n != recoveryCodeCount-1 {
		t.Fatalf("remaining after reset = %d", n)
	}
}

func TestSetupRecoveryNewSetReplacesTheOld(t *testing.T) {
	a := setupRecoveryApp(t)
	_, old := saveSetupCodes(t, a)
	_, codes := saveSetupCodes(t, a)
	finishSetup(t, a, settingsPassword)
	if err := a.ResetAdminPassword(old[0], "ResetDesktop123"); err == nil {
		t.Fatal("replaced setup codes still work")
	}
	if err := a.ResetAdminPassword(codes[0], "ResetDesktop123"); err != nil {
		t.Fatal(err)
	}
}

func TestSetupRecoveryRefusesUnsafeLocations(t *testing.T) {
	a := setupRecoveryApp(t)
	backups := t.TempDir()
	a.service.(*fakeAPI).set(func(st *api.ServiceStatus) { st.Backups = &api.BackupStatus{Dir: backups} })
	for _, unsafe := range []string{
		filepath.Join(a.installDir(), "codes.txt"),
		filepath.Join(filepath.Dir(a.configPath()), "codes.txt"),
		filepath.Join(a.stateDir, "codes.txt"),
		filepath.Join(backups, "codes.txt"),
	} {
		if _, err := a.saveSetupRecoveryCodesWith(func(wruntime.SaveDialogOptions) (string, error) { return unsafe, nil }); err == nil {
			t.Fatalf("unsafe location accepted: %s", unsafe)
		}
	}
	if setupStatus(t, a).CodesSaved {
		t.Fatal("refused saves stored codes")
	}
}

func TestSetupRecoveryLockedOnceInstallStarts(t *testing.T) {
	never := func(wruntime.SaveDialogOptions) (string, error) {
		t.Fatal("save dialog opened after installation started")
		return "", nil
	}
	for name, change := range map[string]func(*Config){
		"password set": func(c *Config) { c.AdminPwHash = "hash" },
		"installed":    func(c *Config) { c.SetupDone = true },
		"removing":     func(c *Config) { c.Removing = true },
	} {
		t.Run(name, func(t *testing.T) {
			a := setupRecoveryApp(t)
			saveSetupCodes(t, a)
			change(&a.cfg)
			if _, err := a.saveSetupRecoveryCodesWith(never); err == nil || !strings.Contains(err.Error(), "locked") {
				t.Fatalf("save after install started: %v", err)
			}
			if _, err := a.setupRecoveryCodesPath(); err == nil {
				t.Fatal("codes opened after install started")
			}
		})
	}
}

func TestSetupRecoveryReportsAMissingOrChangedSheet(t *testing.T) {
	a := setupRecoveryApp(t)
	path, codes := saveSetupCodes(t, a)
	if got, err := a.setupRecoveryCodesPath(); err != nil || got != path {
		t.Fatalf("codes path: %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("[ ] "+strings.Join(codes[:5], "\n[ ] ")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if problem := setupStatus(t, a).CodesProblem; problem != "mismatch" {
		t.Fatalf("edited sheet: %q", problem)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if problem := setupStatus(t, a).CodesProblem; problem != "missing" {
		t.Fatalf("deleted sheet: %q", problem)
	}
	if _, err := a.setupRecoveryCodesPath(); err == nil {
		t.Fatal("missing sheet opened")
	}
	saveSetupCodes(t, a)
	if status := setupStatus(t, a); !status.CodesSaved || status.CodesProblem != "" {
		t.Fatalf("fresh set: %+v", status)
	}
}

func TestSetupRecoveryCodesSurviveRunSetup(t *testing.T) {
	a := setupRecoveryApp(t)
	a.nameCheck = func(string) error { return nil }
	_, codes := saveSetupCodes(t, a)
	if err := a.RunSetup("care", "ChosenLater123"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !a.jobMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("setup job didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.jobMu.Unlock()
	if a.loadConfig().AdminPwHash == "" {
		t.Fatal("RunSetup didn't store the admin password")
	}
	if n := recoveryCount(t, a); n != recoveryCodeCount {
		t.Fatalf("RunSetup changed the pending recovery codes: %d left", n)
	}
	a.cfg.SetupDone = true
	if err := a.ResetAdminPassword(codes[0], "ResetDesktop123"); err != nil {
		t.Fatal(err)
	}
}
