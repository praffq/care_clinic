// Package vm runs the CARE engine: a small Linux VM that hosts Docker for the
// clinic on macOS (Virtualization.framework) and Windows (QEMU with the
// Windows Hypervisor Platform). Linux runs the same operation agent natively.
package vm

import "runtime"

// Ports are shared with vm/agent/main.go; keep them in step.
const (
	vsockControl = 2377
	vsockHTTP    = 80
	vsockHTTPS   = 443
)

// HostFlag starts the helper process that owns the VM.
const HostFlag = "--vm-host"

// Supported reports whether this OS runs Docker inside the CARE engine.
func Supported() bool { return supportedOS(runtime.GOOS) }

func supportedOS(goos string) bool { return goos == "darwin" || goos == "windows" }
