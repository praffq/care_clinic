//go:build unix

// Package logfile is the service's size-rotated log. The file is installed
// as the process's stderr (fd 2), so Go runtime crash output (panics, fatal
// errors) lands in the same file as the structured log and survives the
// restart launchd does next. When the file grows past MaxBytes it is renamed
// to <path>.1 (older ones shift up to <path>.<Keep>) and a new file takes
// over fd 2.
package logfile

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// File writes to stderr and rotates the file behind it.
type File struct {
	path string
	max  int64
	keep int

	mu   sync.Mutex
	size int64
}

// Open opens (appending) path as the process's stderr and returns a writer
// for log output. Callers must write log lines through it so that rotation
// sees them; writing to os.Stderr directly is fine but not counted.
func Open(path string, maxBytes int64, keep int) (*File, error) {
	f := &File{path: path, max: maxBytes, keep: max(keep, 1)}
	if err := f.reopen(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) reopen() error {
	fd, err := os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = fd.Close() }()
	st, err := fd.Stat()
	if err != nil {
		return err
	}
	if err := unix.Dup2(int(fd.Fd()), int(os.Stderr.Fd())); err != nil {
		return fmt.Errorf("redirect stderr to %s: %w", f.path, err)
	}
	f.size = st.Size()
	return nil
}

// Write appends p to the log, rotating first when it would exceed MaxBytes.
func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.size > 0 && f.size+int64(len(p)) > f.max {
		if err := f.rotate(); err != nil {
			// Keep logging to the current file rather than losing lines.
			_, _ = fmt.Fprintf(os.Stderr, "logfile: rotate %s: %v\n", f.path, err)
			f.size = 0
		}
	}
	n, err := os.Stderr.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *File) rotate() error {
	for i := f.keep - 1; i >= 1; i-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", f.path, i), fmt.Sprintf("%s.%d", f.path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(f.path, f.path+".1"); err != nil {
		return err
	}
	return f.reopen()
}
