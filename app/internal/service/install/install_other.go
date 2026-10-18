//go:build !darwin

package install

// Supported reports whether Install works on this system.
const Supported = false

// Install is not available here yet; the Windows service comes later.
func Install(Plan) error { return ErrUnsupported }
