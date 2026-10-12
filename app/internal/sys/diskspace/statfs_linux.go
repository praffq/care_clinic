//go:build linux

package diskspace

import "golang.org/x/sys/unix"

func statBlockSize(fs *unix.Statfs_t) uint64 {
	if fs.Frsize > 0 {
		return uint64(fs.Frsize)
	}
	if fs.Bsize > 0 {
		return uint64(fs.Bsize)
	}
	return 0
}
