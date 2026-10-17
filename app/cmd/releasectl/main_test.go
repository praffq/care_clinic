package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func TestVerifyRequiresExplicitDevelopmentMode(t *testing.T) {
	hash := strings.Repeat("a", 64)
	image := "ghcr.io/example/image@sha256:" + hash
	a := release.Asset{URL: "https://example.test/asset", SHA256: hash, Size: 42}
	build := release.ImageBuild{Commit: strings.Repeat("b", 40), Image: image, Builder: image, SourceHash: hash}
	frontend := build
	frontend.BuildEnv = map[string]string{"EMPTY": ""}
	m := release.Manifest{
		Schema: 1, Revision: 1, PublishedAt: "2026-10-14T14:00:00Z",
		Desktop: release.DesktopUpdate{Version: "0.0.2", RequiresEngine: ">=0.1.2",
			Assets: map[string]release.Asset{"darwin-universal": a, "windows-amd64": a}},
		Engine: release.EngineUpdate{Version: "0.1.2", RequiresDesktop: ">=0.0.1",
			Assets: map[string]release.Asset{"linux-arm64": a, "linux-amd64": a}},
		Care: release.CareUpdate{Version: "3.1.0", RequiresDesktop: ">=0.0.1", RequiresEngine: ">=0.1.2",
			Backend: build, Frontend: frontend},
		SupportImages: map[string]string{"postgres": image, "redis": image, "caddy": image, "silo": image, "backup": image},
		Blocked:       []string{}, SourceHashes: map[string]string{"desktop": hash, "engine": hash, "support": hash},
	}
	path := filepath.Join(t.TempDir(), "updates.json")
	write := func(m release.Manifest) {
		t.Helper()
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(m)
	if err := run([]string{"verify", "--manifest", path, "--unsigned-development"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--manifest", path}); err == nil {
		t.Fatal("default verifier accepted unsigned metadata")
	}
	if err := run([]string{"sign", "--manifest", path, "--unsigned-development"}); err == nil ||
		!strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("sign command accepted development bypass: %v", err)
	}
	m.Engine.Assets = nil
	write(m)
	if err := run([]string{"verify", "--manifest", path, "--unsigned-development"}); err == nil {
		t.Fatal("development verifier skipped manifest validation")
	}
}

func TestFrontendDefaultsAreBuildInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deployments"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "deployments", "env-defaults.yml")
	for _, tc := range []struct{ data, want string }{
		{"be:\n  SECRET: ignored\nfe:\n  REACT_FLAG: \"true\"\n", "true"},
		{"be: {}\nfe:\n  REACT_FLAG: \"\"\n", ""},
	} {
		if err := os.WriteFile(path, []byte(tc.data), 0o644); err != nil {
			t.Fatal(err)
		}
		env, err := frontendEnv(root)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := env["REACT_FLAG"]; !ok || value != tc.want || len(env) != 1 {
			t.Fatalf("frontend defaults = %#v", env)
		}
	}
}

func TestHashAndAssetMeasurements(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "asset")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := release.AssetForFile(path, "https://github.com/example/repo/releases/download/v1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != 3 || a.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" ||
		a.URL != "https://github.com/example/repo/releases/download/v1/asset" {
		t.Fatalf("asset metadata = %#v", a)
	}
	before, err := release.SourceHash(root, "asset")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := release.SourceHash(root, "asset")
	if err != nil || before == after {
		t.Fatalf("content change not detected: %v", err)
	}
}
