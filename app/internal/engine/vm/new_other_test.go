//go:build !windows && (!darwin || !cgo)

package vm

import (
	"errors"
	"testing"
)

func TestNewUnsupported(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("New: %v, want ErrUnsupported", err)
	}
}
