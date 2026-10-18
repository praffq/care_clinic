//go:build windows

package updates

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type dirLock struct{ f *os.File }

// lockDir takes an exclusive, non-blocking lock on path so only one process
// changes versions at a time.
func lockDir(path string) (*dirLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %v", ErrBusy, err)
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) unlock() {
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, new(windows.Overlapped))
	_ = l.f.Close()
}
