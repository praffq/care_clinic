// Package feed defines the signed update feed (updates.json) that desktop
// clients read to discover desktop, engine and CARE releases, and verifies it
// against a set of trusted Ed25519 keys. See docs/updates-feed.md.
package feed

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

// SchemaVersion is the only manifest schema this package accepts.
const SchemaVersion = 1

// Component names used in requires and blocked entries.
const (
	ComponentDesktop = "desktop"
	ComponentEngine  = "engine"
	ComponentCare    = "care"
)

// Desktop asset platforms.
const (
	PlatformDarwinARM64  = "darwin-arm64"
	PlatformDarwinAMD64  = "darwin-amd64"
	PlatformWindowsAMD64 = "windows-amd64"
)

// Engine asset architectures.
const (
	ArchARM64 = "arm64"
	ArchAMD64 = "amd64"
)

// Manifest is the decoded contents of updates.json.
type Manifest struct {
	Schema        int                             `json:"schema"`
	Revision      uint64                          `json:"revision"`
	PublishedAt   time.Time                       `json:"published_at"`
	Expires       time.Time                       `json:"expires"`
	Desktop       []DesktopRelease                `json:"desktop"`
	Engine        []EngineRelease                 `json:"engine"`
	Care          []CareRelease                   `json:"care"`
	SupportImages map[protocol.ImageRole]ImageRef `json:"support_images"`
	Blocked       []string                        `json:"blocked"`
}

// DesktopRelease is one installable desktop app version.
type DesktopRelease struct {
	Version  Version          `json:"version"`
	Requires Requires         `json:"requires"`
	Assets   map[string]Asset `json:"assets"` // keyed by Platform*
}

// EngineRelease is one engine (VM image) version.
type EngineRelease struct {
	Version    Version          `json:"version"`
	Requires   Requires         `json:"requires"`
	DataSchema int              `json:"data_schema"`
	Assets     map[string]Asset `json:"assets"` // keyed by Arch*
}

// CareRelease is one CARE application version and its pinned images.
type CareRelease struct {
	Version    Version                         `json:"version"`
	Requires   Requires                        `json:"requires"`
	Migrations bool                            `json:"migrations"`
	Images     map[protocol.ImageRole]ImageRef `json:"images"`
	BuildEnv   map[string]string               `json:"build_env,omitempty"`
}

// Requires lists version constraints on sibling components.
type Requires struct {
	Desktop *Constraint `json:"desktop,omitempty"`
	Engine  *Constraint `json:"engine,omitempty"`
}

// Asset is a downloadable file pinned by size and SHA-256.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ImageRef is a container image pinned by digest. Tar is an optional image
// tarball URL meant for development feeds that side-load locally built images.
type ImageRef struct {
	Ref    string          `json:"ref"`
	Digest protocol.Digest `json:"digest"`
	Tar    string          `json:"tar,omitempty"`
}

// Version is a semver string. The parsed value is filled in by Verify.
type Version struct {
	versions.Semver
	raw string
}

// String returns the version exactly as written in the feed.
func (v Version) String() string { return v.raw }

func (v Version) MarshalJSON() ([]byte, error) { return json.Marshal(v.raw) }

// UnmarshalJSON records the raw string; it is parsed during validation so
// errors carry the field path.
func (v *Version) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &v.raw); err != nil {
		return fmt.Errorf("version must be a string")
	}
	return nil
}

// Constraint is a semver range. The parsed value is filled in by Verify.
type Constraint struct {
	versions.Constraint
	raw string
}

// String returns the constraint exactly as written in the feed.
func (c Constraint) String() string { return c.raw }

func (c Constraint) MarshalJSON() ([]byte, error) { return json.Marshal(c.raw) }

// UnmarshalJSON records the raw string; it is parsed during validation.
func (c *Constraint) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &c.raw); err != nil {
		return fmt.Errorf("constraint must be a string")
	}
	return nil
}

// NewVersion parses s, for building manifests in code.
func NewVersion(s string) (Version, error) {
	sv, err := versions.ParseSemver(s)
	return Version{Semver: sv, raw: s}, err
}

// NewConstraint parses s, for building manifests in code.
func NewConstraint(s string) (*Constraint, error) {
	c, err := versions.ParseConstraint(s)
	if err != nil {
		return nil, err
	}
	return &Constraint{Constraint: c, raw: s}, nil
}
