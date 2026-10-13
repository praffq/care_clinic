package autostart

import (
	"os"
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

func TestLinuxAutostartRoundTripsHostileExecutablePaths(t *testing.T) {
	for _, exe := range []string{
		`/home/O'Brien/CARE "Clinic" $HOME ` + "`" + `100%/care`,
		`/opt/back\slash\n/$(x)/care`,
		"/tmp/tab\tname/ care",
	} {
		data := []byte(linuxDesktop(exe))
		args, ok := desktopArgv(desktopExec(data))
		if !ok || !reflect.DeepEqual(args, []string{exe, autostartArg}) {
			t.Fatalf("%q round-tripped to %q ok=%v\n%s", exe, args, ok, data)
		}
		if !linuxEnabled(data, exe) || linuxEnabled(data, exe+"2") || !linuxOwned(data) {
			t.Fatalf("enabled/owned wrong for %q", exe)
		}
	}
}

func TestLinuxDesktopUsesBothEscapingLayers(t *testing.T) {
	line := ""
	for _, l := range strings.Split(linuxDesktop(`/a\b"c$d`+"`"+`e%f`), "\n") {
		if strings.HasPrefix(l, "Exec=") {
			line = l
		}
	}
	// exec layer: \\ \" \$ \` %%; string layer doubles every backslash.
	want := `Exec="/a\\\\b\\"c\\$d\\` + "`" + `e%%f" --autostart`
	if line != want {
		t.Fatalf("Exec line = %s\nwant      %s", line, want)
	}
}

func TestLinuxMalformedOrUnrelatedEntriesAreNotOwned(t *testing.T) {
	for _, data := range []string{
		"[Desktop Entry]\nName=Other\nExec=\"/x\" --autostart\n",
		"[Desktop Entry]\nName=CARE Clinic\nExec=\"/x --autostart\n",
		"[Desktop Entry]\nName=CARE Clinic\nExec=/x\n",
		"[Other]\nName=CARE Clinic\nExec=\"/x\" --autostart\n",
		"",
	} {
		if linuxOwned([]byte(data)) {
			t.Errorf("owned: %q", data)
		}
	}
	if !linuxOwned([]byte("[Desktop Entry]\nName=CARE Clinic\nExec=\"/moved/care\" --autostart\n")) {
		t.Fatal("moved executable entry should be owned")
	}
}

func TestMacOwnershipCoversCurrentAndPreviousDirectExecutableEntries(t *testing.T) {
	direct := `<plist><dict><key>Label</key><string>ohc.care-clinic</string><key>ProgramArguments</key><array><string>/Applications/CARE Clinic.app/Contents/MacOS/CARE Clinic</string><string>--autostart</string></array></dict></plist>`
	if !macOwned([]byte(direct)) || !macOwned([]byte(macPlist())) {
		t.Fatal("previous or current entry not owned")
	}
	if sameArgs(plistProgramArguments([]byte(direct)), macProgramArguments()) {
		t.Fatal("previous direct entry must not be exactly enabled")
	}
	other := strings.Replace(direct, "ohc.care-clinic", "com.example.other", 1)
	if macOwned([]byte(other)) || macOwned([]byte("junk")) {
		t.Fatal("unrelated plist reported owned")
	}
}

func TestRemoveOwnedFilePreservesUnrelated(t *testing.T) {
	dir := t.TempDir()
	mine, other := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(mine, []byte("[Desktop Entry]\nName=CARE Clinic\nExec=\"/moved\" --autostart\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("[Desktop Entry]\nName=Other\nExec=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mine, other, filepath.Join(dir, "missing")} {
		if err := removeOwnedFile(p, linuxOwned); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("owned entry not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated entry removed")
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
func TestWindowsOwnershipSurvivesMovedExecutable(t *testing.T) {
	out := "HKEY_CURRENT_USER\\Software\\Run\n    CARE Clinic    REG_SZ    " + windowsCommand(`D:\Old\CARE Clinic.exe`) + "\n"
	if !windowsOwned(out) || windowsEnabled(out, `C:\New\CARE Clinic.exe`) {
		t.Fatal("moved executable should be owned but not enabled")
	}
	unrelated := "HKEY_CURRENT_USER\\Software\\Run\n    CARE Clinic    REG_SZ    C:\\other.exe\n"
	if windowsOwned(unrelated) || windowsOwned("") {
		t.Fatal("unrelated value reported owned")
	}
}
