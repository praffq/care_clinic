//go:build !darwin && !windows

package hostcheck

// Nil probes: the engine runs on macOS and Windows only.
var (
	HostVirtualization func() (Virtualization, error)
	HostMemory         func() (uint64, error)
)
