// Package logfile is the service's size-rotated log. The file is installed
// as the process's stderr (fd 2), so Go runtime crash output (panics, fatal
// errors) lands in the same file as the structured log and survives the
// restart launchd (or the Windows service manager) does next. When the file
// grows past MaxBytes it is renamed to <stem>.1.log (older ones shift up to
// <stem>.<Keep>.log) and a new file takes over fd 2. On Windows the file becomes
// the process's standard error handle (SetStdHandle) and os.Stderr.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

// File writes to stderr and rotates the file behind it.
type File struct {
	dir  *logdir.Dir
	name string
	path string
	max  int64
	keep int

	mu   sync.Mutex
	size int64
	out  *os.File
}

// Open opens (appending) path as the process's stderr and returns a writer
// for log output. Callers must write log lines through it so that rotation
// sees them; writing to os.Stderr directly is fine but not counted.
func Open(path string, maxBytes int64, keep int) (*File, error) {
	dir, err := logdir.OpenOrCreate(filepath.Dir(path), nil)
	if err != nil {
		return nil, err
	}
	return OpenIn(dir, filepath.Base(path), maxBytes, keep)
}

func OpenIn(dir *logdir.Dir, name string, maxBytes int64, keep int) (*File, error) {
	f := &File{dir: dir, name: name, path: filepath.Join(dir.Path(), name), max: maxBytes, keep: max(keep, 1)}
	if err := f.reopen(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) Path() string { return f.path }

func (f *File) reopen() error {
	fd, err := f.dir.OpenAppend(f.name)
	if err != nil {
		return err
	}
	st, err := fd.Stat()
	if err != nil {
		_ = fd.Close()
		return err
	}
	out, err := attachStderr(fd)
	if err != nil {
		_ = fd.Close()
		return fmt.Errorf("redirect stderr to %s: %w", f.path, err)
	}
	f.out, f.size = out, st.Size()
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
	n, err := f.out.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *File) rotate() (err error) {
	if detachStderr(f.out) {
		defer func() {
			if err != nil {
				_ = f.reopen() // keep a writable file after a failed rename
			}
		}()
	}
	if err := logdir.Rotate(f.dir, f.name, f.keep); err != nil {
		return err
	}
	return f.reopen()
}
