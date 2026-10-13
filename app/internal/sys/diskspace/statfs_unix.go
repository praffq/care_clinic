//go:build !windows

package diskspace

import "golang.org/x/sys/unix"

func statVolume(path string) (Usage, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return Usage{}, err
	}
	id, err := filesystemID(path, &fs)
	if err != nil {
		return Usage{}, err
	}
	bsize := statBlockSize(&fs)
	return Usage{
		Volume: id,
		Free:   uint64(fs.Bavail) * bsize,
		Total:  uint64(fs.Blocks) * bsize,
	}, nil
}
