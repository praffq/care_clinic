package install

import (
	"os"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
)

// Supported reports whether Install works on this system.
const Supported = true

// Install runs Script behind one administrator prompt.
func Install(p Plan) error {
	if err := p.validate(); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "care-clinic-*.plist")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(p.Plist); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	script, err := Script(p, f.Name())
	if err != nil {
		return err
	}
	return elevate.Run(script, true)
}

// Current reports whether the installed service is already p: the same
// binary and LaunchDaemon plist.
func Current(p Plan) (bool, error) { return current(p, Binary, PlistPath) }

// Installed reports whether the LaunchDaemon plist is in place.
func Installed() bool {
	_, err := os.Stat(PlistPath)
	return err == nil
}
