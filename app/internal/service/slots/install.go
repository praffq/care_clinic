package slots

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

// maxFileBytes caps each bundle file; nothing else may be in the tarball.
var maxFileBytes = map[string]int64{
	ManifestFile: 64 << 10,
	"vmlinuz":    256 << 20,
	"initramfs":  256 << 20,
	"system.img": 4 << 30,
}

// MaxTarballBytes caps an engine tarball: the file caps plus gzip and tar
// overhead.
const MaxTarballBytes = 5 << 30

// maxTrailerBytes bounds decompressed data after the tar end marker.
const maxTrailerBytes = 1 << 20

// Install verifies tarball against wantSHA256 (hex), unpacks it safely,
// verifies every file against its manifest and installs it as the slot for
// the manifest version. Installing a tarball that is already installed (same
// SHA-256, slot still verifies) returns the existing slot. A different
// tarball for the current slot's version is refused with ErrIsCurrent.
func (s *Store) Install(ctx context.Context, tarball, wantSHA256 string) (Slot, error) {
	want := strings.ToLower(strings.TrimSpace(wantSHA256))
	if !validSHA256(want) {
		return Slot{}, errors.New("install engine: sha256 must be 64 hex digits")
	}
	f, err := os.Open(tarball)
	if err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	} else if !st.Mode().IsRegular() {
		return Slot{}, fmt.Errorf("install engine: %s is not a regular file", tarball)
	}
	// Reject a wrong file before parsing any of it.
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return Slot{}, fmt.Errorf("install engine: tarball sha256 %s, want %s: %w", got, want, ErrChecksum)
	}
	if sl, ok := s.installed(want); ok {
		return sl, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Slot{}, err
	}

	s.mu.Lock()
	s.installing++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.installing--
		s.mu.Unlock()
	}()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	tmp, err := os.MkdirTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return Slot{}, err
	}

	// Hash again while unpacking so a file swapped after the first pass is
	// caught before commit.
	h.Reset()
	m, err := s.unpack(ctxReader{ctx, io.TeeReader(f, h)}, tmp)
	if err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	if _, err := io.Copy(io.Discard, ctxReader{ctx, io.TeeReader(f, h)}); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return Slot{}, fmt.Errorf("install engine: tarball changed while installing: %w", ErrChecksum)
	}
	if err := writeSynced(filepath.Join(tmp, VerifiedFile), []byte(want+"\n")); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	if err := syncDir(tmp); err != nil {
		return Slot{}, fmt.Errorf("install engine: %w", err)
	}
	if err := s.commit(tmp, m.Version); err != nil {
		return Slot{}, fmt.Errorf("install engine %s: %w", m.Version, err)
	}
	return s.readSlot(m.Version)
}

// installed returns a slot installed from the tarball with sha256 sum that
// still verifies.
func (s *Store) installed(sum string) (Slot, bool) {
	list, _ := s.List()
	for _, sl := range list {
		if sl.TarballSHA256 == sum {
			if v, err := s.Verify(sl.Version); err == nil {
				return v, true
			}
		}
	}
	return Slot{}, false
}

// commit renames the verified tmp dir to the slot for version, replacing an
// existing non-current slot.
func (s *Store) commit(tmp, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.Pointer()
	if err != nil {
		return err
	}
	dst := s.Dir(version)
	if _, err := os.Lstat(dst); err == nil {
		if version == p.Current {
			return ErrIsCurrent
		}
		old := tmp + ".old"
		if err := os.Rename(dst, old); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(old) }()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// unpack extracts the bundle into dir and checks it against its manifest.
// Only the top-level directory and the four bundle files may appear, once
// each, as a plain directory and regular files.
func (s *Store) unpack(r io.Reader, dir string) (Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Manifest{}, err
	}
	tr := tar.NewReader(gz)
	var top string
	sums := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read bundle: %w", err)
		}
		name := hdr.Name
		root, rest, _ := strings.Cut(name, "/")
		if !strings.HasPrefix(root, "care-engine-") || (top != "" && root != top) {
			return Manifest{}, fmt.Errorf("unexpected bundle entry %q", name)
		}
		top = root
		switch {
		case hdr.Typeflag == tar.TypeDir && rest == "":
			continue
		case hdr.Typeflag != tar.TypeReg:
			return Manifest{}, fmt.Errorf("bundle entry %q is not a regular file", name)
		}
		limit, ok := maxFileBytes[rest]
		if !ok {
			return Manifest{}, fmt.Errorf("unexpected bundle entry %q", name)
		}
		if _, dup := sums[rest]; dup {
			return Manifest{}, fmt.Errorf("duplicate bundle entry %q", name)
		}
		if hdr.Size > limit {
			return Manifest{}, fmt.Errorf("bundle entry %q is too large (%d bytes)", name, hdr.Size)
		}
		sum, err := extractFile(filepath.Join(dir, rest), tr)
		if err != nil {
			return Manifest{}, err
		}
		sums[rest] = sum
	}
	if n, _ := io.Copy(io.Discard, io.LimitReader(gz, maxTrailerBytes+1)); n > maxTrailerBytes {
		return Manifest{}, errors.New("bundle has trailing data")
	}
	if _, ok := sums[ManifestFile]; !ok {
		return Manifest{}, errors.New("bundle has no manifest.json")
	}
	m, err := readManifest(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	if err := s.checkManifest(m); err != nil {
		return Manifest{}, err
	}
	if want := "care-engine-" + m.Version + "-" + m.Arch; top != want {
		return Manifest{}, fmt.Errorf("bundle directory %q does not match manifest (%s)", top, want)
	}
	for _, name := range bootFiles {
		got, ok := sums[name]
		if !ok {
			return Manifest{}, fmt.Errorf("bundle has no %s", name)
		}
		if got != m.Files[name] {
			return Manifest{}, fmt.Errorf("%s does not match manifest: %w", name, ErrChecksum)
		}
	}
	return m, nil
}

func extractFile(path string, r io.Reader) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("extract %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func syncDir(dir string) error { return atomicfile.SyncDir(dir) }

func hashFile(path string) (string, error) {
	if st, err := os.Lstat(path); err != nil {
		return "", err
	} else if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
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

// ctxReader stops reading once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
