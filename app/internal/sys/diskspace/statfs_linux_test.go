//go:build linux

package diskspace

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestStatBlockSizeUsesFragmentSize(t *testing.T) {
	fs := unix.Statfs_t{Bsize: 4096, Frsize: 1024}
	if got := statBlockSize(&fs); got != 1024 {
		t.Fatalf("statBlockSize = %d, want fragment size 1024", got)
	}
	fs.Frsize = 0
	if got := statBlockSize(&fs); got != 4096 {
		t.Fatalf("statBlockSize fallback = %d, want block size 4096", got)
	}
}
