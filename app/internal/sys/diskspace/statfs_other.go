//go:build !linux && !windows

package diskspace

import "golang.org/x/sys/unix"

func statBlockSize(fs *unix.Statfs_t) uint64 {
	return uint64(fs.Bsize)
}
