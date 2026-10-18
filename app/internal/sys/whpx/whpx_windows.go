package whpx

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Check reports whether WHPX is usable: WinHvPlatform.dll loads and
// WHvGetCapability(WHvCapabilityCodeHypervisorPresent) is true. It returns
// ErrFeatureOff or ErrNotRunning otherwise.
func Check() error {
	dll := windows.NewLazySystemDLL("WinHvPlatform.dll")
	if err := dll.Load(); err != nil {
		return ErrFeatureOff
	}
	proc := dll.NewProc("WHvGetCapability")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("%w (%v)", ErrFeatureOff, err)
	}
	var present, written uint32
	const hypervisorPresent = 0
	hr, _, _ := proc.Call(hypervisorPresent, uintptr(unsafe.Pointer(&present)), unsafe.Sizeof(present), uintptr(unsafe.Pointer(&written)))
	if hr != 0 || present == 0 {
		return ErrNotRunning
	}
	return nil
}
