package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// MaxDownloadBytes bounds a download whose size is not known in advance.
const MaxDownloadBytes = 2 << 30

// ErrChecksum means a finished download does not match its published
// SHA-256 (or size). The partial file is deleted, so the next attempt
// starts over.
var ErrChecksum = errors.New("download does not match its published checksum")

// Progress reports done of total bytes (total 0 when unknown).
type Progress func(done, total int64)

// FetchFile downloads url to dst and verifies it against sum (lowercase hex
// SHA-256) and size (0: unknown). A verified dst is reused as is. Bytes go
// to dst+".part" first; an interrupted download resumes from it with an
// HTTP Range request, and only a verified file is renamed to dst.
func FetchFile(ctx context.Context, hc *http.Client, url, dst, sum string, size int64, progress Progress) error {
	if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
		return fmt.Errorf("download %s: invalid SHA-256 %q", url, sum)
	}
	if got, n, err := hashFile(dst); err == nil && got == sum && (size == 0 || n == size) {
		if progress != nil {
			progress(n, n)
		}
		return nil
	}
	part := dst + ".part"
	if err := fetchPart(ctx, hc, url, part, size, progress); err != nil {
		return err
	}
	got, n, err := hashFile(part)
	if err != nil {
		return err
	}
	if got != sum || (size > 0 && n != size) {
		_ = os.Remove(part)
		return fmt.Errorf("%w: %s is %d bytes with SHA-256 %s, want %d bytes with %s", ErrChecksum, url, n, got, size, sum)
	}
	return os.Rename(part, dst)
}

func fetchPart(ctx context.Context, hc *http.Client, url, part string, size int64, progress Progress) error {
	if hc == nil {
		hc = http.DefaultClient
	}
	limit := size
	if limit <= 0 {
		limit = MaxDownloadBytes
	}
	var offset int64
	if st, err := os.Stat(part); err == nil && st.Mode().IsRegular() {
		offset = st.Size()
	}
	if size > 0 && offset == size {
		return nil // complete; FetchFile verifies it
	}
	if offset > limit {
		offset = 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "care-clinic")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent && rangeStartsAt(resp.Header.Get("Content-Range"), offset):
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		offset = 0
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// The partial file is not a prefix of what the server has now.
		_ = os.Remove(part)
		return fmt.Errorf("download %s: the server rejected resuming; try again", url)
	default:
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	w := &countingWriter{done: offset, total: size, progress: progress}
	if progress != nil {
		progress(offset, size)
	}
	_, err = io.Copy(io.MultiWriter(f, w), io.LimitReader(resp.Body, limit-offset+1))
	if progress != nil {
		progress(w.done, size)
	}
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s did not finish (it resumes from %d bytes next time): %w", url, w.done, err)
	}
	if w.done > limit {
		_ = os.Remove(part)
		return fmt.Errorf("download %s: larger than %d bytes", url, limit)
	}
	return nil
}

// rangeStartsAt reports whether a Content-Range header ("bytes 10-99/100")
// starts at offset.
func rangeStartsAt(header string, offset int64) bool {
	spec, ok := strings.CutPrefix(header, "bytes ")
	if !ok {
		return false
	}
	start, _, ok := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(start, 10, 64)
	return ok && err == nil && n == offset
}

type countingWriter struct {
	done, total int64
	last        time.Time
	progress    Progress
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.progress != nil && time.Since(w.last) >= 200*time.Millisecond {
		w.last = time.Now()
		w.progress(w.done, w.total)
	}
	return len(p), nil
}
