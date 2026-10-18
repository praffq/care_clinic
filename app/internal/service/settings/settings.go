// Package settings persists the host service's configuration in config.json
// under the service root.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
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

// Open loads path, using defaults for a missing file or missing fields.
// onChange, if set, is called after every successful Update. When the file
// cannot be read or is invalid, Open returns the error together with a Store
// holding the defaults, so the service can still come up; the next Update
// replaces the file.
func Open(path string, onChange func(api.ServiceConfig)) (*Store, error) {
	s := &Store{path: path, onChange: onChange, cfg: api.ServiceConfig{MDNSName: DefaultMDNSName}}
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
		cfg.MDNSName = DefaultMDNSName
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
	return nil
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
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
