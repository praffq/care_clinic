// Package install installs care-clinic-service on this computer from the
// desktop app with a single administrator prompt. On macOS it mirrors
// scripts/service-macos.sh install-built: create the care-clinic group and
// add the user, copy the binary root-owned into place, write the
// LaunchDaemon plist and bootstrap it. On Windows it mirrors
// scripts/service-windows.ps1 install: create the care-clinic local group
// and add the user, turn on the Windows Hypervisor Platform, and run
// `care-clinic-service service install` and `service start` (see
// WindowsScript).
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
)

// Paths and names the service runs under (see scripts/service-macos.sh).
const (
	Label     = "org.ohc.care-clinic"
	Group     = "care-clinic"
	Root      = "/Library/Application Support/care-clinic"
	Binary    = Root + "/bin/care-clinic-service"
	PlistPath = "/Library/LaunchDaemons/" + Label + ".plist"
	LogDir    = "/Library/Logs/care-clinic"
)

var (
	// ErrUnsupported is returned where the app can't install the service.
	ErrUnsupported = errors.New("installing the CARE Clinic service from the app is not supported on this system")
	// ErrRestartRequired: the service is installed, but Windows turned on
	// the Windows Hypervisor Platform and must restart before the engine
	// can run.
	ErrRestartRequired = errors.New("the Windows Hypervisor Platform was turned on: restart Windows, then open CARE Clinic to finish setting up")
)

// Plan is one install.
type Plan struct {
	// Binary is the care-clinic-service executable to install, normally the
	// one inside the app bundle.
	Binary string
	// Plist is the LaunchDaemon plist content.
	Plist []byte
	// User is added to the care-clinic group so the app can reach the
	// control socket. On Windows it is the user's SID.
	User string
}

var userRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

func (p Plan) validate() error {
	if !userRE.MatchString(p.User) || p.User == "root" {
		return fmt.Errorf("can't add user %q to the %s group", p.User, Group)
	}
	if !strings.HasPrefix(p.Binary, "/") {
		return fmt.Errorf("service binary path must be absolute: %q", p.Binary)
	}
	if len(p.Plist) == 0 {
		return errors.New("no LaunchDaemon plist")
	}
	return nil
}

// Script is the root shell script that installs Binary with the plist at
// plistTmp. It is idempotent: a loaded service is booted out first (it
// stops CARE and the VM), engine data and slots are kept.
func Script(p Plan, plistTmp string) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	q := elevate.ShQuote
	label := q("system/" + Label)
	lines := []string{
		"set -e",
		"if ! dseditgroup -o read " + q(Group) + " >/dev/null 2>&1; then dseditgroup -o create -r 'CARE Clinic service users' " + q(Group) + "; fi",
		"if ! dseditgroup -o checkmember -m " + q(p.User) + " " + q(Group) + " >/dev/null 2>&1; then dseditgroup -o edit -a " + q(p.User) + " -t user " + q(Group) + "; fi",
		"install -d -o root -g wheel -m 0755 " + q(Root) + " " + q(Root+"/bin") + " " + q(LogDir),
		"install -o root -g wheel -m 0755 " + q(p.Binary) + " " + q(Binary+".new"),
		"mv -f " + q(Binary+".new") + " " + q(Binary),
		"if launchctl print " + label + " >/dev/null 2>&1; then",
		"  launchctl bootout " + label + " || true",
		"  i=0; while launchctl print " + label + " >/dev/null 2>&1; do i=$((i+1)); [ $i -lt 300 ] || { echo 'the old service did not stop' >&2; exit 1; }; sleep 0.5; done",
		"fi",
		"install -o root -g wheel -m 0644 " + q(plistTmp) + " " + q(PlistPath),
		"launchctl bootstrap system " + q(PlistPath),
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// current reports whether the installed service at binary (and, unless
// plist is empty, the LaunchDaemon plist) is already p. A missing file means
// it is not.
func current(p Plan, binary, plist string) (bool, error) {
	want, err := fileSum(p.Binary)
	if err != nil {
		return false, err
	}
	got, err := fileSum(binary)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || got != want {
		return false, err
	}
	if plist == "" {
		return true, nil
	}
	b, err := os.ReadFile(plist)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && bytes.Equal(b, p.Plist), err
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
