//go:build !windows

package diskspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func statVolume(path string) (Usage, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return Usage{}, err
	}
	bsize := statBlockSize(&fs)
	return Usage{
		Volume: fmt.Sprintf("%v", fs.Fsid),
		Free:   uint64(fs.Bavail) * bsize,
		Total:  uint64(fs.Blocks) * bsize,
	}, nil
}
