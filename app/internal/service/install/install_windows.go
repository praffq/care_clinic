package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
)

// Supported reports whether Install works on this system.
const Supported = true

// Install runs WindowsScript behind one UAC prompt. It returns
// ErrRestartRequired when the service is installed but Windows must
// restart to start the hypervisor.
func Install(p Plan) error {
	f, err := os.CreateTemp("", "care-clinic-install-*.txt")
	if err != nil {
		return err
	}
	errFile := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(errFile) }()
	script, err := WindowsScript(p, errFile)
	if err != nil {
		return err
	}
	return installResult(elevate.RunPS(script), errFile)
}

func installResult(err error, errFile string) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	if exit.ExitCode() == RestartExitCode {
		return ErrRestartRequired
	}
	if b, _ := os.ReadFile(errFile); len(strings.TrimSpace(string(b))) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
	}
	return err
}

// Current reports whether the installed service binary in
// %ProgramData%\CARE Clinic\bin is already p.Binary. (QEMU is copied
// with it, so a QEMU bump alone does not count.)
func Current(p Plan) (bool, error) {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return current(p, filepath.Join(pd, WindowsRootName, "bin", "care-clinic-service.exe"), "")
}

// Installed reports whether the CareClinic service is registered. It only
// needs the rights every user has on the service manager.
func Installed() bool {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseServiceHandle(m) }()
	name, _ := windows.UTF16PtrFromString(ServiceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	_ = windows.CloseServiceHandle(s)
	return true
}
