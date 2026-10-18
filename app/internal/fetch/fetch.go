// Package fetch downloads checksum-pinned files. Downloads resume from a
// dest.part file, so an interrupted build or install picks up where it
// stopped, and dest only appears once its sha256 matches the pin.
package fetch

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
)

// ErrChecksum means the downloaded bytes don't match the pinned sha256.
var ErrChecksum = errors.New("checksum mismatch")

// File downloads url to dest unless dest already has the pinned sha256.
func File(ctx context.Context, client *http.Client, url, sha string, dest string) error {
	sha = strings.ToLower(sha)
	if got, err := hashFile(dest); err == nil && got == sha {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	part := dest + ".part"
	if err := download(ctx, client, url, part); err != nil {
		return err
	}
	got, err := hashFile(part)
	if err != nil {
		return err
	}
	if got != sha {
		// A corrupt .part must not be resumed again.
		_ = os.Remove(part)
		return fmt.Errorf("%s: %w: got %s, want %s", url, ErrChecksum, got, sha)
	}
	return os.Rename(part, dest)
}

func download(ctx context.Context, client *http.Client, url, part string) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
			return fmt.Errorf("%s: unexpected Content-Range %q", url, resp.Header.Get("Content-Range"))
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0:
		// The .part is already complete (or longer than the file);
		// the checksum decides.
		return nil
	case resp.StatusCode == http.StatusOK:
		// The server ignored Range: start over.
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return f.Close()
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
