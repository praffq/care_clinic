package logdir

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"
)

type Writer struct {
	dir  *Dir
	name string
	max  int64
	keep int

	mu      sync.Mutex
	f       *os.File
	size    int64
	checked time.Time
	last    time.Time
}

const recheckEvery = 30 * time.Second

func NewWriter(d *Dir, name string, maxBytes int64, keep int) *Writer {
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	return &Writer{dir: d, name: name, max: maxBytes, keep: max(keep, 1)}
}

func (w *Writer) Name() string { return w.name }

func (w *Writer) open() error {
	f, err := w.dir.OpenAppend(w.name)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f, w.size, w.checked = f, st.Size(), time.Now()
	return nil
}

func (w *Writer) stale() bool {
	if time.Since(w.checked) < recheckEvery {
		return false
	}
	w.checked = time.Now()
	cur, err := w.f.Stat()
	if err != nil {
		return true
	}
	onDisk, err := w.dir.Lstat(w.name)
	if err != nil || !os.SameFile(cur, onDisk) {
		return true
	}
	w.size = onDisk.Size()
	return false
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil && w.stale() {
		_ = w.f.Close()
		w.f = nil
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			w.size = 0
		}
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	w.last = time.Now()
	return n, err
}

func (w *Writer) rotate() error {
	_ = w.f.Close()
	w.f = nil
	return Rotate(w.dir, w.name, w.keep)
}

func Rotate(d *Dir, name string, keep int) error {
	_ = d.Remove(RotatedName(name, keep))
	for i := keep - 1; i >= 1; i-- {
		if err := d.Rename(RotatedName(name, i), RotatedName(name, i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return d.Rename(name, RotatedName(name, 1))
}

func (w *Writer) LastWrite() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

func (w *Writer) IsOpen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f != nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
