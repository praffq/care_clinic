//go:build !linux && !windows

package diskspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func statBlockSize(fs *unix.Statfs_t) uint64 {
	return uint64(fs.Bsize)
}

func filesystemID(_ string, fs *unix.Statfs_t) (string, error) {
	return fmt.Sprintf("%v", fs.Fsid), nil
}
