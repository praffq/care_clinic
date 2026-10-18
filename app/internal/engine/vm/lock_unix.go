//go:build unix

package vm

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// checkDataDiskFree fails with ErrDataDiskInUse if another VM has the data
// disk open. Virtualization.framework holds an exclusive flock on attached
// disk images (as does QEMU), so probing it turns the hypervisor's opaque
// "storage device attachment is invalid" into a clear error. The probe lock
// is released at once; the hypervisor's own lock still guards a start race.
func checkDataDiskFree(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("data disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: %s", ErrDataDiskInUse, path)
		}
		return fmt.Errorf("lock data disk: %w", err)
	}
	return nil
}

// setSparse is a no-op: Truncate leaves a hole on Unix filesystems.
func setSparse(*os.File) error { return nil }
