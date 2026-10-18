//go:build unix

package logfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// attachStderr makes fd the process's fd 2 and closes fd.
func attachStderr(fd *os.File) (*os.File, error) {
	defer func() { _ = fd.Close() }()
	if err := unix.Dup2(int(fd.Fd()), int(os.Stderr.Fd())); err != nil {
		return nil, err
	}
	return os.Stderr, nil
}

// detachStderr is a no-op: Unix renames the open file.
func detachStderr(*os.File) bool { return false }
