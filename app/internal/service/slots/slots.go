// Package slots manages installed engine bundles ("slots") in the service's
// data directory:
//
//	<dir>/<version>/            verified, unpacked bundle (see engine/README.md)
//	<dir>/<version>/.verified   SHA-256 of the tarball it was installed from
//	<dir>/current               JSON pointer {current, previous}
//
// A slot is created only by Install, which verifies the tarball and every
// file before atomically renaming a temporary directory into place. The
// current slot is never deleted or overwritten; previous is the single
// fallback kept by Prune.
package slots

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

const (
	ManifestFile = "manifest.json"
	VerifiedFile = ".verified"
	pointerFile  = "current"
	tempPrefix   = ".install-"
)

// bootFiles are the files manifest.json lists with their SHA-256.
var bootFiles = []string{"vmlinuz", "initramfs", "system.img"}

var (
	ErrNoCurrent    = errors.New("no engine slot is active")
	ErrNotInstalled = errors.New("engine slot is not installed")
	ErrIsCurrent    = errors.New("engine slot is the current slot")
	ErrChecksum     = errors.New("checksum mismatch")
)

// Manifest is a bundle's manifest.json.
type Manifest struct {
	Version    string            `json:"version"`
	Arch       string            `json:"arch"`
	DataSchema int               `json:"data_schema"`
	Files      map[string]string `json:"files"`
}

// Slot describes an installed bundle.
type Slot struct {
	Version       string `json:"version"`
	Arch          string `json:"arch"`
	DataSchema    int    `json:"data_schema"`
	TarballSHA256 string `json:"tarball_sha256"`
	Dir           string `json:"dir"`
}

// Pointer is the persisted active-slot record.
type Pointer struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

// Store manages the slots under one directory. Its methods are safe for
// concurrent use; one process owns a directory.
type Store struct {
	dir  string
	arch string

	mu         sync.Mutex // pointer updates, commits and Prune
	installing int        // installs in progress (their temp dirs are live)
}

// New returns a Store for dir (created on first install) accepting bundles
// for arch; an empty arch means the host's (runtime.GOARCH).
func New(dir, arch string) *Store {
	if arch == "" {
		arch = runtime.GOARCH
	}
	return &Store{dir: dir, arch: arch}
}

// Dir returns the directory of the slot for version.
func (s *Store) Dir(version string) string { return filepath.Join(s.dir, version) }

// Pointer returns the active-slot record; Current is empty when none is set.
func (s *Store) Pointer() (Pointer, error) {
	var p Pointer
	b, err := os.ReadFile(filepath.Join(s.dir, pointerFile))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("engine slot pointer: %w", err)
	}
	return p, nil
}

// Current returns the active slot after a full Verify, or ErrNoCurrent.
func (s *Store) Current() (Slot, error) {
	p, err := s.Pointer()
	if err != nil {
		return Slot{}, err
	}
	if p.Current == "" {
		return Slot{}, ErrNoCurrent
	}
	return s.Verify(p.Current)
}

// Activate makes version (which must verify) the current slot; the old
// current becomes previous. Activating the current slot changes nothing.
func (s *Store) Activate(version string) error {
	if _, err := s.Verify(version); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.Pointer()
	if err != nil {
		return err
	}
	if p.Current == version {
		return nil
	}
	next := Pointer{Current: version, Previous: p.Current}
	if next.Previous == "" {
		next.Previous = p.Previous
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(s.dir, pointerFile), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("activate engine %s: %w", version, err)
	}
	return nil
}

// List returns the installed slots sorted by version. It reads only their
// manifests and markers; see Verify for a full check.
func (s *Store) List() ([]Slot, error) {
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Slot
	for _, e := range ents {
		if !e.IsDir() || !validVersion(e.Name()) {
			continue
		}
		if sl, err := s.readSlot(e.Name()); err == nil {
			out = append(out, sl)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := versions.ParseSemver(out[i].Version)
		b, _ := versions.ParseSemver(out[j].Version)
		return a.Compare(b) < 0
	})
	return out, nil
}

// Verify re-checks an installed slot: marker, manifest and every file hash.
func (s *Store) Verify(version string) (Slot, error) {
	sl, m, err := s.readSlotManifest(version)
	if err != nil {
		return Slot{}, err
	}
	for _, name := range bootFiles {
		got, err := hashFile(filepath.Join(sl.Dir, name))
		if err != nil {
			return Slot{}, fmt.Errorf("engine slot %s: %w", version, err)
		}
		if got != m.Files[name] {
			return Slot{}, fmt.Errorf("engine slot %s: %s: %w", version, name, ErrChecksum)
		}
	}
	return sl, nil
}

func (s *Store) readSlot(version string) (Slot, error) {
	sl, _, err := s.readSlotManifest(version)
	return sl, err
}

func (s *Store) readSlotManifest(version string) (Slot, Manifest, error) {
	if !validVersion(version) {
		return Slot{}, Manifest{}, fmt.Errorf("engine slot %q: invalid version", version)
	}
	dir := s.Dir(version)
	st, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Slot{}, Manifest{}, fmt.Errorf("engine %s: %w", version, ErrNotInstalled)
	}
	if err != nil {
		return Slot{}, Manifest{}, err
	}
	if !st.IsDir() {
		return Slot{}, Manifest{}, fmt.Errorf("engine slot %s is not a directory", version)
	}
	mark, err := os.ReadFile(filepath.Join(dir, VerifiedFile))
	if errors.Is(err, os.ErrNotExist) {
		return Slot{}, Manifest{}, fmt.Errorf("engine slot %s is not verified: %w", version, ErrNotInstalled)
	}
	if err != nil {
		return Slot{}, Manifest{}, err
	}
	sum := strings.TrimSpace(string(mark))
	if !validSHA256(sum) {
		return Slot{}, Manifest{}, fmt.Errorf("engine slot %s: bad %s marker", version, VerifiedFile)
	}
	m, err := readManifest(filepath.Join(dir, ManifestFile))
	if err == nil {
		err = s.checkManifest(m)
	}
	if err == nil && m.Version != version {
		err = fmt.Errorf("directory holds version %s", m.Version)
	}
	if err != nil {
		return Slot{}, Manifest{}, fmt.Errorf("engine slot %s: %w", version, err)
	}
	return Slot{Version: m.Version, Arch: m.Arch, DataSchema: m.DataSchema, TarballSHA256: sum, Dir: dir}, m, nil
}

// Prune deletes every slot except current and previous, plus leftovers of
// interrupted installs, and returns the deleted versions.
func (s *Store) Prune() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.Pointer()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, tempPrefix):
			if s.installing > 0 {
				continue
			}
		case e.IsDir() && validVersion(name) && name != p.Current && name != p.Previous:
			removed = append(removed, name)
		default:
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

func (s *Store) checkManifest(m Manifest) error {
	if !validVersion(m.Version) {
		return fmt.Errorf("manifest version %q is not a semantic version", m.Version)
	}
	if m.Arch != s.arch {
		return fmt.Errorf("bundle is for %q, this host needs %q", m.Arch, s.arch)
	}
	if len(m.Files) != len(bootFiles) {
		return fmt.Errorf("manifest lists %d files, want %d", len(m.Files), len(bootFiles))
	}
	for _, name := range bootFiles {
		if !validSHA256(m.Files[name]) {
			return fmt.Errorf("manifest: missing or bad sha256 for %s", name)
		}
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

// validVersion also guarantees that v is a safe path segment.
func validVersion(v string) bool {
	_, err := versions.ParseSemver(v)
	return err == nil
}

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
