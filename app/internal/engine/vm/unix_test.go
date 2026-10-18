//go:build unix

package vm

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func assertSparse(t *testing.T, path string, st os.FileInfo) {
	t.Helper()
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no block count")
	}
	if used := int64(sys.Blocks) * 512; used > 1<<20 {
		t.Errorf("%s uses %d bytes on disk; want sparse", path, used)
	}
}

func TestCheckDataDiskFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.img")
	if _, err := EnsureDataDisk(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDiskFree(path); err != nil {
		t.Fatal(err)
	}
	// Stand in for a hypervisor holding the disk.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDiskFree(path); !errors.Is(err, ErrDataDiskInUse) {
		t.Fatalf("held disk: %v, want ErrDataDiskInUse", err)
	}
	_ = f.Close()
	if err := checkDataDiskFree(path); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if err := checkDataDiskFree(filepath.Join(t.TempDir(), "missing.img")); err == nil {
		t.Fatal("missing disk accepted")
	}
}
