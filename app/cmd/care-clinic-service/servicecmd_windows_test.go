package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureDir(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated token")
	}
	parent := t.TempDir()
	check := func(dir string) {
		t.Helper()
		sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, _ := sd.Owner()
		if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			t.Fatalf("%s owner %s", dir, owner)
		}
		s := sd.String()
		if !strings.Contains(s, "D:P") || strings.Count(s, "(A;") != 2 || !strings.Contains(s, ";;;SY)") || !strings.Contains(s, ";;;BA)") {
			t.Fatalf("%s: %s", dir, s)
		}
	}
	fresh := filepath.Join(parent, "fresh")
	if err := secureDir(fresh); err != nil {
		t.Fatal(err)
	}
	check(fresh)

	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureDir(existing); err != nil {
		t.Fatal(err)
	}
	check(existing)
	if err := secureDir(existing); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestQuoteCommandLine(t *testing.T) {
	got := quoteCommandLine([]string{`C:\ProgramData\CARE Clinic\bin\care-clinic-service.exe`, "--root", `C:\ProgramData\CARE Clinic`})
	want := `"C:\ProgramData\CARE Clinic\bin\care-clinic-service.exe" --root "C:\ProgramData\CARE Clinic"`
	if got != want {
		t.Fatalf("got %s", got)
	}
}
