// Package plugins describes the CARE backend plugins (Django apps, CARE's
// "Plug") a clinic runs: the bundled catalog of presets, free-form plugins
// added by the clinic, the ADDITIONAL_PLUGS value CARE reads at runtime and
// the specs image.build bakes into a derived backend image.
//
// Frontend plugins are not managed here: a CARE admin registers them in
// CARE's plugin settings (PlugConfig).
package plugins

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"go.yaml.in/yaml/v3"
)

// Plugin is one enabled backend plugin. ID is its Django app name (CARE's
// Plug.name); Package plus Version is what pip installs.
type Plugin struct {
	ID      string         `json:"id" yaml:"id"`
	Package string         `json:"package" yaml:"package"`
	Version string         `json:"version,omitempty" yaml:"version"`
	Configs map[string]any `json:"configs,omitempty" yaml:"configs"`
	// Bundled plugins are already installed in the CARE backend image.
	Bundled bool `json:"bundled,omitempty" yaml:"bundled"`
}

// Preset is a catalog entry.
type Preset struct {
	Plugin          `yaml:",inline"`
	Label           string   `json:"label" yaml:"label"`
	Description     string   `json:"description" yaml:"description"`
	Default         bool     `json:"default,omitempty" yaml:"default"`
	RequiredConfigs []string `json:"required_configs,omitempty" yaml:"required_configs"`
}

//go:embed catalog.yml
var catalogYAML []byte

