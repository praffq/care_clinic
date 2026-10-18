package hostcheck

import "golang.org/x/sys/unix"

// HostVirtualization probes Virtualization.framework support.
func HostVirtualization() (Virtualization, error) {
	hv, err := unix.SysctlUint32("kern.hv_support")
	if err != nil {
		return Virtualization{}, err
	}
	arm, err := unix.SysctlUint32("hw.optional.arm64")
	return Virtualization{Supported: hv == 1, Arm64: err == nil && arm == 1}, nil
}

// HostMemory is the physical memory.
func HostMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }
