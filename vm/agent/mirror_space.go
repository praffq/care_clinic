package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Match the desktop's critical running-space policy. Uploads need their full
// temporary allocation even when replacing an existing mirrored file.
const mirrorReserveFree = uint64(5) << 30

var errMirrorCapacity = errors.New("not enough free space in the CARE engine")

func (a *api) checkMirrorSpace(dir string, size int64) error {
	free, _, err := a.space(dir)
	if err != nil {
		return fmt.Errorf("could not check CARE engine mirror capacity: %w", err)
	}
	need := uint64(0)
	if size > 0 {
		need = (uint64(size) + 4095) / 4096 * 4096
	}
	if free < mirrorReserveFree || need > free-mirrorReserveFree {
		return fmt.Errorf("%w: syncing needs %d additional bytes while preserving 5 GiB for clinic data; %d bytes are free. Free engine disk space or reduce the mirrored backup folder, then retry", errMirrorCapacity, need, free)
	}
	return nil
}

func (a *api) copyMirrorFile(dst *os.File, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var written int64
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			if spaceErr := a.checkMirrorSpace(filepath.Dir(dst.Name()), int64(n)); spaceErr != nil {
				return written, spaceErr
			}
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

func mirrorWriteError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, errMirrorCapacity) {
		code = http.StatusInsufficientStorage
	}
	http.Error(w, err.Error(), code)
}
