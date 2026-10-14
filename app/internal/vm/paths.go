package vm

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/applog"
)

// appFolder matches the folder name used everywhere else by the app.
const appFolder = "care-clinic"

// StateDir holds the engine files, the data disk and credentials.
func StateDir() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", appFolder, "vm")
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, appFolder, "vm")
	}
	return filepath.Join(home, ".local", "share", appFolder, "vm")
}

// runDir holds sockets. macOS limits unix socket paths to 104 bytes, so it
// lives directly under the home folder there.
func runDir() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "."+appFolder, "run")
	}
	return filepath.Join(StateDir(), "run")
}

func BinDir() string        { return filepath.Join(StateDir(), "bin") }
func engineDir() string     { return filepath.Join(StateDir(), "engine") }
func dataDisk() string      { return filepath.Join(StateDir(), "data.img") }
func controlSocket() string { return filepath.Join(runDir(), "control.sock") }
func ConsoleLog() string    { return filepath.Join(applog.DefaultLogDir(), "engine-console.log") }
func HostLog() string       { return filepath.Join(applog.DefaultLogDir(), "engine-host.log") }
