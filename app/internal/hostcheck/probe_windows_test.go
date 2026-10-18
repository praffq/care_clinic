package hostcheck

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/whpx"
)

func TestWindowsVirtualization(t *testing.T) {
	ok := func() error { return nil }
	if v := windowsVirtualization("arm64", ok); v.Supported || v.Problem == "" {
		t.Fatalf("arm64: %+v", v)
	}
	if v := windowsVirtualization("amd64", ok); !v.Supported {
		t.Fatalf("ok: %+v", v)
	}
	off := func() error { return fmt.Errorf("x: %w", whpx.ErrFeatureOff) }
	if v := windowsVirtualization("amd64", off); v.Supported || !v.Fixable {
		t.Fatalf("off: %+v", v)
	}
	if v := windowsVirtualization("amd64", func() error { return whpx.ErrNotRunning }); v.Supported || v.Fixable || v.Problem == "" {
		t.Fatalf("not running: %+v", v)
	}
	if v := windowsVirtualization("amd64", func() error { return errors.New("odd") }); v.Supported || v.Problem != "odd" {
		t.Fatalf("other: %+v", v)
	}
}

func TestHostMemory(t *testing.T) {
	m, err := HostMemory()
	if err != nil || m < 1<<30 {
		t.Fatalf("HostMemory = %d, %v", m, err)
	}
}
