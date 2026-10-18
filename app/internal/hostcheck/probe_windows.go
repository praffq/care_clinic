package hostcheck

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/whpx"
)

// HostVirtualization probes the Windows Hypervisor Platform. A feature
// that is off is fixable: installing the service turns it on.
func HostVirtualization() (Virtualization, error) {
	return windowsVirtualization(runtime.GOARCH, whpx.Check), nil
}

func windowsVirtualization(arch string, check func() error) Virtualization {
	if arch != "amd64" {
		return Virtualization{Problem: "CARE Clinic needs a 64-bit Intel or AMD processor on Windows."}
	}
	switch err := check(); {
	case err == nil:
		return Virtualization{Supported: true}
	case errors.Is(err, whpx.ErrFeatureOff):
		return Virtualization{Fixable: true, Problem: "The Windows Hypervisor Platform is turned off. Installing the CARE Clinic service turns it on; restart Windows when asked."}
	case errors.Is(err, whpx.ErrNotRunning):
		return Virtualization{Problem: "The Windows Hypervisor Platform is on but not running. Restart Windows; if this stays, turn on hardware virtualization (Intel VT-x or AMD-V) in the computer's firmware (BIOS/UEFI) settings."}
	default:
		return Virtualization{Problem: err.Error()}
	}
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// HostMemory is the physical memory.
func HostMemory() (uint64, error) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, err
	}
	return m.TotalPhys, nil
}
