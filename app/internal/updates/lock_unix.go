//go:build unix

package updates

import (
	"fmt"
	"os"
	"syscall"
)

type dirLock struct{ f *os.File }

// lockDir takes an exclusive, non-blocking lock on path so only one process
// changes versions at a time.
func lockDir(path string) (*dirLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %v", ErrBusy, err)
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) unlock() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
