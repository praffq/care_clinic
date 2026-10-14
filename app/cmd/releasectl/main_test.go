package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

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
