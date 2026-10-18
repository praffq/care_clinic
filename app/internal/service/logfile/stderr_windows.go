package logfile

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachStderr makes fd the standard error handle (which the Go runtime
// writes crash output to) and os.Stderr.
func attachStderr(fd *os.File) (*os.File, error) {
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(fd.Fd())); err != nil {
		return nil, err
	}
	os.Stderr = fd
	return fd, nil
}

// detachStderr closes the current file: Windows cannot rename an open file.
// It reports that the file was closed.
func detachStderr(out *os.File) bool { _ = out.Close(); return true }
