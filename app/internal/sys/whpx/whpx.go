// Package whpx detects the Windows Hypervisor Platform (WHPX), which QEMU
// needs to run the engine at native speed. CARE never falls back to QEMU's
// software emulation (TCG): it is far too slow for the clinic stack.
package whpx

import "errors"

// Feature is the optional Windows feature that provides WHPX.
const Feature = "HypervisorPlatform"

var (
	// ErrFeatureOff: the Windows Hypervisor Platform feature is not enabled
	// (WinHvPlatform.dll is missing).
	ErrFeatureOff = errors.New("the Windows Hypervisor Platform feature is turned off; turn it on and restart Windows")
	// ErrNotRunning: the feature is on but no hypervisor is running, e.g.
	// Windows has not restarted since it was enabled, or hardware
	// virtualization (VT-x/AMD-V) is off in the firmware settings.
	ErrNotRunning = errors.New("the Windows hypervisor is not running; restart Windows, and if that doesn't help turn on hardware virtualization (VT-x/AMD-V) in the computer's firmware settings")
)
