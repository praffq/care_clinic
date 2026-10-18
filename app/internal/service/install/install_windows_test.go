package install

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The script must at least parse in Windows PowerShell; a syntax error
// would only show up behind a UAC prompt otherwise.
func TestWindowsScriptParses(t *testing.T) {
	s, err := WindowsScript(Plan{Binary: `C:\Program Files\CARE Clinic\care-clinic-service.exe`, User: "S-1-5-21-1-2-3-1001"}, `C:\Temp\e.txt`)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "install.ps1")
	if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	parsePS(t, f)
}

// So must the developer install script.
func TestServiceScriptParses(t *testing.T) {
	f, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "service-windows.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	parsePS(t, f)
}

func parsePS(t *testing.T, f string) {
	t.Helper()
	check := "$e = $null; [void][System.Management.Automation.Language.Parser]::ParseFile('" + f + "', [ref]$null, [ref]$e); if ($e.Count) { $e | ForEach-Object { $_.ToString() }; exit 1 }"
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", f, err, out)
	}
}

func TestInstallResult(t *testing.T) {
	exitErr := func(code string) error {
		return exec.Command("cmd", "/c", "exit "+code).Run()
	}
	dir := t.TempDir()
	errFile := filepath.Join(dir, "err.txt")
	if err := installResult(nil, errFile); err != nil {
		t.Fatal(err)
	}
	if err := installResult(exitErr("3010"), errFile); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("3010: %v", err)
	}
	if err := os.WriteFile(errFile, []byte("New-LocalGroup: access denied\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installResult(exitErr("1"), errFile); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("1: %v", err)
	}
	other := errors.New("The operation was canceled by the user.")
	if err := installResult(other, errFile); err != other {
		t.Fatalf("cancel: %v", err)
	}
}
