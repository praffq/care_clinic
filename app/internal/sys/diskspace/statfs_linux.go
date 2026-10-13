//go:build linux

package diskspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// filesystemID names the shared filesystem. On Btrfs, Statfs Fsid differs per
// subvolume, so the filesystem UUID from the Btrfs info ioctl is used instead.
func filesystemID(path string, fs *unix.Statfs_t) (string, error) {
	if int64(fs.Type) != unix.BTRFS_SUPER_MAGIC {
		return fmt.Sprintf("%v", fs.Fsid), nil
	}
	return btrfsFilesystemID(path)
}

func statBlockSize(fs *unix.Statfs_t) uint64 {
	if fs.Frsize > 0 {
		return uint64(fs.Frsize)
	}
	if fs.Bsize > 0 {
		return uint64(fs.Bsize)
	}
	return 0
}
