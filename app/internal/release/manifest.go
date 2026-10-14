package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"time"
)

// Bootstrap anchors must be replaced with operator-held public keys before the
// first publication. Their private halves were discarded, not committed.
var PublicKeys = []string{
	"PJx+t0rUPizPt0Bx+/FvatOcmfD3pWi5YXVK9SLYNQY=",
	"7EMSvsW3Dd7ws3LIgwVrIAekBDqXzqQNUAb1sQkd3Fc=",
}

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type DesktopUpdate struct {
	Version        string           `json:"version"`
	RequiresEngine string           `json:"requires_engine"`
	Assets         map[string]Asset `json:"assets"`
}

type EngineUpdate struct {
	Version         string           `json:"version"`
	RequiresDesktop string           `json:"requires_desktop"`
	Assets          map[string]Asset `json:"assets"`
}

type ImageBuild struct {
	RuntimeKeys   []string          `json:"runtime_keys,omitempty"`
	Commit     string            `json:"commit"`
	Image      string            `json:"image"`
	Builder    string            `json:"builder"`
	BuildEnv   map[string]string `json:"build_env,omitempty"`
	SourceHash string            `json:"source_hash"`
}

type CareUpdate struct {
	Version         string     `json:"version"`
	RequiresDesktop string     `json:"requires_desktop"`
	RequiresEngine  string     `json:"requires_engine"`
	Backend         ImageBuild `json:"backend"`
	Frontend        ImageBuild `json:"frontend"`
}

type Manifest struct {
	Schema        int               `json:"schema"`
	Revision      uint64            `json:"revision"`
	PublishedAt   string            `json:"published_at"`
	Desktop       DesktopUpdate     `json:"desktop"`
	Engine        EngineUpdate      `json:"engine"`
	Care          CareUpdate        `json:"care"`
	SupportImages map[string]string `json:"support_images"`
	Blocked       []string          `json:"blocked"`
	SourceHashes  map[string]string `json:"source_hashes"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, fmt.Errorf("manifest has trailing data")
	}
	return m, m.Validate()
}

func (m Manifest) Validate() error {
	if m.Schema != 1 || m.Revision == 0 {
		return fmt.Errorf("manifest requires schema 1 and a positive revision")
	}
	at, err := time.Parse(time.RFC3339, m.PublishedAt)
	if err != nil || at.Location() != time.UTC {
		return fmt.Errorf("published_at must be a UTC RFC3339 timestamp")
	}
	if err := validateUpdateVersions(m); err != nil {
		return err
	}
	for name, assets := range map[string]map[string]Asset{"desktop": m.Desktop.Assets, "engine": m.Engine.Assets} {
		for _, platform := range requiredPlatforms(name) {
			if _, ok := assets[platform]; !ok {
				return fmt.Errorf("%s missing %s asset", name, platform)
			}
		}
		for platform, a := range assets {
			u, err := url.Parse(a.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" ||
				!sha256Pattern.MatchString(a.SHA256) || a.Size <= 0 {
				return fmt.Errorf("invalid %s asset %s", name, platform)
			}
		}
	}
	for name, image := range map[string]ImageBuild{"backend": m.Care.Backend, "frontend": m.Care.Frontend} {
		if !commitPattern.MatchString(image.Commit) || !digestRefPattern.MatchString(image.Image) ||
			!digestRefPattern.MatchString(image.Builder) || !sha256Pattern.MatchString(image.SourceHash) {
			return fmt.Errorf("invalid %s build provenance", name)
		}
	}
	if m.Care.Frontend.BuildEnv == nil || m.Blocked == nil {
		return fmt.Errorf("frontend build_env and blocked must be present")
	}
	for _, name := range []string{"postgres", "redis", "caddy", "silo", "backup"} {
		if !digestRefPattern.MatchString(m.SupportImages[name]) {
			return fmt.Errorf("support image %s must be pinned by digest", name)
		}
	}
	for _, name := range []string{"desktop", "engine", "support"} {
		if !sha256Pattern.MatchString(m.SourceHashes[name]) {
			return fmt.Errorf("missing source hash for %s", name)
		}
	}
	return nil
}

func requiredPlatforms(component string) []string {
	if component == "desktop" {
		return []string{"darwin-universal", "windows-amd64"}
	}
	return []string{"linux-arm64", "linux-amd64"}
}

// VerifyManifest authenticates exact bytes before parsing or accepting revision.
// The caller must persist Revision atomically before using any advertised asset.
func VerifyManifest(data, signature []byte, highest uint64) (Manifest, error) {
	return VerifyWithKeys(data, signature, highest, PublicKeys)
}

func VerifyWithKeys(data, signature []byte, highest uint64, keys []string) (Manifest, error) {
	valid := false
	for _, encoded := range keys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && len(key) == ed25519.PublicKeySize && ed25519.Verify(key, data, signature) {
			valid = true
			break
		}
	}
	if !valid {
		return Manifest{}, fmt.Errorf("updates signature is not trusted")
	}
	m, err := DecodeManifest(data)
	if err != nil {
		return m, err
	}
	if m.Revision < highest {
		return m, fmt.Errorf("manifest rollback: revision %d below %d", m.Revision, highest)
	}
	return m, nil
}

func SignManifest(data []byte, encoded string) ([]byte, error) {
	if _, err := DecodeManifest(data); err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key must be a base64 Ed25519 private key (64 bytes)")
	}
	signature := ed25519.Sign(ed25519.PrivateKey(key), data)
	if _, err := VerifyManifest(data, signature, 0); err != nil {
		return nil, fmt.Errorf("signing key does not match embedded trust anchors: %w", err)
	}
	return signature, nil
}
