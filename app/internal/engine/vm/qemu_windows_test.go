package vm

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckDataDiskFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.img")
	if _, err := EnsureDataDisk(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDiskFree(path); err != nil {
		t.Fatal(err)
	}
	// Stand in for QEMU, which opens disk images sharing reads only.
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDataDiskFree(path); !errors.Is(err, ErrDataDiskInUse) {
		t.Fatalf("held disk: %v, want ErrDataDiskInUse", err)
	}
	_ = windows.CloseHandle(h)
	if err := checkDataDiskFree(path); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if err := checkDataDiskFree(filepath.Join(t.TempDir(), "missing.img")); err == nil {
		t.Fatal("missing disk accepted")
	}
}

func TestEnsureDataDiskIsSparse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.img")
	if _, err := EnsureDataDisk(path, 1<<30); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(path)
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		t.Fatal(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_SPARSE_FILE == 0 {
		t.Fatalf("attributes %#x, want sparse", attrs)
	}
}

func TestWHPXSettingsNeverTCG(t *testing.T) {
	if WHPXAccel != "whpx,kernel-irqchip=off" {
		t.Fatalf("accelerator = %q", WHPXAccel)
	}
	// Hosted runners have no WHPX; the probe must fail cleanly, not panic.
	if err := CheckWHPX(); err != nil && !errors.Is(err, ErrHypervisorUnavailable) {
		t.Fatalf("CheckWHPX = %v", err)
	}
}
