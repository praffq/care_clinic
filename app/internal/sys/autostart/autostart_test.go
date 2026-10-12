package autostart

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMacAutostartUsesLaunchServicesInsteadOfAnEphemeralExecutablePath(t *testing.T) {
	plist := macPlist()
	if strings.Contains(plist, "/AppTranslocation/") || strings.Contains(plist, "Contents/MacOS") {
		t.Fatalf("LaunchAgent kept a potentially translocated executable path: %s", plist)
	}
	if got := plistProgramArguments([]byte(plist)); !reflect.DeepEqual(got, macProgramArguments()) {
		t.Fatalf("plist arguments = %v, want %v", got, macProgramArguments())
	}
	if sameArgs(plistProgramArguments([]byte(plist)), []string{"/wrong/app", autostartArg}) {
		t.Fatal("an unrelated application registration was reported enabled")
	}
}

func TestLinuxAutostartEscapesPathAndChecksTheRecordedExecutable(t *testing.T) {
	exe := `/home/O'Brien/CARE "Clinic" $HOME ` + "`" + `100%/care`
	data := []byte(linuxDesktop(exe))
	if got, want := desktopExec(data), desktopCommand(exe); got != want {
		t.Fatalf("Exec = %q, want %q", got, want)
	}
	if !linuxEnabled(data, exe) {
		t.Fatal("the current executable was not recognized")
	}
	if linuxEnabled(data, filepath.Join(filepath.Dir(exe), "other")) {
		t.Fatal("a registration for another executable was reported enabled")
	}
	for _, escaped := range []string{`\"Clinic\"`, `\$HOME`, "\\`100", "100%%"} {
		if !strings.Contains(desktopExec(data), escaped) {
			t.Errorf("desktop Exec is missing escaped form %q: %s", escaped, desktopExec(data))
		}
	}
}

func TestWindowsAutostartChecksTheRegistryValue(t *testing.T) {
	exe := `C:\Users\O'Brien\CARE Clinic.exe`
	output := "HKEY_CURRENT_USER\\Software\\Run\n    CARE Clinic    REG_SZ    " + windowsCommand(exe) + "\n"
	if !windowsEnabled(output, exe) {
		t.Fatal("the current executable was not recognized")
	}
	if windowsEnabled(output, `C:\Other\CARE Clinic.exe`) {
		t.Fatal("a registration for another executable was reported enabled")
	}
}

func TestPlistProgramArgumentsRejectMalformedInput(t *testing.T) {
	if got := plistProgramArguments([]byte("<plist><dict><key>ProgramArguments</key><array><string>broken")); got != nil {
		t.Fatalf("malformed plist arguments = %v", got)
	}
}
