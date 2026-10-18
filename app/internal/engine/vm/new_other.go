//go:build !darwin || !cgo

package vm

import (
	"errors"
	"fmt"
	"runtime"
)

// New returns errors.ErrUnsupported: only the macOS Virtualization.framework
// driver (which needs cgo) exists so far.
func New(cfg Config) (Machine, error) {
	return nil, fmt.Errorf("engine VM driver for %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
