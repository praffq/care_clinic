//go:build !darwin && !windows

package install

// Supported reports whether Install works on this system.
const Supported = false

// Install is not available here.
func Install(Plan) error { return ErrUnsupported }

// Installed reports whether the service is registered; never here.
func Installed() bool { return false }

// Current reports whether the installed service is already p; never here.
func Current(Plan) (bool, error) { return false, nil }
