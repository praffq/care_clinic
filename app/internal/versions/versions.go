// Package versions loads and validates versions.yml, the human-edited file
// that pins the desktop, engine, CARE and support-image versions of a release.
package versions

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SchemaVersion is the only schema value this package accepts.
const SchemaVersion = 1

// File is the decoded contents of versions.yml.
type File struct {
	Schema  int             `yaml:"schema"`
	Desktop Component       `yaml:"desktop"`
	Engine  Component       `yaml:"engine"`
	Care    Care            `yaml:"care"`
	Images  Images          `yaml:"images"`
	Tools   map[string]Tool `yaml:"tools"`
}

// Component is a versioned artefact with compatibility requirements.
type Component struct {
	Version  Version  `yaml:"version"`
	Requires Requires `yaml:"requires"`
}

// Care pins the CARE application and its source repositories.
type Care struct {
	Version  Version  `yaml:"version"`
	Requires Requires `yaml:"requires"`
	Backend  Source   `yaml:"backend"`
	Frontend Source   `yaml:"frontend"`
}

// Requires lists version constraints on sibling components.
type Requires struct {
	Desktop *Constraint `yaml:"desktop"`
	Engine  *Constraint `yaml:"engine"`
}

// Source is a GitHub repository and git ref.
type Source struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

// Images pins third-party container images.
type Images struct {
	Postgres string `yaml:"postgres"`
	Redis    string `yaml:"redis"`
	Caddy    string `yaml:"caddy"`
	Silo     string `yaml:"silo"`
}

// Tool is a downloadable host-side binary pinned by checksum.
type Tool struct {
	Version Version `yaml:"version"`
	URL     string  `yaml:"url"`
	SHA256  string  `yaml:"sha256"`
}

// Version is a semver value that must be written as a YAML string.
type Version struct {
	Semver
	raw string
	tag string
}

func (v Version) String() string { return v.raw }

// UnmarshalYAML records the raw scalar and its resolved tag; validation
// happens in Parse so errors can carry the field path.
func (v *Version) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: version must be a string", n.Line)
	}
	v.raw, v.tag = n.Value, n.ShortTag()
	return nil
}

// UnmarshalYAML records the raw scalar; it is parsed during validation.
func (c *Constraint) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: constraint must be a string", n.Line)
	}
	c.raw = n.Value
	return nil
}

// Load reads and parses the versions file at path.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Parse decodes and validates versions.yml contents. Unknown keys are errors.
func Parse(data []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty document")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected a single YAML document")
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

type errs []error

func (e *errs) add(path, format string, args ...any) {
	*e = append(*e, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
}

var (
	repoRE     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)
	sha256RE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	toolNameRE = regexp.MustCompile(`^[a-z0-9]+(?:_[a-z0-9]+)*$`)
)

func (f *File) validate() error {
	var e errs
	if f.Schema != SchemaVersion {
		e.add("schema", "must be %d, got %d", SchemaVersion, f.Schema)
	}

	e.version("desktop.version", &f.Desktop.Version)
	e.constraint("desktop.requires.engine", f.Desktop.Requires.Engine, true)
	if f.Desktop.Requires.Desktop != nil {
		e.add("desktop.requires.desktop", "a component cannot require itself")
	}

	e.version("engine.version", &f.Engine.Version)
	e.constraint("engine.requires.desktop", f.Engine.Requires.Desktop, true)
	if f.Engine.Requires.Engine != nil {
		e.add("engine.requires.engine", "a component cannot require itself")
	}

	e.version("care.version", &f.Care.Version)
	e.constraint("care.requires.desktop", f.Care.Requires.Desktop, true)
	e.constraint("care.requires.engine", f.Care.Requires.Engine, true)
	e.source("care.backend", f.Care.Backend)
	e.source("care.frontend", f.Care.Frontend)

	e.image("images.postgres", f.Images.Postgres)
	e.image("images.redis", f.Images.Redis)
	e.image("images.caddy", f.Images.Caddy)
	e.image("images.silo", f.Images.Silo)

	names := make([]string, 0, len(f.Tools))
	for name := range f.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := "tools." + name
		if !toolNameRE.MatchString(name) {
			e.add(p, "name must be lower_snake_case")
		}
		t := f.Tools[name]
		e.version(p+".version", &t.Version)
		e.httpsURL(p+".url", t.URL)
		if t.SHA256 == "" {
			e.add(p+".sha256", "required")
		} else if !sha256RE.MatchString(t.SHA256) {
			e.add(p+".sha256", "must be 64 lowercase hex characters")
		}
		f.Tools[name] = t
	}
	return errors.Join(e...)
}

func (e *errs) version(path string, v *Version) {
	switch {
	case v.raw == "" && (v.tag == "" || v.tag == "!!null"):
		e.add(path, "required")
		return
	case v.tag != "!!str":
		e.add(path, "must be a quoted string, got %s %q", strings.TrimPrefix(v.tag, "!!"), v.raw)
		return
	}
	sv, err := ParseSemver(v.raw)
	if err != nil {
		e.add(path, "%v", err)
		return
	}
	v.Semver = sv
}

func (e *errs) constraint(path string, c *Constraint, required bool) {
	if c == nil {
		if required {
			e.add(path, "required")
		}
		return
	}
	parsed, err := ParseConstraint(c.raw)
	if err != nil {
		e.add(path, "%v", err)
		return
	}
	*c = parsed
}

func (e *errs) source(path string, s Source) {
	if s.Repo == "" {
		e.add(path+".repo", "required")
	} else if !repoRE.MatchString(s.Repo) {
		e.add(path+".repo", "must be owner/name, got %q", s.Repo)
	}
	if strings.TrimSpace(s.Ref) == "" {
		e.add(path+".ref", "required")
	}
}

func (e *errs) image(path, ref string) {
	if ref == "" {
		e.add(path, "required")
		return
	}
	if strings.ContainsAny(ref, " \t\n") {
		e.add(path, "must not contain whitespace")
		return
	}
	name, digest, _ := strings.Cut(ref, "@")
	tag := ""
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		tag = name[i+1:]
	}
	switch {
	case tag == "" && digest == "":
		e.add(path, "must pin a tag or digest, got %q", ref)
	case tag == "latest" || tag == "edge":
		e.add(path, "floating tag %q is not allowed", tag)
	}
}

func (e *errs) httpsURL(path, raw string) {
	if raw == "" {
		e.add(path, "required")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		e.add(path, "must be an https URL, got %q", raw)
	}
}
