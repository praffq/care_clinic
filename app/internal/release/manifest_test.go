package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManifest(t *testing.T) Manifest {
	t.Helper()
	hash := strings.Repeat("a", 64)
	image := "ghcr.io/example/care@sha256:" + hash
	a := Asset{URL: "https://github.com/example/care/releases/download/v1/file", SHA256: hash, Size: 42}
	build := ImageBuild{Commit: strings.Repeat("b", 40), Image: image, Builder: image, SourceHash: hash}
	frontend := build
	frontend.BuildEnv = map[string]string{"EMPTY": ""}
	return Manifest{
		Schema: 1, Revision: 1, PublishedAt: "2026-10-14T14:00:00Z",
		Desktop:       DesktopUpdate{"0.1.19", ">=0.1.2", map[string]Asset{"darwin-universal": a, "windows-amd64": a}},
		Engine:        EngineUpdate{"0.1.2", ">=0.1.19", map[string]Asset{"linux-arm64": a, "linux-amd64": a}},
		Care:          CareUpdate{"3.1.0", ">=0.1.19", ">=0.1.2", build, frontend},
		SupportImages: map[string]string{"postgres": image, "redis": image, "caddy": image, "silo": image, "backup": image},
		Blocked:       []string{}, SourceHashes: map[string]string{"desktop": hash, "engine": hash, "support": hash},
	}
}

func TestCanonicalDigestReferencesRetainRepositoryPortsAndDigests(t *testing.T) {
	m := testManifest(t)
	digest := "@sha256:" + strings.Repeat("a", 64)
	m.SupportImages["silo"] = "docker.io/pgsty/silo:RELEASE.2026-09-03T13-18-01Z" + digest
	m.Care.Backend.Image = "localhost:5000/team/app:RC1" + digest
	m.Care.Backend.Builder = "localhost:5000/team/builder" + digest
	m.CanonicalizeImageReferences()
	if m.SupportImages["silo"] != "docker.io/pgsty/silo"+digest ||
		m.Care.Backend.Image != "localhost:5000/team/app"+digest ||
		m.Care.Backend.Builder != "localhost:5000/team/builder"+digest {
		t.Fatal("canonicalization changed repository, registry port or digest")
	}
	m.SupportImages["silo"] = "docker.io/pgsty/silo:RELEASE"
	m.CanonicalizeImageReferences()
	if err := m.Validate(); err == nil {
		t.Fatal("canonicalization must not make an unpinned image valid")
	}
}

func TestJobsEngineManifestRequiresPublishedNativeAssets(t *testing.T) {
	m := testManifest(t)
	m.Engine.Version = "0.1.3"
	if err := m.Validate(); err == nil {
		t.Fatal("new jobs engine accepted without immutable native agent assets")
	}
	for _, arch := range []string{"arm64", "amd64"} {
		m.Engine.Assets["native-linux-"+arch] = Asset{
			URL:    "https://example.test/care-engine-v0.1.3/care-engine-agent-linux-" + arch,
			SHA256: strings.Repeat("c", 64), Size: 42,
		}
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestVerification(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	encoded := base64.StdEncoding.EncodeToString(pub)
	m := testManifest(t)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(key, data)
	for _, tt := range []struct {
		name            string
		data, signature []byte
		highest         uint64
		keys            []string
		ok              bool
	}{
		{"current", data, signature, 1, []string{encoded, "invalid"}, true},
		{"next", data, signature, 0, []string{"invalid", encoded}, true},
		{"rollback", data, signature, 2, []string{encoded}, false},
		{"modified", append(append([]byte{}, data...), '\n'), signature, 0, []string{encoded}, false},
		{"wrong key", data, signature, 0, PublicKeys, false},
		{"short signature", data, signature[:63], 0, []string{encoded}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyWithKeys(tt.data, tt.signature, tt.highest, tt.keys)
			if (err == nil) != tt.ok {
				t.Fatalf("verification error = %v", err)
			}
		})
	}
	original := PublicKeys
	PublicKeys = []string{encoded}
	t.Cleanup(func() { PublicKeys = original })
	signed, err := SignManifest(data, base64.StdEncoding.EncodeToString(key))
	if err != nil || !ed25519.Verify(pub, data, signed) {
		t.Fatalf("sign manifest: %v", err)
	}
	c := NewClient(t.TempDir())
	if _, err := c.accept(data, signature); err != nil {
		t.Fatal(err)
	}
	if _, err := c.accept(data, signature); err != nil {
		t.Fatalf("identical revision rejected: %v", err)
	}
	m.PublishedAt = "2026-10-14T14:01:00Z"
	changed, _ := json.Marshal(m)
	if _, err := c.accept(changed, ed25519.Sign(key, changed)); err == nil {
		t.Fatal("reused revision accepted")
	}
	m.Revision = 2
	newer, _ := json.Marshal(m)
	if _, err := c.accept(newer, ed25519.Sign(key, newer)); err != nil {
		t.Fatal(err)
	}
	restarted := &Client{StatePath: c.StatePath}
	if _, err := restarted.accept(data, signature); err == nil {
		t.Fatal("rollback accepted after restart")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(c.StatePath), "verified.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.accept(newer, ed25519.Sign(key, newer)); err == nil {
		t.Fatal("corrupt state silently reset")
	}
}

func TestManifestRequiresCompleteProvenance(t *testing.T) {
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Schema = 2 },
		func(m *Manifest) { m.Revision = 0 },
		func(m *Manifest) { m.PublishedAt = "2026-10-14T10:00:00-04:00" },
		func(m *Manifest) { m.Desktop.Version = "01.2.3" },
		func(m *Manifest) { m.Care.Version = "3.0.0" },
		func(m *Manifest) { m.Engine.RequiresDesktop = "nonsense" },
		func(m *Manifest) { delete(m.Engine.Assets, "linux-arm64") },
		func(m *Manifest) {
			a := m.Engine.Assets["linux-arm64"]
			a.SHA256 = strings.Repeat("0", 64)
			m.Engine.Assets["linux-arm64"] = a
		},
		func(m *Manifest) { m.Care.Backend.Image = "care:latest" },
		func(m *Manifest) { m.Care.Frontend.BuildEnv = nil },
		func(m *Manifest) { m.Blocked = nil },
		func(m *Manifest) { delete(m.SupportImages, "redis") },
		func(m *Manifest) { delete(m.SourceHashes, "desktop") },
	} {
		m := testManifest(t)
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	m := testManifest(t)
	data, _ := json.Marshal(m)
	if _, err := DecodeManifest(data); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(data), `"schema":1`, `"unknown":1,"schema":1`, 1), string(data) + "{}"} {
		if _, err := DecodeManifest([]byte(bad)); err == nil {
			t.Fatal("non-strict manifest accepted")
		}
	}
}
