package vm

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// consoleLog is an append-only log file that is renamed to path+".1"
// (replacing any previous one) when a write would grow it past max bytes, so
// at most about 2*max bytes are kept.
type consoleLog struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

func openConsoleLog(path string, max int64) (*consoleLog, error) {
	l := &consoleLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *consoleLog) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("console log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("console log: %w", err)
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *consoleLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *consoleLog) rotate() error {
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("console log: %w", err)
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return fmt.Errorf("console log: %w", err)
	}
	return l.open()
}

func (l *consoleLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// copyConsole copies r to log and mirror until r fails; a failing mirror
// never stops logging.
func copyConsole(r io.Reader, log, mirror io.Writer) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = log.Write(buf[:n])
			if mirror != nil {
				_, _ = mirror.Write(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}
