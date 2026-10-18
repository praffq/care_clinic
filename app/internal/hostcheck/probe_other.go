//go:build !darwin

package hostcheck

// Nil probes: these checks come with the Windows service.
var (
	HostVirtualization func() (Virtualization, error)
	HostMemory         func() (uint64, error)
)
