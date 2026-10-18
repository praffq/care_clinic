// Package settings persists the host service's configuration in config.json
// under the service root.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

// DefaultMDNSName is advertised as care.local until another name is set.
const DefaultMDNSName = "care"

// Store holds the configuration; it is safe for concurrent use.
type Store struct {
	path     string
	onChange func(api.ServiceConfig)

	mu  sync.Mutex
	cfg api.ServiceConfig
}

// Open loads path, using defaults for a missing file or missing fields
// (DefaultMDNSName when defaults has no name). onChange, if set, is called after every successful Update. When the file
// cannot be read or is invalid, Open returns the error together with a Store
// holding the defaults, so the service can still come up; the next Update
// replaces the file.
func Open(path string, defaults api.ServiceConfig, onChange func(api.ServiceConfig)) (*Store, error) {
	if defaults.MDNSName == "" {
		defaults.MDNSName = DefaultMDNSName
	}
	s := &Store{path: path, onChange: onChange, cfg: defaults}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	cfg := s.cfg
	if err := json.Unmarshal(b, &cfg); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.MDNSName == "" {
		cfg.MDNSName = defaults.MDNSName
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = defaults.BackupDir
	}
	if err := validate(&cfg); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg = cfg
	return s, nil
}

// Get returns the current configuration.
func (s *Store) Get() api.ServiceConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies the fields set in u, validates and normalizes the result,
// writes it atomically and applies it. Invalid values are reported as
// api.ErrInvalidConfig.
func (s *Store) Update(u api.ServiceConfigUpdate) (api.ServiceConfig, error) {
	s.mu.Lock()
	cfg := s.cfg
	if u.MDNSName != nil {
		cfg.MDNSName = *u.MDNSName
	}
	if u.ProxyProtocol != nil {
		cfg.ProxyProtocol = *u.ProxyProtocol
	}
	if u.BackupDir != nil {
		cfg.BackupDir = *u.BackupDir
	}
	if u.BackupCopyDir != nil {
		cfg.BackupCopyDir = *u.BackupCopyDir
	}
	err := validate(&cfg)
	if err == nil {
		err = writeAtomic(s.path, cfg)
	}
	if err == nil {
		s.cfg = cfg
	}
	s.mu.Unlock()
	if err != nil {
		return api.ServiceConfig{}, err
	}
	if s.onChange != nil {
		s.onChange(cfg)
	}
	return cfg, nil
}

func validate(cfg *api.ServiceConfig) error {
	cfg.MDNSName = mdns.Label(cfg.MDNSName)
	if err := mdns.ValidateLabel(cfg.MDNSName); err != nil {
		return fmt.Errorf("%w: mdns_name: %w", api.ErrInvalidConfig, err)
	}
	var err error
	if cfg.BackupDir, err = folder("backup_dir", cfg.BackupDir); err != nil {
		return err
	}
	if cfg.BackupCopyDir == "" {
		return nil
	}
	if cfg.BackupCopyDir, err = folder("backup_copy_dir", cfg.BackupCopyDir); err != nil {
		return err
	}
	if within(cfg.BackupCopyDir, cfg.BackupDir) || within(cfg.BackupDir, cfg.BackupCopyDir) {
		return fmt.Errorf("%w: backup_copy_dir: %q overlaps backup_dir %q; choose a folder on another drive", api.ErrInvalidConfig, cfg.BackupCopyDir, cfg.BackupDir)
	}
	return nil
}

// folder cleans d, an absolute path that is not a filesystem root.
func folder(key, d string) (string, error) {
	if !filepath.IsAbs(d) || strings.ContainsAny(d, "\x00\r\n") {
		return "", fmt.Errorf("%w: %s: %q is not an absolute path", api.ErrInvalidConfig, key, d)
	}
	c := filepath.Clean(d)
	if filepath.Dir(c) == c {
		return "", fmt.Errorf("%w: %s: %q is a filesystem root", api.ErrInvalidConfig, key, d)
	}
	return c, nil
}

// within reports whether path is dir or below it (both clean).
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeAtomic replaces path with cfg so that a crash leaves the old or the
// new file, never a partial one.
func writeAtomic(path string, cfg api.ServiceConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return atomicfile.SyncDir(dir)
}