var (
	idRE        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	configKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,99}$`)
	secretRE    = regexp.MustCompile(`(?i)(KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL)`)
)

// Catalog returns the bundled presets.
func Catalog() []Preset {
	var out []Preset
	if err := yaml.Unmarshal(catalogYAML, &out); err != nil {
		panic("plugins: invalid catalog.yml: " + err.Error())
	}
	for _, p := range out {
		if err := p.Validate(); err != nil {
			panic("plugins: invalid catalog.yml: " + err.Error())
		}
	}
	return out
}

// FindPreset returns the catalog entry id.
func FindPreset(id string) (Preset, bool) {
	for _, p := range Catalog() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Defaults is the plugin set of a new clinic: the default presets.
func Defaults() []Plugin {
	var out []Plugin
	for _, p := range Catalog() {
		if p.Default {
			out = append(out, p.clone())
		}
	}
	return out
}

func (p Plugin) clone() Plugin {
	p.Configs = maps.Clone(p.Configs)
	return p
}

// Validate checks p's fields with the same rules the engine applies to
// image.build, so a plugin that validates here can be built.
func (p Plugin) Validate() error {
	var errs []error
	if !idRE.MatchString(p.ID) {
		errs = append(errs, fmt.Errorf("plugin name %q must be a Django app name (lowercase letters, digits and _)", p.ID))
	}
	if !protocol.ValidPluginPackage(p.Package) {
		errs = append(errs, fmt.Errorf("%s: package %q must be a PyPI package name or a git+https:// URL", p.ID, p.Package))
	}
	if !protocol.ValidPluginVersion(p.Version) {
		errs = append(errs, fmt.Errorf("%s: version %q must be @<git ref> (for git URLs) or a pip specifier such as ==1.2", p.ID, p.Version))
	} else if strings.HasPrefix(p.Version, "@") != strings.HasPrefix(p.Package, "git+") && p.Version != "" {
		errs = append(errs, fmt.Errorf("%s: use @<ref> versions with git URLs and pip specifiers with package names", p.ID))
	}
	for k := range p.Configs {
		if !configKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("%s: config name %q is not valid", p.ID, k))
		}
	}
	return errors.Join(errs...)
}

// Missing returns the required configs of p's preset that are unset or empty.
func Missing(p Plugin) []string {
	pre, ok := FindPreset(p.ID)
	if !ok {
		return nil
	}
	var out []string
	for _, k := range pre.RequiredConfigs {
		if v, ok := p.Configs[k]; !ok || v == nil || v == "" {
			out = append(out, k)
		}
	}
	return out
}

// ValidateSet checks every plugin, unique IDs and required configs.
func ValidateSet(set []Plugin) error {
	var errs []error
	seen := map[string]bool{}
	for _, p := range set {
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("plugin %s is listed twice", p.ID))
		}
		seen[p.ID] = true
		if m := Missing(p); len(m) > 0 {
			errs = append(errs, fmt.Errorf("%s needs %s", p.ID, strings.Join(m, ", ")))
		}
	}
	if len(set) > protocol.MaxPlugins {
		errs = append(errs, fmt.Errorf("at most %d plugins", protocol.MaxPlugins))
	}
	return errors.Join(errs...)
}

// plug is CARE's Plug dataclass (plugs/plug.py). Version is always written:
// CARE defaults it to "@main", which is wrong for PyPI packages.
type plug struct {
	Name        string         `json:"name"`
	PackageName string         `json:"package_name"`
	Version     string         `json:"version"`
	Configs     map[string]any `json:"configs"`
}

// AdditionalPlugs renders set as CARE's ADDITIONAL_PLUGS JSON. It holds
// plugin configs, which may be secrets: never log it.
func AdditionalPlugs(set []Plugin) (string, error) {
	out := make([]plug, 0, len(set))
	for _, p := range set {
		cfg := p.Configs
		if cfg == nil {
			cfg = map[string]any{}
		}
		out = append(out, plug{Name: p.ID, PackageName: p.Package, Version: p.Version, Configs: cfg})
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// BuildSpecs are the plugins image.build must install: those not bundled in
// the CARE backend image. None means the base image runs as is.
func BuildSpecs(set []Plugin) []protocol.PluginSpec {
	var out []protocol.PluginSpec
	for _, p := range set {
		if !p.Bundled {
			out = append(out, protocol.PluginSpec{Name: p.ID, Package: p.Package, Version: p.Version})
		}
	}
	return out
}

// IsSecret reports whether a config name looks like a credential.
func IsSecret(key string) bool { return secretRE.MatchString(key) }

// RedactedValue replaces secret config values in redacted plugins.
const RedactedValue = "********"

// Redacted returns p with secret config values masked, for display and logs.
func (p Plugin) Redacted() Plugin {
	p = p.clone()
	for k, v := range p.Configs {
		if IsSecret(k) && v != nil && v != "" {
			p.Configs[k] = RedactedValue
		}
	}
	return p
}

// RedactAll redacts every plugin of set.
func RedactAll(set []Plugin) []Plugin {
	out := make([]Plugin, 0, len(set))
	for _, p := range set {
		out = append(out, p.Redacted())
	}
	return out
}

// Summary names a set for journals and history, e.g. "care_filly,x" or "none".
func Summary(set []Plugin) string {
	ids := make([]string, 0, len(set))
	for _, p := range set {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}

// Enable returns set with p added or replaced (same ID). A preset ID
// without a package takes the preset's package, version and bundled flag;
// the preset's configs are the defaults for configs p leaves unset. Bundled
// is derived, not taken from p.
func Enable(set []Plugin, p Plugin) ([]Plugin, error) {
	if pre, ok := FindPreset(p.ID); ok && p.Package == "" {
		cfg := maps.Clone(pre.Configs)
		if cfg == nil {
			cfg = map[string]any{}
		}
		maps.Copy(cfg, p.Configs)
		p = pre.clone()
		p.Configs = cfg
	} else {
		// Only a preset's own source is in the CARE image; never trust the
		// caller's flag.
		p.Bundled = ok && pre.Bundled && p.Package == pre.Package && p.Version == pre.Version
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if m := Missing(p); len(m) > 0 {
		return nil, &MissingConfigError{ID: p.ID, Keys: m}
	}
	out := slices.Clone(set)
	if i := slices.IndexFunc(out, func(q Plugin) bool { return q.ID == p.ID }); i >= 0 {
		out[i] = p
	} else {
		out = append(out, p)
	}
	return out, ValidateSet(out)
}

// Disable returns set without id.
func Disable(set []Plugin, id string) ([]Plugin, error) {
	i := slices.IndexFunc(set, func(q Plugin) bool { return q.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("plugin %s is not enabled", id)
	}
	return slices.Delete(slices.Clone(set), i, i+1), nil
}

// MissingConfigError means a preset was enabled without its required configs.
type MissingConfigError struct {
	ID   string
	Keys []string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf("%s needs %s", e.ID, strings.Join(e.Keys, ", "))
}

// ParseConfig parses a "KEY=value" argument; value is JSON when it parses
// as JSON (lists, numbers, booleans), else the plain string.
func ParseConfig(arg string) (string, any, error) {
	k, v, ok := strings.Cut(arg, "=")
	if !ok || !configKeyRE.MatchString(k) {
		return "", nil, fmt.Errorf("config %q must be KEY=value", arg)
	}
	var j any
	if err := json.Unmarshal([]byte(v), &j); err == nil {
		return k, j, nil
	}
	return k, v, nil
}

// ParseAdditionalPlugs reads an ADDITIONAL_PLUGS value back into a set. A
// plugin matching its preset's package and version counts as bundled.
func ParseAdditionalPlugs(s string) ([]Plugin, error) {
	var in []plug
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, fmt.Errorf("ADDITIONAL_PLUGS: %w", err)
	}
	out := make([]Plugin, 0, len(in))
	for _, q := range in {
		p := Plugin{ID: q.Name, Package: q.PackageName, Version: q.Version, Configs: q.Configs}
		if len(p.Configs) == 0 {
			p.Configs = nil
		}
		if pre, ok := FindPreset(p.ID); ok && pre.Bundled && pre.Package == p.Package && pre.Version == p.Version {
			p.Bundled = true
		}
		out = append(out, p)
	}
	return out, nil
}

// BundledOnly returns the plugins of set the CARE backend image has.
func BundledOnly(set []Plugin) []Plugin {
	var out []Plugin
	for _, p := range set {
		if p.Bundled {
			out = append(out, p.clone())
		}
	}
	return out
}

// Equal reports whether a and b render the same ADDITIONAL_PLUGS.
func Equal(a, b []Plugin) bool {
	x, errA := AdditionalPlugs(a)
	y, errB := AdditionalPlugs(b)
	return errA == nil && errB == nil && x == y
}
