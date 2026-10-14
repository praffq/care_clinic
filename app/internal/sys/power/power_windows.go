//go:build windows

package power

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemPowerStatus struct {
	ACLineStatus       byte
	BatteryFlag        byte
	BatteryLifePercent byte
	SystemStatusFlag   byte
	BatteryLifeTime    uint32
	BatteryFullLife    uint32
}

func OnACPower() (bool, bool, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("GetSystemPowerStatus")
	var status systemPowerStatus
	result, _, err := proc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return false, false, fmt.Errorf("detect mains power: %w", err)
	}
	if status.ACLineStatus == 255 {
		return false, false, nil
	}
	return status.ACLineStatus == 1, true, nil
}
