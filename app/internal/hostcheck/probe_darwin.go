package hostcheck

import "golang.org/x/sys/unix"

// HostVirtualization probes Virtualization.framework support.
func HostVirtualization() (Virtualization, error) {
	hv, err := unix.SysctlUint32("kern.hv_support")
	if err != nil {
		return Virtualization{}, err
	}
	if arm, err := unix.SysctlUint32("hw.optional.arm64"); err != nil || arm != 1 {
		return Virtualization{Problem: "CARE Clinic needs a Mac with Apple silicon (M1 or later)."}, nil
	}
	return Virtualization{Supported: hv == 1}, nil
}

// HostMemory is the physical memory.
func HostMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }
