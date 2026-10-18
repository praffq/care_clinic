package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func sheetCodes(t *testing.T, sheet []byte) []string {
	t.Helper()
	var codes []string
	for _, line := range strings.Split(string(sheet), "\n") {
		if strings.HasPrefix(line, "[ ] ") {
			codes = append(codes, strings.TrimPrefix(line, "[ ] "))
		}
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("expected six codes, got %d", len(codes))
	}
	return codes
}

func recoveryApp(t *testing.T) (*App, []string) {
	t.Helper()
	a := settingsApp(t)
	a.stateDir = filepath.Join(t.TempDir(), "clinic")
	sheet, hashes, err := generateAdminRecoveryCodes("test-clinic.local")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.saveRecovery(adminRecovery{Hashes: hashes}); err != nil {
		t.Fatal(err)
	}
	return a, sheetCodes(t, sheet)
}

func recoveryCount(t *testing.T, a *App) int {
	t.Helper()
	n, err := a.GetAdminRecoveryCodeCount()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminRecoverySingleUseAndPersistence(t *testing.T) {
	a, codes := recoveryApp(t)
	password := "NewDesktopPass123"
	normal := strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))
	if err := a.ResetAdminPassword(normal, password); err != nil {
		t.Fatal(err)
	}
	if !a.VerifyAdminPassword(password) || a.VerifyAdminPassword(settingsPassword) {
		t.Fatal("password was not changed")
	}
	if n := recoveryCount(t, a); n != 5 {
		t.Fatalf("used code was not consumed: %d left", n)
	}
	if err := a.ResetAdminPassword(codes[0], "AnotherDesktop123"); err == nil {
		t.Fatal("used code accepted again")
	}
	if err := a.ResetAdminPassword(codes[1], "AnotherDesktop123"); err != nil {
		t.Fatal("another code should remain valid:", err)
	}
	file, _ := a.recoveryFile()
	for _, p := range []string{a.configPath(), file} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range append(codes, password, "AnotherDesktop123") {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("plaintext secret saved in %s", p)
			}
		}
	}
	// Windows has no POSIX permission bits; files inherit the folder ACL.
	if fi, err := os.Stat(file); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("recovery file mode: %v, %v", fi, err)
	}
}

func TestAdminRecoveryCodeCountTracksChanges(t *testing.T) {
	a, codes := recoveryApp(t)
	for i, code := range codes {
		if err := a.ResetAdminPassword(code, "NewDesktopPass123"); err != nil {
			t.Fatal(err)
		}
		if n := recoveryCount(t, a); n != 5-i {
			t.Fatalf("remaining = %d, want %d", n, 5-i)
		}
	}
	path := filepath.Join(t.TempDir(), "new-codes.txt")
	if err := os.WriteFile(path, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.saveAdminRecoveryCodes(path); err == nil {
		t.Fatal("failed export succeeded")
	}
	if n := recoveryCount(t, a); n != 0 {
		t.Fatalf("remaining = %d", n)
	}
	if err := a.saveAdminRecoveryCodes(filepath.Join(t.TempDir(), "new-codes.txt")); err != nil {
		t.Fatal(err)
	}
	if n := recoveryCount(t, a); n != 6 {
		t.Fatalf("remaining = %d", n)
	}
	a.cfg.Role = roleClient
	if _, err := a.GetAdminRecoveryCodeCount(); err == nil {
		t.Fatal("client exposed server recovery status")
	}
}

func TestAdminRecoveryWithoutCodes(t *testing.T) {
	a := settingsApp(t)
	a.stateDir = t.TempDir()
	if n := recoveryCount(t, a); n != 0 {
		t.Fatalf("remaining = %d", n)
	}
	if err := a.ResetAdminPassword("ABCD", "NewDesktopPass123"); err == nil {
		t.Fatal("reset without codes succeeded")
	}
}

func TestAdminRecoveryWeakPasswordKeepsTheCode(t *testing.T) {
	a, codes := recoveryApp(t)
	if err := a.ResetAdminPassword(codes[0], "weak"); err == nil {
		t.Fatal("weak password accepted")
	}
	if n := recoveryCount(t, a); n != 6 {
		t.Fatal("invalid new password consumed a code")
	}
}

func TestAdminRecoveryFailedPasswordSaveSpendsTheCode(t *testing.T) {
	a, codes := recoveryApp(t)
	a.configFile = filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(a.configFile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetAdminPassword(codes[0], "ChangedDesktop123"); err == nil || !strings.Contains(err.Error(), "use another code") {
		t.Fatalf("failed persistence: %v", err)
	}
	if n := recoveryCount(t, a); n != 5 {
		t.Fatalf("code reusable after a failed save: %d left", n)
	}
}

func TestAdminRecoveryRateLimit(t *testing.T) {
	a, codes := recoveryApp(t)
	for range recoveryFreeTries {
		if err := a.ResetAdminPassword("incorrect", "NewDesktopPass123"); err == nil {
			t.Fatal("incorrect recovery code accepted")
		}
	}
	if err := a.ResetAdminPassword(codes[0], "NewDesktopPass123"); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("lockout not enforced: %v", err)
	}
	r, err := a.loadRecovery()
	if err != nil {
		t.Fatal(err)
	}
	r.RetryAfter = time.Now().Add(-time.Second).Unix()
	if err := a.saveRecovery(r); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetAdminPassword(codes[0], "NewDesktopPass123"); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.loadRecovery(); r.Failures != 0 || r.RetryAfter != 0 {
		t.Fatalf("successful recovery did not reset the throttle: %+v", r)
	}
}

func TestAdminRecoveryConcurrentUse(t *testing.T) {
	a, codes := recoveryApp(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.ResetAdminPassword(codes[0], "NewDesktopPass123")
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || recoveryCount(t, a) != 5 {
		t.Fatal("concurrent recovery reused a code")
	}
}

func TestChangeAdminPasswordKeepsRecoveryCodes(t *testing.T) {
	a, codes := recoveryApp(t)
	if err := a.ChangeAdminPassword(settingsPassword, "NewDesktopPass123"); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetAdminPassword(codes[0], settingsPassword); err != nil {
		t.Fatal("password change invalidated recovery codes:", err)
	}
}

func TestRecoveryExportNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.txt")
	if err := saveRecoveryFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := saveRecoveryFile(path, []byte("second")); err == nil {
		t.Fatal("existing export overwritten")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "first" {
		t.Fatal("existing export changed")
	}
}

func TestSaveAdminRecoveryCodes(t *testing.T) {
	a, oldCodes := recoveryApp(t)
	backups := t.TempDir()
	a.service.(*fakeAPI).set(func(st *api.ServiceStatus) { st.Backups = &api.BackupStatus{Dir: backups} })
	var target string
	choose := func(opts wruntime.SaveDialogOptions) (string, error) {
		if !strings.HasPrefix(opts.DefaultFilename, "CARE-") || !strings.HasSuffix(opts.DefaultFilename, ".txt") {
			t.Errorf("filename = %q", opts.DefaultFilename)
		}
		return target, nil
	}
	if _, err := a.saveAdminRecoveryCodesWith("WrongPassword123", choose); err == nil {
		t.Fatal("wrong admin password accepted")
	}
	target = ""
	if path, err := a.saveAdminRecoveryCodesWith(settingsPassword, choose); err != nil || path != "" {
		t.Fatalf("cancelled dialog: %q, %v", path, err)
	}
	for _, unsafe := range []string{
		filepath.Join(a.installDir(), "codes.txt"),
		filepath.Join(filepath.Dir(a.configPath()), "codes.txt"),
		filepath.Join(a.stateDir, "codes.txt"),
		filepath.Join(backups, "codes.txt"),
	} {
		target = unsafe
		if _, err := a.saveAdminRecoveryCodesWith(settingsPassword, choose); err == nil {
			t.Fatalf("unsafe location accepted: %s", unsafe)
		}
	}
	if n := recoveryCount(t, a); n != 6 {
		t.Fatalf("refused saves changed the codes: %d", n)
	}
	target = filepath.Join(t.TempDir(), "codes.txt")
	path, err := a.saveAdminRecoveryCodesWith(settingsPassword, choose)
	if err != nil || path != target {
		t.Fatalf("save: %q, %v", path, err)
	}
	if err := a.ResetAdminPassword(oldCodes[0], "NewDesktopPass123"); err == nil {
		t.Fatal("replacement left old codes valid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ResetAdminPassword(sheetCodes(t, data)[0], "NewDesktopPass123"); err != nil {
		t.Fatal("exported replacement code did not work:", err)
	}
}

func TestDamagedRecoveryFileIsAnError(t *testing.T) {
	a, _ := recoveryApp(t)
	file, _ := a.recoveryFile()
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetAdminRecoveryCodeCount(); err == nil {
		t.Fatal("damaged file read as no codes")
	}
	if err := a.ResetAdminPassword("x", "NewDesktopPass123"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("damaged file: %v", err)
	}
}

func TestRemoveRecovery(t *testing.T) {
	a, _ := recoveryApp(t)
	for range 2 {
		if err := a.removeRecovery(); err != nil {
			t.Fatal(err)
		}
	}
	if n := recoveryCount(t, a); n != 0 {
		t.Fatalf("codes left after removal: %d", n)
	}
}
