package versions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `schema: 1
desktop:
  version: "0.2.0"
  requires: { engine: ">=0.1.0 <0.2.0" }
engine:
  version: "0.1.0+build.7"
  requires: { desktop: ">=0.2.0" }
care:
  version: "3.1.0"
  requires: { desktop: ">=0.2.0", engine: ">=0.1.0" }
  backend:  { repo: "ohcnetwork/care",    ref: "develop" }
  frontend: { repo: "ohcnetwork/care_fe", ref: "develop" }
images:
  postgres: "postgres:17.10-alpine"
  redis: "redis:8.8.0-alpine"
  caddy: "caddy:2.11.4"
  silo: "docker.io/pgsty/silo:RELEASE.2026-09-03T13-18-01Z"
  node: "node:22.23.3-bookworm-slim"
  nginx: "nginx:1.30.6-alpine"
plugins:
  care_onboarding_fe: { repo: "ohcnetwork/care_onboarding_fe", ref: "main", kind: "frontend" }
  care_filly: { repo: "ohcnetwork/care_filly", ref: "develop", kind: "backend" }
tools:
  qemu_windows_x86_64:
    version: "10.1.0"
    url: "https://example.com/qemu.exe"
    sha256: "c7592db7716029e814d71c9a7dfc9b76dc72019753ee9afc2fa70b14c9d28b4f"
`

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		old     string // substring of validYAML to replace
		new     string
		wantErr string // empty means success
	}{
		{name: "valid"},
		{name: "unknown top-level key", old: "schema: 1\n", new: "schema: 1\nextra: true\n", wantErr: "field extra not found"},
		{name: "unknown nested key", old: `ref: "develop" }` + "\n  frontend", new: `ref: "develop", branch: x }` + "\n  frontend", wantErr: "field branch not found"},
		{name: "unquoted float version", old: `version: "0.2.0"`, new: `version: 0.2`, wantErr: "desktop.version: must be a quoted string"},
		{name: "unquoted int version", old: `version: "3.1.0"`, new: `version: 3`, wantErr: "care.version: must be a quoted string"},
		{name: "missing version", old: `version: "3.1.0"`, new: `version:`, wantErr: "care.version: required"},
		{name: "bad semver", old: `"0.1.0+build.7"`, new: `"v0.1.0"`, wantErr: "engine.version: invalid semver"},
		{name: "semver leading zero", old: `"0.1.0+build.7"`, new: `"0.01.0"`, wantErr: "engine.version: invalid semver"},
		{name: "bad range operator", old: `">=0.1.0 <0.2.0"`, new: `"~0.1.0"`, wantErr: "desktop.requires.engine: invalid comparator"},
		{name: "bad range version", old: `engine: ">=0.1.0" }`, new: `engine: ">=0.1" }`, wantErr: "care.requires.engine: invalid comparator"},
		{name: "missing requires", old: `  requires: { desktop: ">=0.2.0" }` + "\n", new: "", wantErr: "engine.requires.desktop: required"},
		{name: "self requirement", old: `{ desktop: ">=0.2.0" }`, new: `{ desktop: ">=0.2.0", engine: "=0.1.0" }`, wantErr: "engine.requires.engine: a component cannot require itself"},
		{name: "wrong schema", old: "schema: 1", new: "schema: 2", wantErr: "schema: must be 1"},
		{name: "bad repo", old: `"ohcnetwork/care_fe"`, new: `"care_fe"`, wantErr: "care.frontend.repo: must be owner/name"},
		{name: "empty ref", old: `care",    ref: "develop"`, new: `care",    ref: ""`, wantErr: "care.backend.ref: required"},
		{name: "latest image", old: `"redis:8.8.0-alpine"`, new: `"redis:latest"`, wantErr: `images.redis: floating tag "latest"`},
		{name: "edge image", old: `"caddy:2.11.4"`, new: `"caddy:edge"`, wantErr: `images.caddy: floating tag "edge"`},
		{name: "untagged image", old: `"caddy:2.11.4"`, new: `"registry:5000/caddy"`, wantErr: "images.caddy: must pin a tag or digest"},
		{name: "digest image", old: `"caddy:2.11.4"`, new: `"caddy@sha256:c7592db7716029e814d71c9a7dfc9b76dc72019753ee9afc2fa70b14c9d28b4f"`},
		{name: "empty image", old: `"postgres:17.10-alpine"`, new: `""`, wantErr: "images.postgres: required"},
		{name: "uppercase sha", old: `"c7592db7`, new: `"C7592DB7`, wantErr: "tools.qemu_windows_x86_64.sha256: must be 64 lowercase hex"},
		{name: "short sha", old: `b4f"`, new: `"`, wantErr: "tools.qemu_windows_x86_64.sha256: must be 64 lowercase hex"},
		{name: "http tool url", old: `"https://example.com`, new: `"http://example.com`, wantErr: "tools.qemu_windows_x86_64.url: must be an https URL"},
		{name: "bad plugin kind", old: `kind: "backend"`, new: `kind: "both"`, wantErr: `plugins.care_filly.kind: must be "backend" or "frontend"`},
		{name: "bad plugin id", old: "  care_filly:", new: "  Care-Filly:", wantErr: "plugins.Care-Filly: id must be"},
		{name: "bad plugin repo", old: `"ohcnetwork/care_filly"`, new: `"care_filly"`, wantErr: "plugins.care_filly.repo: must be owner/name"},
		{name: "empty plugin ref", old: `ref: "develop", kind`, new: `ref: "", kind`, wantErr: "plugins.care_filly.ref: required"},
		{name: "missing node image", old: "  node: \"node:22.23.3-bookworm-slim\"\n", new: "", wantErr: "images.node: required"},
		{name: "multiple documents", old: "schema: 1\n", new: "schema: 1\n---\nschema: 1\n", wantErr: "single YAML document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := validYAML
			if tt.old != "" {
				if !strings.Contains(src, tt.old) {
					t.Fatalf("test setup: %q not found in fixture", tt.old)
				}
				src = strings.Replace(src, tt.old, tt.new, 1)
			}
			f, err := Parse([]byte(src))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if f == nil {
					t.Fatal("nil file")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseValues(t *testing.T) {
	f, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Engine.Version.String(); got != "0.1.0+build.7" {
		t.Errorf("engine version = %q", got)
	}
	if f.Engine.Version.Build != "build.7" || f.Engine.Version.Minor != 1 {
		t.Errorf("engine semver = %+v", f.Engine.Version.Semver)
	}
	if !f.Desktop.Requires.Engine.Check(f.Engine.Version.Semver) {
		t.Error("desktop should accept engine 0.1.0")
	}
	if !f.Care.Requires.Desktop.Check(f.Desktop.Version.Semver) {
		t.Error("care should accept desktop 0.2.0")
	}
	if p := f.Plugins["care_filly"]; p.Kind != PluginBackend || p.Ref != "develop" {
		t.Errorf("plugin decode: %+v", p)
	}
	if f.Care.Backend.Repo != "ohcnetwork/care" || f.Images.Silo == "" {
		t.Errorf("unexpected decode: %+v", f.Care.Backend)
	}
}

func TestRepoVersionsFile(t *testing.T) {
	path := filepath.Join("..", "..", "..", "versions.yml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("repo versions.yml not found: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestConstraint(t *testing.T) {
	tests := []struct {
		constraint string
		version    string
		want       bool
	}{
		{">=1.2.3 <2.0.0", "1.2.3", true},
		{">=1.2.3 <2.0.0", "1.9.99", true},
		{">=1.2.3 <2.0.0", "2.0.0", false},
		{">=1.2.3 <2.0.0", "1.2.2", false},
		{">=1.2.3 <2.0.0", "2.0.0-rc.1", true},
		{"=1.0.0", "1.0.0", true},
		{"=1.0.0", "1.0.0+meta", true},
		{"=1.0.0", "1.0.1", false},
		{">1.0.0", "1.0.0", false},
		{">1.0.0", "1.0.1", true},
		{"<=1.0.0", "1.0.0", true},
		{">=1.0.0", "1.0.0-alpha", false},
		{">1.0.0-alpha.1", "1.0.0-alpha.beta", true},
		{">1.0.0-alpha.9", "1.0.0-alpha.10", true},
	}
	for _, tt := range tests {
		c, err := ParseConstraint(tt.constraint)
		if err != nil {
			t.Fatalf("ParseConstraint(%q): %v", tt.constraint, err)
		}
		v, err := ParseSemver(tt.version)
		if err != nil {
			t.Fatalf("ParseSemver(%q): %v", tt.version, err)
		}
		if got := c.Check(v); got != tt.want {
			t.Errorf("%q.Check(%q) = %v, want %v", tt.constraint, tt.version, got, tt.want)
		}
	}
}

func TestParseConstraintErrors(t *testing.T) {
	for _, s := range []string{"", "   ", "1.0.0", "~1.0.0", "^1.0.0", ">=1.0", ">= 1.0.0", "=>1.0.0", ">=1.0.0 || <0.5.0"} {
		if _, err := ParseConstraint(s); err == nil {
			t.Errorf("ParseConstraint(%q) succeeded, want error", s)
		}
	}
}

func TestParseSemver(t *testing.T) {
	valid := []string{"0.0.0", "1.2.3", "10.20.30", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-0.3.7", "1.0.0+20130313144700", "1.0.0-beta+exp.sha.5114f85"}
	for _, s := range valid {
		v, err := ParseSemver(s)
		if err != nil {
			t.Errorf("ParseSemver(%q): %v", s, err)
			continue
		}
		if v.String() != s {
			t.Errorf("round trip %q -> %q", s, v.String())
		}
	}
	invalid := []string{"", "1", "1.2", "1.2.3.4", "v1.2.3", "01.2.3", "1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-a..b", "1.2.3-a_b", "-1.2.3"}
	for _, s := range invalid {
		if _, err := ParseSemver(s); err == nil {
			t.Errorf("ParseSemver(%q) succeeded, want error", s)
		}
	}
}
