//go:build !windows

package atomicfile

import (
	"errors"
	"os"
)

// SyncDir flushes dir's entries (renames and creations in it) to disk.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
