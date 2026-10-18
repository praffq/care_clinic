package vm

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// checkDataDiskFree fails with ErrDataDiskInUse if another process (QEMU)
// has the data disk open: QEMU opens disk images without FILE_SHARE_WRITE,
// so an exclusive open fails with a sharing violation. The probe handle is
// closed at once.
func checkDataDiskFree(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("data disk: %w", err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return fmt.Errorf("%w: %s", ErrDataDiskInUse, path)
	}
	if err != nil {
		return fmt.Errorf("data disk: %w", &os.PathError{Op: "open", Path: path, Err: err})
	}
	_ = windows.CloseHandle(h)
	return nil
}

// setSparse marks f sparse so Truncate does not allocate the whole disk.
func setSparse(f *os.File) error {
	var n uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil); err != nil {
		return fmt.Errorf("mark sparse: %w", err)
	}
	return nil
}
